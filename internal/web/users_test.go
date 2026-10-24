package web

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// secretBoard is the small board with a hidden group, "Secret society", that
// alice and bob belong to.
func secretBoard() *bundletest.Board {
	b := bundletest.Small()
	b.Groups = append(b.Groups, bundle.Group{ID: 6, Name: "Secret society", Kind: "hidden"})
	b.GroupMembers = append(b.GroupMembers,
		bundle.GroupMember{GroupID: 6, UserID: 1},
		bundle.GroupMember{GroupID: 6, UserID: 2},
	)
	return b
}

// withMembers adds n active members named member001 and on, who have 1 to n posts.
func withMembers(b *bundletest.Board, n int) *bundletest.Board {
	proto := b.Users[2]
	for i := 1; i <= n; i++ {
		u := proto
		u.ID = int64(100 + i)
		u.Username = fmt.Sprintf("member%03d", i)
		u.Email = fmt.Sprintf("member%03d@example.com", i)
		u.RegisteredAt = time.Date(2021, 1, 1, 0, i, 0, 0, time.UTC)
		u.PostCount = i
		b.Users = append(b.Users, u)
		b.GroupMembers = append(b.GroupMembers, bundle.GroupMember{GroupID: 2, UserID: u.ID})
	}
	return b
}

func TestProfilePages(t *testing.T) {
	h, _ := smallSite(t)
	bob := signIn(t, h, "bob", "example-password")

	rec := getAs(t, h, "/u/1", bob)
	if rec.Code != http.StatusOK {
		t.Fatalf("/u/1 as bob: status %d", rec.Code)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{
		`<bdi>alice</bdi>`, `src="/files/ranks/newcomer.png"`, `src="/files/avatars/1.png"`,
		`Example signature`, `Newcomer`, `admins`, `members`, `120`, `May 1, 2019`, `Oct 1, 2026`,
		`href="/search?author=alice"`, `Find posts by this user`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/u/1 lacks %q", want)
		}
	}
	if strings.Contains(body, "alice@example.com") {
		t.Error("the profile shows the email address")
	}
	checkBasePath(t, "/u/1", body, "/")

	// A member who never visited, with no signature and a pending group.
	body = bodyOf(t, getAs(t, h, "/u/3", bob))
	if strings.Contains(body, "Beta testers") || !strings.Contains(body, "No visit recorded") {
		t.Errorf("/u/3: pending group shown or last visit missing")
	}

	// A guest gets 403 and a way to sign in that returns to the profile.
	rec = get(t, h, "/u/1")
	body = bodyOf(t, rec)
	if rec.Code != http.StatusForbidden || !strings.Contains(body, `href="/login?next=%2Fu%2F1"`) {
		t.Errorf("/u/1 as guest: status %d, sign-in link missing", rec.Code)
	}
	if strings.Contains(body, "alice") || strings.Contains(body, "Example signature") {
		t.Error("the 403 page shows the profile")
	}

	rec = getAs(t, h, "/u/8", bob)
	if rec.Code != http.StatusGone || !strings.Contains(bodyOf(t, rec), "This account no longer exists.") {
		t.Errorf("/u/8: status %d", rec.Code)
	}
	if rec := get(t, h, "/u/8"); rec.Code != http.StatusGone {
		t.Errorf("/u/8 as guest: status %d, want 410", rec.Code)
	}
	// An inactive member cannot sign in; the profile is there, as the posts are.
	rec = getAs(t, h, "/u/4", bob)
	if body := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(body, "<bdi>dave</bdi>") || strings.Contains(body, "@example.com") {
		t.Errorf("/u/4: status %d", rec.Code)
	}
	if rec := get(t, h, "/u/4"); rec.Code != http.StatusForbidden {
		t.Errorf("/u/4 as guest: status %d, want 403", rec.Code)
	}
	if body := bodyOf(t, getAs(t, h, "/members", bob)); strings.Contains(body, "dave") {
		t.Error("the member list shows an inactive member")
	}
	for _, p := range []string{"/u/99", "/u/0", "/u/-1", "/u/abc", "/u/01"} {
		if rec := getAs(t, h, p, bob); rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", p, rec.Code)
		}
	}
}

// No profile, list or team page shows an email address, an IP address or a hidden
// group, to anyone: not to a member, a moderator, an admin or a guest.
func TestNoPrivateDataOnUserPages(t *testing.T) {
	h, _ := newTestSite(t, secretBoard(), Options{})
	people := map[string][]*http.Cookie{
		"guest": nil,
		"alice": signIn(t, h, "alice", "example-password"), // owner and admin
		"bob":   signIn(t, h, "bob", "example-password"),   // moderator
		"carol": signIn(t, h, "carol", "example-password"),
	}
	for who, cookies := range people {
		for _, p := range []string{"/u/1", "/u/2", "/u/3", "/members", "/members?sort=posts", "/team"} {
			body := bodyOf(t, getAs(t, h, p, cookies))
			for _, bad := range []string{"@example.com", "10.0.0.", "Secret society", "spammer"} {
				if strings.Contains(body, bad) {
					t.Errorf("%s sees %q on %s", who, bad, p)
				}
			}
		}
	}
	// The same page shows the confirmed groups that are open to view.
	if body := bodyOf(t, getAs(t, h, "/u/2", people["alice"])); !strings.Contains(body, "Beta testers") {
		t.Error("bob's profile lacks Beta testers")
	}
}

