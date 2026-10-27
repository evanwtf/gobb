package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// patternBraces matches a {name} part of a route pattern, but not {$}.
var patternBraces = regexp.MustCompile(`\{[a-z]+\}`)

// requestWith returns a GET of the index with cookies, for a call that needs a
// request and no handler.
func requestWith(cookies []*http.Cookie) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/", nil)
	for _, c := range cookies {
		r.AddCookie(c)
	}
	return r
}

// adminPatterns asks a running site for the patterns of the admin area, with the
// method, and returns each one as an address: a {name} part becomes 1.
func adminPatterns(t *testing.T) (get, post []string) {
	t.Helper()
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	newTestSite(t, bundletest.Small(), Options{})
	onNew = nil
	for _, p := range s.patterns {
		method, rest, found := strings.Cut(p, " ")
		if !found {
			method, rest = "", p
		}
		if !strings.HasPrefix(rest, "/admin") {
			continue
		}
		target := strings.TrimSuffix(patternBraces.ReplaceAllString(rest, "1"), "{$}")
		switch method {
		case "GET":
			get = append(get, target)
		case "POST":
			post = append(post, target)
		default:
			get, post = append(get, target), append(post, target)
		}
	}
	if len(get) < 3 || len(post) < 1 {
		t.Fatalf("found too few admin routes: GET %v, POST %v", get, post)
	}
	return get, post
}

// TestAdminGateHidesTheArea walks the routes of the admin area: a guest, a
// member and a moderator get the 404 page of any missing path on every one, GET
// and POST, and a POST changes nothing. A new admin route is in the walk by
// itself, because the walk reads the patterns of the running site.
func TestAdminGateHidesTheArea(t *testing.T) {
	gets, posts := adminPatterns(t)
	gets = append(gets, "/admin", "/admin/no-such-page")
	posts = append(posts, "/admin", "/admin/no-such-page")
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	missing := bodyOf(t, get(t, h, "/no-such-page"))
	if !strings.Contains(missing, "Page not found") {
		t.Fatalf("the 404 page lacks its heading:\n%.300s", missing)
	}
	visitors := map[string][]*http.Cookie{"": nil}
	for _, who := range []string{"carol", "bob"} {
		visitors[who] = signIn(t, h, who, testPassword)
	}
	// The snapshot comes after the sign-ins, which write the users.
	before := dumpData(t, pool)
	for who, cookies := range visitors {
		for _, target := range gets {
			rec := getAs(t, h, target, cookies)
			if body := bodyOf(t, rec); rec.Code != http.StatusNotFound || !strings.Contains(body, "Page not found") {
				t.Errorf("%q: GET %s = %d, want the 404 page", who, target, rec.Code)
			}
		}
		for _, target := range posts {
			rec, _ := postAs(t, h, target, cookies, url.Values{"password": {testPassword}, "next": {"/admin/"}})
			if body := bodyOf(t, rec); rec.Code != http.StatusNotFound || !strings.Contains(body, "Page not found") {
				t.Errorf("%q: POST %s = %d, want the 404 page", who, target, rec.Code)
			}
		}
		// Only an admin has the link in the nav.
		if strings.Contains(bodyOf(t, getAs(t, h, "/", cookies)), `href="/admin/"`) {
			t.Errorf("%q sees the admin link", who)
		}
	}
	if diff := diffTables(before, dumpData(t, pool)); len(diff) != 0 {
		t.Errorf("a refused request changed %v", diff)
	}
}

func TestAdminIndexAndLog(t *testing.T) {
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	// A command writes a row with no actor.
	if err := board.ActivateUser(t.Context(), pool, 4, time.Now()); err != nil {
		t.Fatal(err)
	}
	alice := signIn(t, h, "alice", testPassword)

	index := getAs(t, h, "/admin/", alice)
	body := bodyOf(t, index)
	if index.Code != http.StatusOK || !strings.Contains(body, `href="/admin/log"`) {
		t.Fatalf("index = %d\n%s", index.Code, body)
	}
	if !strings.Contains(body, `<a href="/admin/" aria-current="page">Admin</a>`) {
		t.Error("the nav does not mark the admin link")
	}
	for _, target := range []string{"/admin/", "/admin/log", "/admin/confirm"} {
		rec := getAs(t, h, target, alice)
		if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
			t.Errorf("%s: Cache-Control %q", target, cc)
		}
		if !strings.Contains(bodyOf(t, rec), `<meta name="robots" content="noindex">`) {
			t.Errorf("%s has no robots meta", target)
		}
	}
	if rec := getAs(t, h, "/admin", alice); rec.Code != http.StatusMovedPermanently || rec.Header().Get("Location") != "/admin/" {
		t.Errorf("/admin = %d to %q", rec.Code, rec.Header().Get("Location"))
	}

	log := bodyOf(t, getAs(t, h, "/admin/log", alice))
	if !strings.Contains(log, "Command line") || !strings.Contains(log, "<code>user.activate</code>") || !strings.Contains(log, `<a href="/u/4">user 4</a> <bdi>dave</bdi>`) {
		t.Errorf("the log lacks the command's row:\n%s", log)
	}
	// The filters: one that matches, one that matches nothing, a name that no
	// member has, and a bad target.
	for _, c := range []struct {
		query, want string
	}{
		{"?action=user.activate", "<code>user.activate</code>"},
		{"?noactor=1", "Command line"},
		{"?type=user&target=4", "<code>user.activate</code>"},
		{"?actor=alice", "No action matches."},
		{"?actor=nobody-here", "No member has that name."},
		{"?type=user&target=x", "The target ID must be a whole number above 0."},
	} {
		rec := getAs(t, h, "/admin/log"+c.query, alice)
		if b := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(b, c.want) {
			t.Errorf("%s = %d, lacks %q", c.query, rec.Code, c.want)
		}
	}
	if rec := getAs(t, h, "/admin/log?page=9", alice); rec.Code != http.StatusNotFound {
		t.Errorf("page 9 = %d, want 404", rec.Code)
	}
	if rec := getAs(t, h, "/admin/log?page=x", alice); rec.Code != http.StatusBadRequest {
		t.Errorf("page x = %d, want 400", rec.Code)
	}
}

