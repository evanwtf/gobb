package web

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"slices"
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

// csrfValue matches the value of a csrf field, which differs on each answer to a
// guest with no cookie.
var csrfValue = regexp.MustCompile(`name="csrf" value="[^"]*"`)

// answerShape returns what a visitor can see of rec, with the path of the request
// replaced: the status, every header but Date (a Set-Cookie without its value),
// and the body without the csrf values. Two answers with the same shape differ
// only in the path that was asked for.
func answerShape(t *testing.T, rec *httptest.ResponseRecorder, path string) string {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "%d\n", rec.Code)
	h := rec.Header().Clone()
	h.Del("Date")
	var cookies []string
	for _, c := range rec.Result().Cookies() {
		c.Value = ""
		cookies = append(cookies, c.String())
	}
	h.Del("Set-Cookie")
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		fmt.Fprintf(&b, "%s: %s\n", k, strings.Join(h[k], ", "))
	}
	slices.Sort(cookies)
	fmt.Fprintf(&b, "cookies: %s\n", strings.Join(cookies, "; "))
	body := csrfValue.ReplaceAllString(bodyOf(t, rec), `name="csrf" value=""`)
	body = strings.ReplaceAll(body, url.QueryEscape(path), "PATH")
	body = strings.ReplaceAll(body, path, "PATH")
	return b.String() + body
}

// TestAdminGateHidesTheArea walks the routes of the admin area: a guest, a
// member, a moderator and a member whose session ended get, on every one, the
// same answer as a path that does not exist (status, headers and body), for GET,
// HEAD and POST, and a POST changes nothing. A new admin route is in the walk by
// itself, because the walk reads the patterns of the running site.
func TestAdminGateHidesTheArea(t *testing.T) {
	gets, posts := adminPatterns(t)
	gets = append(gets, "/admin", "/admin/no-such-page")
	posts = append(posts, "/admin", "/admin/no-such-page")
	h, pool := siteWithPool(t, bundletest.Small(), Options{})
	visitors := map[string][]*http.Cookie{"a guest": nil}
	for _, who := range []string{"carol", "bob", "dave-ended"} {
		name := strings.TrimSuffix(who, "-ended")
		if name == "dave" {
			// dave is inactive; carol's second session stands in for a member whose
			// session ended (a sign-out, a ban, a deactivation).
			name = "carol"
		}
		visitors[who] = signIn(t, h, name, testPassword)
		if strings.HasSuffix(who, "-ended") {
			exec(t, pool, `delete from sessions where user_id = 3 and id not in (select id from sessions where user_id = 3 order by created_at limit 1)`)
		}
	}
	// The snapshot comes after the sign-ins, which write the users.
	before := dumpData(t, pool)
	form := url.Values{"password": {testPassword}, "next": {"/admin/"}}
	for who, cookies := range visitors {
		for _, method := range []string{http.MethodGet, http.MethodHead} {
			for _, target := range gets {
				missing := "/nowhere" + target
				got := answerShape(t, request(t, h, method, target, cookies, nil), target)
				want := answerShape(t, request(t, h, method, missing, cookies, nil), missing)
				if got != want {
					t.Errorf("%s: %s %s differs from a missing path:\n%s\n---\n%s", who, method, target, got, want)
				}
			}
		}
		for _, target := range posts {
			missing := "/nowhere" + target
			rec, _ := postAs(t, h, target, cookies, form)
			ref, _ := postAs(t, h, missing, cookies, form)
			if got, want := answerShape(t, rec, target), answerShape(t, ref, missing); got != want || rec.Code != http.StatusNotFound {
				t.Errorf("%s: POST %s = %d, differs from a missing path:\n%s\n---\n%s", who, target, rec.Code, got, want)
			}
		}
		// Only an admin has the link in the nav.
		if strings.Contains(bodyOf(t, getAs(t, h, "/", cookies)), `href="/admin/"`) {
			t.Errorf("%s sees the admin link", who)
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
	// The pass belongs to the session that confirmed: a second session of alice
	// has none.
	if other := signIn(t, h, "alice", testPassword); func() bool {
		r := requestWith(other)
		id, err := s.identify(nil, r)
		if err != nil {
			t.Fatal(err)
		}
		return s.passFresh(r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
	}() {
		t.Error("a second session of alice holds the pass")
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

func TestAdminConfirmEndsWithTheSession(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	onNew = nil
	alice := signIn(t, h, "alice", testPassword)
	if rec, _ := postAs(t, h, "/admin/confirm", alice, url.Values{"password": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
	if rec, _ := postAs(t, h, "/logout", alice, url.Values{}); rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-out = %d", rec.Code)
	}
	r := requestWith(alice)
	id, err := s.identify(httptest.NewRecorder(), r)
	if err != nil {
		t.Fatal(err)
	}
	if s.passFresh(r.WithContext(context.WithValue(r.Context(), identityKey{}, id))) {
		t.Error("the pass outlived the session")
	}
}

// TestAdminDangerous: a dangerous write runs only with a pass. Without one, the
// admin goes to the confirm page, which sends them back to the admin page in
// "back".
func TestAdminDangerous(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _ := siteWithPool(t, bundletest.Small(), Options{})
	onNew = nil
	ran := 0
	a := adminRoutes{routes: newRoutes(), s: s}
	a.dangerous("/test", maxPostBody, func(w http.ResponseWriter, r *http.Request) {
		ran++
		w.WriteHeader(http.StatusNoContent)
	})
	gated := s.adminGate(a)
	alice := signIn(t, h, "alice", testPassword)
	send := func(back string) *httptest.ResponseRecorder {
		t.Helper()
		page := getAs(t, h, "/", alice)
		form := url.Values{"back": {back}, "csrf": {formToken(t, bodyOf(t, page))}}
		return request(t, gated, http.MethodPost, "/admin/test", jar(alice, page), form)
	}
	rec := send("/admin/log?action=x")
	if want := "/admin/confirm?next=%2Fadmin%2Flog%3Faction%3Dx"; rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != want || ran != 0 {
		t.Fatalf("without a pass = %d to %q, ran %d", rec.Code, rec.Header().Get("Location"), ran)
	}
	if rec := send("https://example.com/"); rec.Header().Get("Location") != "/admin/confirm?next=%2Fadmin%2F" {
		t.Errorf("a foreign back = %q", rec.Header().Get("Location"))
	}
	if rec, _ := postAs(t, h, "/admin/confirm", alice, url.Values{"password": {testPassword}}); rec.Code != http.StatusSeeOther {
		t.Fatalf("confirm = %d", rec.Code)
	}
	if rec := send("/admin/"); rec.Code != http.StatusNoContent || ran != 1 {
		t.Errorf("with a pass = %d, ran %d", rec.Code, ran)
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
	carol := signInAt(t, h, "/forum/", "carol")
	for _, target := range []string{"/forum/admin/", "/forum/admin", "/forum/admin/log"} {
		if rec := get(t, h, target); rec.Code != http.StatusNotFound {
			t.Errorf("a guest: %s = %d", target, rec.Code)
		}
		if rec := getAs(t, h, target, carol); rec.Code != http.StatusNotFound {
			t.Errorf("carol: %s = %d", target, rec.Code)
		}
	}
}