func TestMemberAndTeamPages(t *testing.T) {
	h, _ := newTestSite(t, withMembers(bundletest.Small(), 60), Options{})
	bob := signIn(t, h, "bob", "example-password")

	rec := getAs(t, h, "/members", bob)
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("/members: status %d", rec.Code)
	}
	// 63 members: 50 on page 1.
	for _, want := range []string{
		`href="/members?page=2"`, `href="/members?sort=joined"`, `href="/members?sort=posts"`,
		`aria-sort="ascending"`, `href="/u/1"`, `<bdi>alice</bdi>`, `Page 1 of 2`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/members lacks %q", want)
		}
	}
	if strings.Contains(body, `href="/members?sort=name"`) {
		t.Error("the default sort has a sort parameter")
	}
	if strings.Contains(body, "dave") || strings.Contains(body, "member051") {
		t.Error("/members lists an inactive user or the second page")
	}

	// The sort stays in the page links, and page 1 has no page parameter.
	body = bodyOf(t, getAs(t, h, "/members?sort=posts", bob))
	if !strings.Contains(body, `href="/members?sort=posts&amp;page=2"`) || strings.Contains(body, "page=1") {
		t.Error("the page links of /members?sort=posts are wrong")
	}
	if i, j := strings.Index(body, "member060"), strings.Index(body, "member059"); i < 0 || j < i {
		t.Errorf("posts are not in descending order (%d, %d)", i, j)
	}
	body = bodyOf(t, getAs(t, h, "/members?sort=posts&page=2", bob))
	if !strings.Contains(body, "carol") || !strings.Contains(body, "member001") || strings.Contains(body, "alice") || strings.Contains(body, "member060") || !strings.Contains(body, `rel="prev" href="/members?sort=posts"`) {
		t.Error("page 2 by posts is wrong")
	}
	if rec := getAs(t, h, "/members?page=3", bob); rec.Code != http.StatusNotFound {
		t.Errorf("page 3: status %d, want 404", rec.Code)
	}
	for _, q := range []string{"0", "-1", "abc", "999999999999999999999999999999", "+2"} {
		if rec := getAs(t, h, "/members?page="+q, bob); rec.Code != http.StatusBadRequest {
			t.Errorf("/members?page=%s: status %d, want 400", q, rec.Code)
		}
	}
	// An unknown sort is the name sort.
	if rec := getAs(t, h, "/members?sort=bogus", bob); rec.Code != http.StatusOK {
		t.Errorf("sort=bogus: status %d", rec.Code)
	}

	rec = get(t, h, "/members")
	if rec.Code != http.StatusForbidden || !strings.Contains(bodyOf(t, rec), `href="/login?next=%2Fmembers"`) {
		t.Errorf("guest /members: status %d", rec.Code)
	}

	// The team is public.
	rec = get(t, h, "/team")
	body = bodyOf(t, rec)
	if rec.Code != http.StatusOK {
		t.Fatalf("/team: status %d", rec.Code)
	}
	ia, ib := strings.Index(body, "alice"), strings.Index(body, "bob")
	if ia < 0 || ib < ia || !strings.Contains(body, "Owners") || !strings.Contains(body, "moderators") {
		t.Errorf("/team: alice at %d, bob at %d", ia, ib)
	}
	if strings.Contains(body, "carol") {
		t.Error("/team lists a plain member")
	}
}

// The small board has one page of members, so page 2 is past the end.
func TestMembersPastTheEnd(t *testing.T) {
	h, _ := smallSite(t)
	bob := signIn(t, h, "bob", "example-password")
	if rec := getAs(t, h, "/members?sort=posts&page=2", bob); rec.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rec.Code)
	}
}

func TestNavLinks(t *testing.T) {
	h, _ := smallSite(t)
	guest := bodyOf(t, get(t, h, "/"))
	if !strings.Contains(guest, `href="/team"`) || strings.Contains(guest, `href="/members"`) {
		t.Error("the guest nav should hold Team and not Members")
	}
	bob := signIn(t, h, "bob", "example-password")
	body := bodyOf(t, getAs(t, h, "/team", bob))
	if !strings.Contains(body, `href="/members"`) || !strings.Contains(body, `href="/team" aria-current="page"`) {
		t.Error("the member nav should hold Members, and Team as the current page")
	}
	if body := bodyOf(t, getAs(t, h, "/members", bob)); !strings.Contains(body, `href="/members" aria-current="page"`) {
		t.Error("Members is not the current page on /members")
	}
}

