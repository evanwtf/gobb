package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestMemberViews(t *testing.T) {
	// In the fixture no group holds account.download, so a file with no post
	// is 403 even for its uploader. Members get the right here.
	b := bundletest.Small()
	members, yes, download := int64(2), bundle.Yes, "account.download"
	b.Grants = append(b.Grants, bundle.Grant{GroupID: &members, ForumID: 0, Option: &download, Setting: &yes})
	h, _ := newTestSite(t, b, Options{})
	alice := signIn(t, h, "alice", testPassword)
	bob := signIn(t, h, "bob", testPassword)
	carol := signIn(t, h, "carol", testPassword)

	// alice may read the members' lounge, which a guest cannot.
	for _, p := range []string{"/f/3", "/t/2"} {
		if rec := getAs(t, h, p, alice); rec.Code != http.StatusOK {
			t.Errorf("alice %s = %d", p, rec.Code)
		}
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("guest %s = %d, want 404", p, rec.Code)
		}
	}
	// carol is not in the lounge.
	if rec := getAs(t, h, "/f/3", carol); rec.Code != http.StatusNotFound {
		t.Errorf("carol /f/3 = %d, want 404", rec.Code)
	}

	// Post 3 waits for approval. A moderator and its author see it, marked.
	const waiting = "Waiting for approval"
	for name, c := range map[string][]*http.Cookie{"bob": bob, "carol": carol} {
		body := bodyOf(t, getAs(t, h, "/t/1", c))
		if !strings.Contains(body, "This reply waits for a moderator.") || !strings.Contains(body, waiting) {
			t.Errorf("%s does not see post 3 marked as waiting", name)
		}
	}
	// Other members and guests do not see it.
	for name, c := range map[string][]*http.Cookie{"guest": nil} {
		if strings.Contains(bodyOf(t, getAs(t, h, "/t/1", c)), "This reply waits for a moderator.") {
			t.Errorf("%s sees post 3", name)
		}
	}
	// Post 3 is the only waiting one: bob sees one mark on the page's posts.
	if n := strings.Count(bodyOf(t, getAs(t, h, "/t/1", bob)), `<span class="tag tag-warn">Waiting for approval</span>`); n != 1 {
		t.Errorf("bob sees %d waiting marks, want 1", n)
	}

	// An attachment with no post: its uploader may fetch it; bob gets a 404.
	if rec := getAs(t, h, "/a/2", alice); rec.Code != http.StatusOK {
		t.Errorf("alice /a/2 = %d, want 200", rec.Code)
	}
	if rec := getAs(t, h, "/a/2", bob); rec.Code != http.StatusNotFound {
		t.Errorf("bob /a/2 = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/a/2"); rec.Code != http.StatusNotFound {
		t.Errorf("guest /a/2 = %d, want 404", rec.Code)
	}
}

// A member holds the grants of the member's own groups and nothing of the
// guests'. A member who leaves the members group loses what only that group
// gave (spec 9.1).
func TestMembersDoNotInheritGuests(t *testing.T) {
	b := bundletest.Small()
	var kept []bundle.GroupMember
	for _, m := range b.GroupMembers {
		if m.GroupID == 2 && m.UserID == 3 {
			continue
		}
		kept = append(kept, m)
	}
	b.GroupMembers = kept
	b.Grants = append(b.Grants, guestGrant(2, "forum.read"))
	h, _ := newTestSite(t, b, Options{})
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK {
		t.Fatalf("guest /t/1 = %d, want 200", rec.Code)
	}
	carol := signIn(t, h, "carol", testPassword)
	if rec := getAs(t, h, "/t/1", carol); rec.Code != http.StatusNotFound {
		t.Errorf("carol /t/1 = %d, want 404", rec.Code)
	}
	if rec := getAs(t, h, "/f/2", carol); rec.Code != http.StatusNotFound {
		t.Errorf("carol /f/2 = %d, want 404", rec.Code)
	}
}

func TestMemberTimeAndScheme(t *testing.T) {
	h, _ := smallSite(t)
	bob := signIn(t, h, "bob", testPassword)
	rec := getAs(t, h, "/t/1", bob)
	body := bodyOf(t, rec)
	// Post 1 is 09:00 UTC on May 2, 2019: 10:00 in London.
	if !strings.Contains(body, `<time datetime="2019-05-02T09:00:00Z">May 2, 2019, 10:00 am</time>`) {
		t.Errorf("bob does not see post 1 at 10:00 am London time")
	}
	if !strings.Contains(body, `data-scheme="slate-deep"`) {
		t.Error("bob's page is not in slate-deep")
	}
	if !strings.Contains(body, "Times are shown in Europe/London.") {
		t.Error("the footer does not name bob's zone")
	}
	// The guest sees New York time and the board scheme.
	guest := bodyOf(t, get(t, h, "/t/1"))
	if !strings.Contains(guest, "May 2, 2019, 5:00 am") || !strings.Contains(guest, `data-scheme="slate"`) {
		t.Error("the guest does not see the board time and scheme")
	}
	// ?scheme= still wins for one page.
	if body := bodyOf(t, getAs(t, h, "/t/1?scheme=slate-light", bob)); !strings.Contains(body, `data-scheme="slate-light"`) {
		t.Error("?scheme= does not override a member's scheme")
	}
}

