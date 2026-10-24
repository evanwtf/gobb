package web

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// sitePathAttr finds the value of every href, src and action attribute.
var sitePathAttr = regexp.MustCompile(`\b(?:href|src|action)="([^"]*)"`)

// checkBasePath fails the test for any href, src or action in body that is not
// a site path under base, an https or mailto URL, or an anchor.
func checkBasePath(t *testing.T, where, body, base string) {
	t.Helper()
	for _, m := range sitePathAttr.FindAllStringSubmatch(body, -1) {
		v := m[1]
		switch {
		case strings.HasPrefix(v, base), strings.HasPrefix(v, "https:"),
			strings.HasPrefix(v, "mailto:"), strings.HasPrefix(v, "#"):
		default:
			t.Errorf("%s: %q is not under %s", where, v, base)
		}
	}
}

// TestBasePathEverywhere checks that every link and source on every page is
// under the base path. Later tasks add their pages to the list.
func TestBasePathEverywhere(t *testing.T) {
	b := linkBoard()
	h, pool := siteWithPool(t, b, Options{BasePath: "/forum/"})
	pages := []string{"/forum/", "/forum/f/1", "/forum/f/2", "/forum/t/1", "/forum/t/5", "/forum/t/6", "/forum/login"}
	for _, p := range pages {
		rec := get(t, h, p)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", p, rec.Code)
			continue
		}
		body := bodyOf(t, rec)
		if !sitePathAttr.MatchString(body) {
			t.Errorf("%s: no links found", p)
		}
		checkBasePath(t, p, body, "/forum/")
	}
	// A member's pages hold the badges, the jump links and the mark-read forms.
	// They stay under the base path too.
	alice := signInAt(t, h, "/forum/", "alice")
	if _, err := pool.Exec(t.Context(), `update users set last_visit_at = '2019-06-01T00:00:00Z', last_mark_at = null where id = 1`); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/forum/", "/forum/f/2", "/forum/f/1", "/forum/new", "/forum/t/1"} {
		rec := getAs(t, h, p, alice)
		if rec.Code != http.StatusOK {
			t.Errorf("member %s: status %d", p, rec.Code)
			continue
		}
		body := bodyOf(t, rec)
		checkBasePath(t, "member "+p, body, "/forum/")
		if p != "/forum/t/1" && !strings.Contains(body, `action="/forum/`) {
			t.Errorf("member %s: no form", p)
		}
	}
	for _, want := range []string{`action="/forum/read"`, `href="/forum/t/1/new"`} {
		if !strings.Contains(bodyOf(t, getAs(t, h, "/forum/", alice))+bodyOf(t, getAs(t, h, "/forum/f/2", alice)), want) {
			t.Errorf("the member pages lack %s", want)
		}
	}
	if !strings.Contains(bodyOf(t, getAs(t, h, "/forum/f/2", alice)), `action="/forum/f/2/read"`) {
		t.Error("the forum page lacks the mark-forum form under the base path")
	}
	if rec := getAs(t, h, "/forum/t/1/new", alice); rec.Code != http.StatusFound || !strings.HasPrefix(rec.Header().Get("Location"), "/forum/t/1") {
		t.Errorf("/forum/t/1/new = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec, _ := postAsAt(t, h, "/forum/", "/forum/f/2/read", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/f/2" {
		t.Errorf("POST /forum/f/2/read = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	rec, _ = postAsAt(t, h, "/forum/", "/forum/read", alice, url.Values{})
	if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/forum/" {
		t.Errorf("POST /forum/read = %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if rec := get(t, h, "/forum/new"); rec.Code != http.StatusFound || rec.Header().Get("Location") != "/forum/login?next=%2Fforum%2Fnew" {
		t.Errorf("guest /forum/new = %d %q", rec.Code, rec.Header().Get("Location"))
	}

	// The rich post holds links and images that the author wrote as site paths.
	body := bodyOf(t, get(t, h, "/forum/t/5"))
	for _, want := range []string{`href="/forum/t/1"`, `src="/forum/a/3"`, `src="/forum/files/smilies/smile.gif"`} {
		if !strings.Contains(body, want) {
			t.Errorf("/forum/t/5 lacks %s", want)
		}
	}
	// Redirects keep the base path too.
	for _, c := range []struct{ from, to string }{
		{"/forum/p/4", "/forum/t/1#p4"},
		{"/forum/t/3", "/forum/t/1"},
		// A link forum with a site path as its target.
		{"/forum/f/4", "/forum/pages/rules?x=1#top"},
	} {
		rec := get(t, h, c.from)
		if loc := rec.Header().Get("Location"); loc != c.to {
			t.Errorf("%s: Location %q, want %q (status %d)", c.from, loc, c.to, rec.Code)
		}
	}
	for _, p := range []string{"/t/1", "/f/2", "/p/4", "/a/1"} {
		if rec := get(t, h, p); rec.Code != http.StatusNotFound {
			t.Errorf("%s without the prefix: status %d, want 404", p, rec.Code)
		}
	}
}

// signInAt signs in the made-up user name under a base path whose index is home.
func signInAt(t *testing.T, h http.Handler, home, name string) []*http.Cookie {
	t.Helper()
	rec, cookies := postAsAt(t, h, home, home+"login", nil, url.Values{"name": {name}, "password": {"example-password"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("sign-in of %s: status %d", name, rec.Code)
	}
	return cookies
}
