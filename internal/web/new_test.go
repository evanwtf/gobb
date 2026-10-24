package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// siteWithPool is newTestSite for a test that also reads the database.
func siteWithPool(t *testing.T, b *bundletest.Board, opt Options) (http.Handler, *pgxpool.Pool) {
	t.Helper()
	pool, dataDir := boardtest.Load(t, b)
	opt.DataDir = dataDir
	if opt.LoginFloor == 0 {
		opt.LoginFloor = time.Millisecond
	}
	h, err := New(t.Context(), pool, opt)
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return h, pool
}

// getWith sends a GET with cookies and extra headers.
func getWith(t *testing.T, h http.Handler, target string, cookies []*http.Cookie, hdr map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	for k, v := range hdr {
		r.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

// asPage and asImage are the headers of a top-level navigation and of an image
// load.
var (
	asPage  = map[string]string{"Sec-Fetch-Dest": "document"}
	asImage = map[string]string{"Sec-Fetch-Dest": "image"}
)

// pagedBoard is the small board with topic 7 in forum 2: 30 posts, one an hour
// from 2021-04-01 00:00 UTC, with IDs 100 to 129. Carol read the topic up to
// post 25 (ID 124).
func pagedBoard() *bundletest.Board {
	b := bundletest.Small()
	b.Topics = append(b.Topics, bundleTopic(7, 2, "A long topic", "normal", at(2021, 4, 1, 0, 0)))
	for i := range 30 {
		b.Posts = append(b.Posts, bundlePost(int64(100+i), 7, 1, "alice", at(2021, 4, 1, i, 0), "Post number."))
	}
	b.TopicMarks = append(b.TopicMarks, bundle.TopicMark{UserID: 3, TopicID: 7, MarkedAt: at(2021, 4, 2, 0, 0)})
	return b
}

func TestIsNavigation(t *testing.T) {
	for _, c := range []struct {
		name   string
		method string
		hdr    map[string]string
		want   bool
	}{
		{"document", "GET", map[string]string{"Sec-Fetch-Dest": "document"}, true},
		{"image", "GET", map[string]string{"Sec-Fetch-Dest": "image"}, false},
		{"empty", "GET", map[string]string{"Sec-Fetch-Dest": "empty"}, false},
		{"iframe", "GET", map[string]string{"Sec-Fetch-Dest": "iframe"}, false},
		{"image beats accept", "GET", map[string]string{"Sec-Fetch-Dest": "image", "Accept": "text/html"}, false},
		{"no header, html", "GET", map[string]string{"Accept": "text/html,application/xhtml+xml"}, true},
		{"no header, no accept", "GET", nil, false},
		{"no header, image accept", "GET", map[string]string{"Accept": "image/avif,image/*"}, false},
		{"head", "HEAD", map[string]string{"Sec-Fetch-Dest": "document"}, false},
		{"prefetch", "GET", map[string]string{"Sec-Fetch-Dest": "document", "Sec-Purpose": "prefetch"}, false},
	} {
		r := httptest.NewRequest(c.method, "/t/1", nil)
		for k, v := range c.hdr {
			r.Header.Set(k, v)
		}
		if got := isNavigation(r); got != c.want {
			t.Errorf("%s: isNavigation = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestTopicViewMarks(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()
	cookies := signIn(t, h, "carol", "example-password")

	var n int
	views := func() int {
		t.Helper()
		if err := pool.QueryRow(ctx, `select view_count from topics where id = 1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	marks := func() int {
		t.Helper()
		if err := pool.QueryRow(ctx, `select count(*) from topic_marks where user_id = 3`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	// An image load of the topic writes nothing.
	if rec := getWith(t, h, "/t/1", cookies, asImage); rec.Code != http.StatusOK {
		t.Fatalf("image GET /t/1: %d", rec.Code)
	}
	if views() != 42 || marks() != 0 {
		t.Errorf("after an image load: %d views, %d marks; want 42 and 0", views(), marks())
	}

	// A top-level navigation counts a view and marks the page read.
	rec := getWith(t, h, "/t/1", cookies, asPage)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /t/1: %d", rec.Code)
	}
	if views() != 43 {
		t.Errorf("view_count = %d, want 43", views())
	}
	var at time.Time
	if err := pool.QueryRow(ctx, `select marked_at from topic_marks where user_id = 3 and topic_id = 1`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(time.Date(2020, 2, 1, 11, 0, 0, 0, time.UTC)) {
		t.Errorf("marked_at = %v, want 2020-02-01 11:00 UTC", at)
	}
	// The page shows the view it just counted.
	if !strings.Contains(bodyOf(t, rec), "43 views") {
		t.Error("the page does not show 43 views")
	}

	// /t/1/new goes to the first unread post; after the view there is none newer,
	// so it goes to the last post.
	rec = getWith(t, h, "/t/1/new", cookies, asPage)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/t/1#p4" {
		t.Errorf("/t/1/new = %d %q, want 302 /t/1#p4", rec.Code, rec.Header().Get("Location"))
	}
	if views() != 43 {
		t.Errorf("/t/1/new changed view_count to %d", views())
	}

	// A guest's navigation counts a view and writes no mark.
	getWith(t, h, "/t/1", nil, asPage)
	if views() != 44 {
		t.Errorf("guest view: view_count = %d, want 44", views())
	}
	if err := pool.QueryRow(ctx, `select count(*) from topic_marks`).Scan(&n); err != nil || n != 2 {
		t.Errorf("topic_marks rows = %d, %v; want 2 (bob and carol)", n, err)
	}
}

func TestFirstUnreadRedirect(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	// Carol's first unread post is her own post 3, which waits for approval.
	rec := getAs(t, h, "/t/1/new", carol)
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/t/1#p3" {
		t.Errorf("/t/1/new = %d %q, want 302 /t/1#p3", rec.Code, rec.Header().Get("Location"))
	}
	// A guest gets the first post. A hidden topic and a missing one look alike.
	rec = get(t, h, "/t/1/new")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/t/1#p1" {
		t.Errorf("guest /t/1/new = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	hidden := get(t, h, "/t/2/new")
	missing := get(t, h, "/t/99/new")
	if hidden.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("hidden %d, missing %d; want 404", hidden.Code, missing.Code)
	}
	if shape(bodyOf(t, hidden), "/t/2/new") != shape(bodyOf(t, missing), "/t/99/new") {
		t.Error("the 404 of a hidden topic differs from the 404 of a missing one")
	}
	if rec := get(t, h, "/t/abc/new"); rec.Code != http.StatusNotFound {
		t.Errorf("/t/abc/new = %d, want 404", rec.Code)
	}
}

func TestMarkReadOnPage(t *testing.T) {
	h, pool := siteWithPool(t, pagedBoard(), Options{})
	ctx := context.Background()
	carol := signIn(t, h, "carol", "example-password")

	// The first unread post is the 26th, which is on page 2.
	rec := getAs(t, h, "/t/7/new", carol)
	if rec.Header().Get("Location") != "/t/7?page=2#p125" {
		t.Errorf("/t/7/new = %d %q, want /t/7?page=2#p125", rec.Code, rec.Header().Get("Location"))
	}

	// A page that shows only old posts does not mark the newer posts read.
	var at time.Time
	if _, err := pool.Exec(ctx, `delete from topic_marks where user_id = 3 and topic_id = 7`); err != nil {
		t.Fatal(err)
	}
	getWith(t, h, "/t/7", carol, asPage)
	if err := pool.QueryRow(ctx, `select marked_at from topic_marks where user_id = 3 and topic_id = 7`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	// Post 25 (index 24) is at 2021-04-02 00:00 UTC, the last post of page 1.
	if !at.Equal(time.Date(2021, 4, 2, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("page 1 marked up to %v, want 2021-04-02 00:00 UTC", at)
	}
}

func TestMarkReadForms(t *testing.T) {
	const badge = `badge-new`
	for _, c := range []struct{ name, target string }{
		{"forum", "/f/2/read"},
		{"all", "/read"},
	} {
		t.Run(c.name, func(t *testing.T) {
			h, _ := siteWithPool(t, bundletest.Small(), Options{})
			carol := signIn(t, h, "carol", "example-password")
			if !strings.Contains(bodyOf(t, getAs(t, h, "/", carol)), badge) {
				t.Fatal("the index shows no New badge before the mark")
			}
			if !strings.Contains(bodyOf(t, getAs(t, h, "/f/2", carol)), badge) {
				t.Fatal("the forum page shows no New badge before the mark")
			}

			// Without the csrf token: 403, and nothing changes.
			rec := request(t, h, http.MethodPost, c.target, carol, url.Values{"next": {"/"}})
			if rec.Code != http.StatusForbidden {
				t.Errorf("POST %s without csrf = %d, want 403", c.target, rec.Code)
			}
			if !strings.Contains(bodyOf(t, getAs(t, h, "/", carol)), badge) {
				t.Error("a refused POST cleared the badges")
			}

			rec, carol = postAs(t, h, c.target, carol, url.Values{"next": {"/f/2"}})
			if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/f/2" {
				t.Errorf("POST %s = %d %q, want 303 /f/2", c.target, rec.Code, rec.Header().Get("Location"))
			}
			for _, p := range []string{"/", "/f/2"} {
				if strings.Contains(bodyOf(t, getAs(t, h, p, carol)), badge) {
					t.Errorf("%s still shows a New badge after %s", p, c.target)
				}
			}
		})
	}
}

func TestMarkReadRefusals(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	// A forum that carol cannot read is a missing forum.
	rec, _ := postAs(t, h, "/f/3/read", carol, url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /f/3/read = %d, want 404", rec.Code)
	}
	rec, _ = postAs(t, h, "/f/99/read", carol, url.Values{})
	if rec.Code != http.StatusNotFound {
		t.Errorf("POST /f/99/read = %d, want 404", rec.Code)
	}
	// A next outside the site goes to the index.
	rec, _ = postAs(t, h, "/read", carol, url.Values{"next": {"https://example.com/x"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/" {
		t.Errorf("POST /read with a foreign next = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// A guest is sent to sign in.
	rec, _ = postAs(t, h, "/read", nil, url.Values{})
	if rec.Code != http.StatusSeeOther || !strings.HasPrefix(rec.Header().Get("Location"), "/login") {
		t.Errorf("guest POST /read = %d %q, want 303 to /login", rec.Code, rec.Header().Get("Location"))
	}
	// GET is not allowed.
	if rec := getAs(t, h, "/read", carol); rec.Code != http.StatusMethodNotAllowed && rec.Code != http.StatusNotFound {
		t.Errorf("GET /read = %d", rec.Code)
	}
}

func TestNewPage(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()

	rec := get(t, h, "/new")
	if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login?next=%2Fnew" {
		t.Errorf("guest /new = %d %q, want 302 /login?next=%%2Fnew", rec.Code, rec.Header().Get("Location"))
	}

	if _, err := pool.Exec(ctx, `update users set last_visit_at = '2019-06-01T00:00:00Z' where id in (1, 2, 3)`); err != nil {
		t.Fatal(err)
	}
	// Carol cannot read forum 3, so topic 2 is not in her list.
	carol := signIn(t, h, "carol", "example-password")
	rec = getAs(t, h, "/new", carol)
	if rec.Code != http.StatusOK {
		t.Fatalf("carol /new = %d", rec.Code)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{"Welcome to the example board", "Main Discussion", `href="/f/2"`, "New since your last visit (1)", "badge-new", `href="/t/1/new"`, `action="/read"`} {
		if !strings.Contains(body, want) {
			t.Errorf("carol /new lacks %q", want)
		}
	}
	if strings.Contains(body, "Favorite color?") || strings.Contains(body, "Members Lounge") {
		t.Error("carol /new lists a topic of a forum she cannot read")
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/", carol)), "New since your last visit (1)") {
		t.Error("the nav of the index lacks the count")
	}
	if strings.Contains(bodyOf(t, get(t, h, "/")), "New since your last visit") {
		t.Error("a guest sees the link")
	}

	// Bob reads forum 3 too and marked everything read: both topics are listed,
	// and neither has a badge or a jump link.
	bob := signIn(t, h, "bob", "example-password")
	body = bodyOf(t, getAs(t, h, "/new", bob))
	for _, want := range []string{"Welcome to the example board", "Favorite color?", "Members Lounge"} {
		if !strings.Contains(body, want) {
			t.Errorf("bob /new lacks %q", want)
		}
	}
	// The nav counts unread topics, and bob has read both: the item is hidden.
	if strings.Contains(body, `href="/new"`) {
		t.Error("bob has no unread topic, and the nav still links /new")
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/", bob)), `href="/new"`) {
		t.Error("bob: the nav of the index links /new at 0")
	}
	if strings.Contains(body, "badge-new") || strings.Contains(body, "/t/1/new") {
		t.Error("bob /new marks a read topic as new")
	}

	// Alice reads both forums and has both topics unread.
	alice := signIn(t, h, "alice", "example-password")
	body = bodyOf(t, getAs(t, h, "/new", alice))
	for _, want := range []string{"Favorite color?", "Welcome to the example board", "Members Lounge", "/t/2/new", "/t/1/new", "New since your last visit (2)"} {
		if !strings.Contains(body, want) {
			t.Errorf("alice /new lacks %q", want)
		}
	}
	// A page past the end, and a bad page number.
	if rec := getAs(t, h, "/new?page=2", alice); rec.Code != http.StatusNotFound {
		t.Errorf("/new?page=2 = %d, want 404", rec.Code)
	}
	if rec := getAs(t, h, "/new?page=abc", alice); rec.Code != http.StatusBadRequest {
		t.Errorf("/new?page=abc = %d, want 400", rec.Code)
	}
}

func TestBadgesAndJumpLinks(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	bob := signIn(t, h, "bob", "example-password")

	idx := bodyOf(t, getAs(t, h, "/", carol))
	row := rowOf(idx, "forum-row", "Main Discussion")
	if !strings.Contains(row, "badge-new") || !strings.HasPrefix(row, ` row-new"`) {
		t.Errorf("carol: the Main Discussion row has no badge:\n%s", row)
	}
	if strings.Contains(bodyOf(t, getAs(t, h, "/", bob)), "badge-new") {
		t.Error("bob: the index shows a badge")
	}
	if strings.Contains(bodyOf(t, get(t, h, "/")), "badge-new") {
		t.Error("a guest sees a badge")
	}

	fp := bodyOf(t, getAs(t, h, "/f/2", carol))
	trow := rowOf(fp, "topic-row", "Welcome to the example board")
	if !strings.Contains(trow, "badge-new") || !strings.Contains(trow, `href="/t/1/new"`) {
		t.Errorf("carol: topic row lacks the badge or the jump link:\n%s", trow)
	}
	// The forum page and the index hold the mark forms of a member.
	if !strings.Contains(fp, `action="/f/2/read"`) || !strings.Contains(idx, `action="/read"`) {
		t.Error("a member's pages lack the mark-read forms")
	}
	for _, p := range []string{"/", "/f/2"} {
		body := bodyOf(t, get(t, h, p))
		if strings.Contains(body, `/read"`) {
			t.Errorf("a guest's %s holds a mark-read form", p)
		}
	}
	bp := bodyOf(t, getAs(t, h, "/f/2", bob))
	if strings.Contains(rowOf(bp, "topic-row", "Welcome"), "/t/1/new") {
		t.Error("bob: a read topic has a jump link")
	}
}

func TestVisitOnNavigationOnly(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()
	carol := signIn(t, h, "carol", "example-password")
	active := func() *time.Time {
		t.Helper()
		var a *time.Time
		if err := pool.QueryRow(ctx, `select last_active_at from users where id = 3`).Scan(&a); err != nil {
			t.Fatal(err)
		}
		return a
	}
	// The sign-in itself starts a visit.
	if active() == nil {
		t.Fatal("sign-in did not set last_active_at")
	}
	if _, err := pool.Exec(ctx, `update users set last_active_at = null where id = 3`); err != nil {
		t.Fatal(err)
	}
	getWith(t, h, "/", carol, asImage)
	if active() != nil {
		t.Error("an image load set last_active_at")
	}
	getWith(t, h, "/", carol, asPage)
	if active() == nil {
		t.Error("a navigation did not set last_active_at")
	}
}

func TestVisitWindow(t *testing.T) {
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	h, pool := siteWithPool(t, bundletest.Small(), Options{Now: func() time.Time { return now }})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `update users set last_visit_at = '2026-10-01T00:00:00Z', last_active_at = '2026-10-23T08:00:00Z' where id = 2`); err != nil {
		t.Fatal(err)
	}
	// Sign-in starts a visit: it begins where the last one ended.
	bob := signIn(t, h, "bob", "example-password")
	var visit, active time.Time
	if err := pool.QueryRow(ctx, `select last_visit_at, last_active_at from users where id = 2`).Scan(&visit, &active); err != nil {
		t.Fatal(err)
	}
	if !visit.Equal(time.Date(2026, 10, 23, 8, 0, 0, 0, time.UTC)) || !active.Equal(now) {
		t.Errorf("after sign-in: visit %v, active %v", visit, active)
	}
	// A request 3 hours later is a new visit.
	now = now.Add(3 * time.Hour)
	getWith(t, h, "/", bob, asPage)
	if err := pool.QueryRow(ctx, `select last_visit_at, last_active_at from users where id = 2`).Scan(&visit, &active); err != nil {
		t.Fatal(err)
	}
	if !visit.Equal(time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)) || !active.Equal(now) {
		t.Errorf("after 3 hours: visit %v, active %v", visit, active)
	}
}

// The nav count is the number of unread topics. It falls as the member reads, and
// the item hides at 0.
func TestNavCountFalls(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `update users set last_visit_at = '2019-06-01T00:00:00Z', last_mark_at = null where id = 1`); err != nil {
		t.Fatal(err)
	}
	alice := signIn(t, h, "alice", "example-password")
	nav := func() string {
		t.Helper()
		body := bodyOf(t, getAs(t, h, "/", alice))
		_, rest, _ := strings.Cut(body, `aria-label="Main"`)
		nav, _, _ := strings.Cut(rest, "</nav>")
		return nav
	}
	if !strings.Contains(nav(), "New since your last visit (2)") {
		t.Fatalf("nav at start:\n%s", nav())
	}
	// Reading topic 1 leaves topic 2.
	getWith(t, h, "/t/1", alice, asPage)
	if !strings.Contains(nav(), "New since your last visit (1)") {
		t.Errorf("nav after one topic:\n%s", nav())
	}
	// The list still holds both topics, one of them read.
	body := bodyOf(t, getAs(t, h, "/new", alice))
	if !strings.Contains(body, "Welcome to the example board") || strings.Count(body, "badge-new") != 1 {
		t.Errorf("/new after reading topic 1 holds %d badges", strings.Count(body, "badge-new"))
	}
	getWith(t, h, "/t/2", alice, asPage)
	if strings.Contains(nav(), `href="/new"`) {
		t.Errorf("nav at 0 still links /new:\n%s", nav())
	}
	// The page itself still opens at 0.
	if rec := getAs(t, h, "/new", alice); rec.Code != http.StatusOK {
		t.Errorf("/new at 0 = %d", rec.Code)
	}
}

// The page that marks a topic read shows the count without that topic. Alice has
// two unread topics; the page of the first shows 1, and the page of the second,
// which leaves none, has no link to /new.
func TestNavCountOnTheMarkingPage(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	if _, err := pool.Exec(context.Background(), `update users set last_visit_at = '2019-06-01T00:00:00Z', last_mark_at = null where id = 1`); err != nil {
		t.Fatal(err)
	}
	alice := signIn(t, h, "alice", "example-password")
	navOf := func(body string) string {
		_, rest, _ := strings.Cut(body, `aria-label="Main"`)
		nav, _, _ := strings.Cut(rest, "</nav>")
		return nav
	}
	// An image load marks nothing, so its count stays.
	if nav := navOf(bodyOf(t, getWith(t, h, "/t/1", alice, asImage))); !strings.Contains(nav, "(2)") {
		t.Errorf("an image load changed the count:\n%s", nav)
	}
	if nav := navOf(bodyOf(t, getWith(t, h, "/t/1", alice, asPage))); !strings.Contains(nav, "New since your last visit (1)") {
		t.Errorf("the page of the first topic:\n%s", nav)
	}
	if nav := navOf(bodyOf(t, getWith(t, h, "/t/2", alice, asPage))); strings.Contains(nav, `href="/new"`) {
		t.Errorf("the page of the last unread topic still links /new:\n%s", nav)
	}
}

// A request of a member who is not active writes nothing to users.
func TestInactiveMemberWritesNothing(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()
	carol := signIn(t, h, "carol", "example-password")
	if _, err := pool.Exec(ctx, `update users set state = 'inactive', last_active_at = null where id = 3`); err != nil {
		t.Fatal(err)
	}
	row := func() string {
		t.Helper()
		var s string
		if err := pool.QueryRow(ctx, `select to_jsonb(u)::text from users u where id = 3`).Scan(&s); err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := row()
	for _, p := range []string{"/", "/t/1", "/new", "/f/2"} {
		getWith(t, h, p, carol, asPage)
	}
	if after := row(); after != before {
		t.Errorf("a request of an inactive member changed the row:\n%s\n%s", before, after)
	}
	// The same holds for the function: an inactive user is not recorded.
	if err := board.RecordVisit(ctx, pool, 3, time.Now()); err != nil {
		t.Fatal(err)
	}
	if after := row(); after != before {
		t.Errorf("RecordVisit wrote for an inactive member:\n%s\n%s", before, after)
	}
}

// The view count and the topic mark are written after the page renders. A page
// that fails writes neither.
func TestFailedPageWritesNoBookkeeping(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()
	carol := signIn(t, h, "carol", "example-password")
	if _, err := pool.Exec(ctx, `drop table smilies`); err != nil {
		t.Fatal(err)
	}
	rec := getWith(t, h, "/t/1", carol, asPage)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("GET /t/1 with no smilies table = %d, want 500", rec.Code)
	}
	var views, marks int
	if err := pool.QueryRow(ctx, `select view_count from topics where id = 1`).Scan(&views); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from topic_marks where user_id = 3`).Scan(&marks); err != nil {
		t.Fatal(err)
	}
	if views != 42 || marks != 0 {
		t.Errorf("after a 500: %d views, %d marks; want 42 and 0", views, marks)
	}
}

// /t/{marker}/new answers 301 to the target's /new, after the permission check of
// /t/{marker}.
func TestMarkerNewRedirect(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	ctx := context.Background()
	alice := signIn(t, h, "alice", "example-password")
	rec := getAs(t, h, "/t/3/new", alice)
	if rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/t/1/new" {
		t.Errorf("/t/3/new = %d %q, want 301 /t/1/new", rec.Code, rec.Header().Get("Location"))
	}
	// /t/3 answers the same way.
	if rec := getAs(t, h, "/t/3", alice); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/t/1" {
		t.Errorf("/t/3 = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	// A guest may not read the forum of the marker: 404, as for /t/3.
	if rec := get(t, h, "/t/3/new"); rec.Code != http.StatusNotFound {
		t.Errorf("guest /t/3/new = %d, want 404", rec.Code)
	}
	// A marker whose target the viewer may not see is a 404 too.
	if _, err := pool.Exec(ctx, `update topics set forum_id = 2, moved_to_topic_id = 2 where id = 3`); err != nil {
		t.Fatal(err)
	}
	carol := signIn(t, h, "carol", "example-password")
	if rec := getAs(t, h, "/t/3", carol); rec.Code != http.StatusNotFound {
		t.Fatalf("carol /t/3 = %d, want 404", rec.Code)
	}
	if rec := getAs(t, h, "/t/3/new", carol); rec.Code != http.StatusNotFound {
		t.Errorf("carol /t/3/new = %d, want 404", rec.Code)
	}
}

// The link of an unread topic and of an unread forum says "New" in its accessible
// name, and the visible pill is hidden from a screen reader so that it is not
// read twice.
func TestNewInAccessibleName(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	idx := bodyOf(t, getAs(t, h, "/", carol))
	row := rowOf(idx, "forum-row", "Main Discussion")
	if !strings.Contains(row, `<span class="visually-hidden">New: </span>Main Discussion</a>`) {
		t.Errorf("the forum link does not say New:\n%s", row)
	}
	fp := bodyOf(t, getAs(t, h, "/f/2", carol))
	trow := rowOf(fp, "topic-row", "Welcome to the example board")
	if !strings.Contains(trow, `<span class="visually-hidden">New: </span>Welcome to the example board</a>`) {
		t.Errorf("the topic link does not say New:\n%s", trow)
	}
	for _, r := range []string{row, trow} {
		if !strings.Contains(r, `<span class="badge-new" aria-hidden="true">New</span>`) {
			t.Errorf("the pill is not hidden from a screen reader:\n%s", r)
		}
	}
	// A read topic has neither.
	bob := signIn(t, h, "bob", "example-password")
	bp := bodyOf(t, getAs(t, h, "/f/2", bob))
	if strings.Contains(rowOf(bp, "topic-row", "Welcome"), "New: ") {
		t.Error("a read topic link says New")
	}
}