func TestViewIP(t *testing.T) {
	b := bundletest.Small()
	group := int64(3)
	yes := bundle.Yes
	opt := "mod.view_ip"
	b.Grants = append(b.Grants, bundle.Grant{GroupID: &group, ForumID: 0, Option: &opt, Setting: &yes})
	h, _ := newTestSite(t, b, Options{})
	bob := signIn(t, h, "bob", testPassword)
	carol := signIn(t, h, "carol", testPassword)
	if body := bodyOf(t, getAs(t, h, "/t/1", bob)); !strings.Contains(body, "10.0.0.5") {
		t.Error("the moderator does not see the IP address of post 1")
	}
	for name, c := range map[string][]*http.Cookie{"guest": nil, "carol": carol} {
		if body := bodyOf(t, getAs(t, h, "/t/1", c)); strings.Contains(body, "10.0.0.5") {
			t.Errorf("%s sees an IP address", name)
		}
	}
}

func TestSchemePicker(t *testing.T) {
	h, pool := siteAndPool(t, bundletest.Small(), Options{})

	// The footer holds the picker on every page, and marks the current scheme.
	body := bodyOf(t, get(t, h, "/t/1"))
	for _, want := range []string{`action="/scheme"`, `name="scheme" value="slate"`, `name="scheme" value="slate-deep"`, `name="scheme" value="slate-light"`, `aria-pressed="true"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the footer lacks %s", want)
		}
	}
	if !strings.Contains(body, `name="next" value="/t/1"`) {
		t.Error("the picker does not return to the page")
	}

	// A guest: a cookie for a year, and the next page uses it.
	rec, cookies := postAs(t, h, "/scheme", nil, url.Values{"scheme": {"slate-light"}, "next": {"/t/1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/t/1" {
		t.Fatalf("guest POST /scheme = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	raw := rawSetCookie(rec, "gobb_scheme")
	for _, want := range []string{"gobb_scheme=slate-light", "Max-Age=31536000", "SameSite=Lax", "Path=/"} {
		if !strings.Contains(raw, want) {
			t.Errorf("gobb_scheme cookie %q lacks %s", raw, want)
		}
	}
	page := bodyOf(t, getAs(t, h, "/", cookies))
	if !strings.Contains(page, `data-scheme="slate-light"`) {
		t.Error("the guest's next page ignores the cookie")
	}
	if !strings.Contains(page, `value="slate-light" aria-pressed="true"`) {
		t.Error("the picker does not mark the guest's scheme")
	}
	// ?scheme= beats the cookie for one page.
	if page := bodyOf(t, getAs(t, h, "/?scheme=slate-deep", cookies)); !strings.Contains(page, `data-scheme="slate-deep"`) {
		t.Error("?scheme= does not beat the cookie")
	}
	// The return path of the picker drops the preview.
	if page := bodyOf(t, getAs(t, h, "/t/1?scheme=slate-deep&page=1", cookies)); strings.Contains(page, "scheme=slate-deep&amp;") || strings.Contains(page, `name="next" value="/t/1?scheme`) {
		t.Error("the picker keeps the ?scheme= preview in next")
	}
	// A bad cookie falls back to the board scheme.
	bad := []*http.Cookie{{Name: "gobb_scheme", Value: "neon"}}
	if page := bodyOf(t, getAs(t, h, "/", bad)); !strings.Contains(page, `data-scheme="slate"`) {
		t.Error("a bad cookie changed the scheme")
	}
	// A bad value, and an unsafe next.
	if rec, _ := postAs(t, h, "/scheme", nil, url.Values{"scheme": {"neon"}}); rec.Code != http.StatusBadRequest {
		t.Errorf("bad scheme = %d, want 400", rec.Code)
	}
	if rec, _ := postAs(t, h, "/scheme", nil, url.Values{"scheme": {"slate"}, "next": {"//evil.example"}}); rec.Header().Get("Location") != "/" {
		t.Errorf("unsafe next went to %q", rec.Header().Get("Location"))
	}

	// A member: the choice goes into the row, and no cookie is set.
	bob := signIn(t, h, "bob", testPassword)
	rec, bob = postAs(t, h, "/scheme", bob, url.Values{"scheme": {"slate-light"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("member POST /scheme = %d", rec.Code)
	}
	if cookieNamed(rec, "gobb_scheme") != nil {
		t.Error("a member's choice set a cookie")
	}
	var scheme string
	if err := pool.QueryRow(t.Context(), `select color_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "slate-light" {
		t.Errorf("users.color_scheme = %q (%v), want slate-light", scheme, err)
	}
	if page := bodyOf(t, getAs(t, h, "/", bob)); !strings.Contains(page, `data-scheme="slate-light"`) {
		t.Error("bob's next page is not in the scheme he chose")
	}
	// A member's cookie of the old guest days does not beat the row.
	both := append(append([]*http.Cookie(nil), bob...), &http.Cookie{Name: "gobb_scheme", Value: "slate-deep"})
	if page := bodyOf(t, getAs(t, h, "/", both)); !strings.Contains(page, `data-scheme="slate-light"`) {
		t.Error("the guest cookie beats a member's choice")
	}
}

// A forum the viewer may list but not read has a lock. A guest is told that the
// forum is for members; a member is told there is no access.
func TestLockLabel(t *testing.T) {
	b := bundletest.Small()
	b.Forums = append(b.Forums, bundleForum(4, 0, "forum", "Staff Room", 5))
	b.Grants = append(b.Grants, guestGrant(4, "forum.list"))
	group := int64(2)
	list := "forum.list"
	yes := bundle.Yes
	b.Grants = append(b.Grants, bundle.Grant{GroupID: &group, ForumID: 4, Option: &list, Setting: &yes})
	h, _ := newTestSite(t, b, Options{})

	row := rowOf(bodyOf(t, get(t, h, "/")), "forum-row", "Staff Room")
	if !strings.Contains(row, "Members only") || strings.Contains(row, "No access") {
		t.Errorf("guest row:\n%s", row)
	}
	bob := signIn(t, h, "bob", testPassword)
	row = rowOf(bodyOf(t, getAs(t, h, "/", bob)), "forum-row", "Staff Room")
	if !strings.Contains(row, "No access") || strings.Contains(row, "Members only") {
		t.Errorf("member row:\n%s", row)
	}
	if strings.Contains(row, `href="/f/4"`) || strings.Contains(row, "forum-count") {
		t.Errorf("member row links or counts the forum:\n%s", row)
	}
}

// account.download decides whether a member sees an image of an attachment.
func TestMemberImageNote(t *testing.T) {
	b := bundletest.Small()
	user := int64(3)
	yes := bundle.Yes
	opt := "account.download"
	b.Grants = append(b.Grants, bundle.Grant{UserID: &user, ForumID: 0, Option: &opt, Setting: &yes})
	h, _ := newTestSite(t, b, Options{})
	bob := signIn(t, h, "bob", testPassword)
	carol := signIn(t, h, "carol", testPassword)

	// Post 2 shows attachment 1 inline.
	withRight := bodyOf(t, getAs(t, h, "/t/1", carol))
	if !strings.Contains(withRight, `src="/a/1"`) || strings.Contains(withRight, "img-note") {
		t.Errorf("carol, who may download, does not see the image:\n%s", withRight)
	}
	without := bodyOf(t, getAs(t, h, "/t/1", bob))
	if strings.Contains(without, `src="/a/1"`) || !strings.Contains(without, "not available to you") {
		t.Errorf("bob, who may not download, sees the image or lacks the note")
	}
	if guest := bodyOf(t, get(t, h, "/t/1")); !strings.Contains(guest, "sign in to view") {
		t.Error("the guest lacks the sign-in note")
	}
	// The file itself follows the same right.
	if rec := getAs(t, h, "/a/1", carol); rec.Code != http.StatusOK {
		t.Errorf("carol /a/1 = %d, want 200", rec.Code)
	}
	if rec := getAs(t, h, "/a/1", bob); rec.Code != http.StatusForbidden {
		t.Errorf("bob /a/1 = %d, want 403", rec.Code)
	}
}

// The cookies of a site under a base path stay under it.
func TestBasePathCookies(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Small(), Options{BasePath: "/forum/", CookieSecure: true})
	rec, cookies := postAsAt(t, h, "/forum/", "/forum/login", nil, url.Values{"name": {"alice"}, "password": {testPassword}, "next": {"/forum/t/1"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/t/1" {
		t.Fatalf("sign-in = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	for _, line := range rec.Header().Values("Set-Cookie") {
		if !strings.Contains(line, "Path=/forum/") {
			t.Errorf("Set-Cookie %q lacks Path=/forum/", line)
		}
	}
	body := bodyOf(t, getAs(t, h, "/forum/", cookies))
	for _, want := range []string{`action="/forum/logout"`, `action="/forum/scheme"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %s", want)
		}
	}
	checkBasePath(t, "/forum/ as alice", body, "/forum/")
	rec, _ = postAsAt(t, h, "/forum/", "/forum/logout", cookies, url.Values{})
	if rec.Header().Get("Location") != "/forum/" {
		t.Errorf("logout goes to %q", rec.Header().Get("Location"))
	}
	if c := cookieNamed(rec, "gobb_session"); c == nil || c.Path != "/forum/" {
		t.Errorf("the expired cookie = %+v", c)
	}
}
