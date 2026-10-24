package web

import (
	"net/http"
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
	h, _ := newTestSite(t, b, Options{BasePath: "/forum/"})
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