func TestStaticPages(t *testing.T) {
	h, _ := smallSite(t)
	rec := get(t, h, "/pages/about")
	body := bodyOf(t, rec)
	if rec.Code != http.StatusOK || !strings.Contains(body, "This is an example board.") || !strings.Contains(body, "<h1>About</h1>") {
		t.Errorf("/pages/about: status %d", rec.Code)
	}
	if !strings.Contains(bodyOf(t, get(t, h, "/pages/rules")), "Stay on topic.") {
		t.Error("/pages/rules lacks its text")
	}
	if rec := get(t, h, "/pages/nope"); rec.Code != http.StatusNotFound {
		t.Errorf("/pages/nope: status %d", rec.Code)
	}
	// The footer of every page lists the static pages, in slug order.
	for _, p := range []string{"/", "/pages/about", "/pages/nope", "/login"} {
		body := bodyOf(t, get(t, h, p))
		i, j := strings.Index(body, `href="/pages/about"`), strings.Index(body, `href="/pages/rules"`)
		if i < 0 || j < i {
			t.Errorf("the footer of %s lists the pages at %d and %d", p, i, j)
		}
	}
}

func TestAccountRedirect(t *testing.T) {
	h, _ := smallSite(t)
	rec := get(t, h, "/account")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?next=%2Faccount" {
		t.Errorf("guest: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	bob := signIn(t, h, "bob", "example-password")
	rec = getAs(t, h, "/account", bob)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/u/2" {
		t.Errorf("bob: %d %q", rec.Code, rec.Header().Get("Location"))
	}
}

// Every author name on a list links to the profile, and so does the name of the
// last poster. A guest sees the links too: the profile answers with the way to
// sign in.
func TestAuthorLinks(t *testing.T) {
	h, pool := siteWithPool(t, pagedBoard(), Options{})
	for _, p := range []string{"/", "/f/2"} {
		body := bodyOf(t, get(t, h, p))
		if !strings.Contains(body, `<a href="/u/1"><bdi>alice</bdi></a>`) {
			t.Errorf("%s: the last poster does not link to the profile", p)
		}
	}
	if !strings.Contains(bodyOf(t, get(t, h, "/t/7")), `<a href="/u/1"><bdi>alice</bdi></a>`) {
		t.Error("/t/7: the author does not link")
	}
	// The "new since your last visit" list names the last poster the same way.
	if _, err := pool.Exec(t.Context(), `update users set last_visit_at = '2021-01-01T00:00:00Z', last_mark_at = null where id = 3`); err != nil {
		t.Fatal(err)
	}
	carol := signIn(t, h, "carol", "example-password")
	if !strings.Contains(bodyOf(t, getAs(t, h, "/new", carol)), `<a href="/u/1"><bdi>alice</bdi></a>`) {
		t.Error("/new: the last poster does not link to the profile")
	}
}

// A static page with no title shows its slug in the window title, as it does in
// the heading.
func TestStaticPageWithoutTitle(t *testing.T) {
	h, pool := smallSite2(t)
	if _, err := pool.Exec(t.Context(), `update static_pages set title = '' where slug = 'rules'`); err != nil {
		t.Fatal(err)
	}
	body := bodyOf(t, get(t, h, "/pages/rules"))
	if !strings.Contains(body, "<title>rules - Example Board</title>") || !strings.Contains(body, "<h1>rules</h1>") {
		t.Errorf("an empty title: %s", body[:min(len(body), 400)])
	}
}

// smallSite2 is smallSite with the pool.
func smallSite2(t *testing.T) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	return siteWithPool(t, bundletest.Small(), Options{})
}

func TestProfilePagesUnderBasePath(t *testing.T) {
	h, _ := newTestSite(t, secretBoard(), Options{BasePath: "/forum/"})
	alice := signInAt(t, h, "/forum/", "alice")
	for _, p := range []string{"/forum/u/2", "/forum/members", "/forum/team", "/forum/pages/about"} {
		rec := getAs(t, h, p, alice)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		checkBasePath(t, p, bodyOf(t, rec), "/forum/")
	}
	if body := bodyOf(t, getAs(t, h, "/forum/u/2", alice)); !strings.Contains(body, `href="/forum/search?author=bob"`) {
		t.Error("the search link lacks the base path")
	}
	for _, p := range []string{"/forum/u/2", "/forum/members"} {
		rec := get(t, h, p)
		if rec.Code != http.StatusForbidden {
			t.Errorf("guest %s: status %d", p, rec.Code)
		}
		checkBasePath(t, "guest "+p, bodyOf(t, rec), "/forum/")
	}
	if rec := get(t, h, "/forum/account"); rec.Header().Get("Location") != "/forum/login?next=%2Fforum%2Faccount" {
		t.Errorf("guest /forum/account: %q", rec.Header().Get("Location"))
	}
	if rec := getAs(t, h, "/forum/account", alice); rec.Header().Get("Location") != "/forum/u/1" {
		t.Errorf("alice /forum/account: %q", rec.Header().Get("Location"))
	}
}