func TestAdminConfirm(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	onNew = nil
	alice := signIn(t, h, "alice", testPassword)
	fresh := func() bool {
		r := requestWith(alice)
		id, err := s.identify(nil, r)
		if err != nil {
			t.Fatal(err)
		}
		return s.passFresh(r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}
	if got := s.confirmHref("/admin/log?action=x"); got != "/admin/confirm?next=%2Fadmin%2Flog%3Faction%3Dx" {
		t.Errorf("confirmHref = %q", got)
	}

	page := bodyOf(t, getAs(t, h, "/admin/confirm?next=%2Fadmin%2Flog", alice))
	if !strings.Contains(page, `name="next" value="/admin/log"`) {
		t.Errorf("the form does not carry next:\n%s", page)
	}
	rec, _ := postAs(t, h, "/admin/confirm", alice, url.Values{"password": {"not the password"}, "next": {"/admin/log"}})
	if b := bodyOf(t, rec); rec.Code != http.StatusBadRequest || !strings.Contains(b, msgWrongCurrent) || !strings.Contains(b, `aria-invalid="true"`) {
		t.Errorf("a wrong password = %d", rec.Code)
	}
	if fresh() {
		t.Fatal("a wrong password gave a pass")
	}
	rec, _ = postAs(t, h, "/admin/confirm", alice, url.Values{"password": {testPassword}, "next": {"/admin/log?action=x"}})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/admin/log?action=x" {
		t.Errorf("the right password = %d to %q", rec.Code, rec.Header().Get("Location"))
	}
	if !fresh() {
		t.Error("the right password gave no pass")
	}
	// The pass ends after 10 minutes.
	s.opt.Now = func() time.Time { return time.Now().Add(passFor + time.Second) }
	if fresh() {
		t.Error("the pass outlived 10 minutes")
	}
}

func TestAdminNext(t *testing.T) {
	s := &site{base: "/forum/"}
	for next, want := range map[string]string{
		"/forum/admin/log":                 "/forum/admin/log",
		"/forum/admin/log?action=x":        "/forum/admin/log?action=x",
		"":                                 "/forum/admin/",
		"/forum/t/1":                       "/forum/admin/",
		"/admin/log":                       "/forum/admin/",
		"https://example.com/forum/admin/": "/forum/admin/",
		"//example.com/forum/admin/":       "/forum/admin/",
		"/forum/admin/../t/1":              "/forum/admin/",
		"/forum/admin/\\x":                 "/forum/admin/",
	} {
		if got := s.adminNext(next); got != want {
			t.Errorf("adminNext(%q) = %q, want %q", next, got, want)
		}
	}
}

func TestAdminWriteLimit(t *testing.T) {
	h, _ := siteWithPool(t, bundletest.Small(), Options{AdminWrites: 2})
	alice := signIn(t, h, "alice", testPassword)
	form := url.Values{"password": {testPassword}, "next": {"/admin/"}}
	for i := range 2 {
		if rec, _ := postAs(t, h, "/admin/confirm", alice, form); rec.Code != http.StatusSeeOther {
			t.Fatalf("write %d = %d", i+1, rec.Code)
		}
	}
	rec, _ := postAs(t, h, "/admin/confirm", alice, form)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") == "" {
		t.Errorf("write 3 = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestAdminUnderBasePath(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Small(), Options{BasePath: "/forum/"})
	alice := signInAt(t, h, "/forum/", "alice")
	body := bodyOf(t, getAs(t, h, "/forum/", alice))
	if !strings.Contains(body, `href="/forum/admin/"`) {
		t.Error("the nav has no admin link under the base path")
	}
	rec := getAs(t, h, "/forum/admin/", alice)
	if b := bodyOf(t, rec); rec.Code != http.StatusOK || !strings.Contains(b, `href="/forum/admin/log"`) {
		t.Errorf("index under the base path = %d", rec.Code)
	}
	if rec := get(t, h, "/forum/admin/"); rec.Code != http.StatusNotFound {
		t.Errorf("a guest under the base path = %d", rec.Code)
	}
}
