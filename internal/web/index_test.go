package web

import (
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestIndexPage(t *testing.T) {
	h, _ := smallSite(t)
	rec := get(t, h, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("Content-Type = %q", ct)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{
		`<html data-scheme="slate"`,
		`Example Board`,
		`src="/files/header/banner.png"`,
		`General`,
		`Main Discussion`,
		`The main forum`,
		`<bdi>Visitor</bdi>`,
		`href="/p/4"`,
		`Welcome to the example board`,
		`<time datetime="2020-02-01T11:00:00Z">Feb 1, 2020, 6:00 am</time>`,
		`/static/gobb.css`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	for _, bad := range []string{"Members Lounge", "A quiet room", "/u/", "Favorite color"} {
		if strings.Contains(body, bad) {
			t.Errorf("the guest page holds %q", bad)
		}
	}
}

func TestIndexHead(t *testing.T) {
	h, _ := smallSite(t)
	rec := do(t, h, http.MethodHead, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD / = %d", rec.Code)
	}
}

func TestIndexCountsAndSubforums(t *testing.T) {
	h, _ := smallSite(t)
	body := bodyOf(t, get(t, h, "/"))
	// The forum row carries its topic and post counts for the guest: 1 and 3.
	row := regexp.MustCompile(`(?s)Main Discussion.*?</tr>`).FindString(body)
	if row == "" {
		t.Fatal("no row for Main Discussion")
	}
	if !regexp.MustCompile(`>1<`).MatchString(row) || !regexp.MustCompile(`>3<`).MatchString(row) {
		t.Errorf("row lacks the counts 1 and 3:\n%s", row)
	}
	if strings.Contains(row, "Subforums:") {
		t.Error("the guest sees a subforum line")
	}
}

func TestIndexLinkForumAndLooseForum(t *testing.T) {
	b := bundletest.Small()
	b.Forums = append(b.Forums,
		bundleForum(4, 0, "forum", "Loose Talk", 2),
		bundleForum(5, 0, "link", "Elsewhere", 3),
	)
	url := "https://example.com/out"
	b.Forums[4].LinkURL = &url
	for _, id := range []int64{4, 5} {
		b.Grants = append(b.Grants, guestGrant(id, "forum.list"), guestGrant(id, "forum.read"))
	}
	h, _ := newTestSite(t, b, Options{})
	body := bodyOf(t, get(t, h, "/"))
	if !strings.Contains(body, "Loose Talk") || !strings.Contains(body, "Elsewhere") {
		t.Fatal("a loose forum or a link forum is missing")
	}
	// The name and the link go through /f/5, which redirects to the URL.
	if !strings.Contains(body, `href="/f/5"`) || strings.Contains(body, `href="https://example.com/out"`) {
		t.Error("the link forum does not link through /f/5")
	}
	if !strings.Contains(body, "https://example.com/out") {
		t.Error("the link forum does not show its URL")
	}
	// A link forum shows no counts, and a loose forum sits under a bar with no title.
	link := regexp.MustCompile(`(?s)Elsewhere.*?</tr>`).FindString(body)
	if strings.Contains(link, "forum-count") {
		t.Errorf("link forum shows counts:\n%s", link)
	}
	if !strings.Contains(body, `class="category untitled"`) {
		t.Error("no untitled bar above the loose forums")
	}
}

func TestSchemePreview(t *testing.T) {
	h, _ := smallSite(t)
	for _, c := range []struct{ query, want string }{
		{"?scheme=slate-light", `data-scheme="slate-light"`},
		{"?scheme=slate-deep", `data-scheme="slate-deep"`},
		{"?scheme=slate", `data-scheme="slate"`},
		{"?scheme=evil", `data-scheme="slate"`},
		{"?scheme=", `data-scheme="slate"`},
	} {
		rec := get(t, h, "/"+c.query)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d", c.query, rec.Code)
		}
		if !strings.Contains(bodyOf(t, rec), c.want) {
			t.Errorf("%s: body lacks %s", c.query, c.want)
		}
		if len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: the preview set a cookie", c.query)
		}
	}
}

func TestDefaultSchemeFromSettings(t *testing.T) {
	b := bundletest.Small()
	b.Settings.DefaultColorScheme = "slate-deep"
	h, _ := newTestSite(t, b, Options{})
	if !strings.Contains(bodyOf(t, get(t, h, "/")), `data-scheme="slate-deep"`) {
		t.Error("the page ignores the default scheme of the board")
	}
}

func TestNoHeaderImage(t *testing.T) {
	b := bundletest.Small()
	b.Settings.HeaderImage = nil
	delete(b.Files, "header/banner.png")
	h, _ := newTestSite(t, b, Options{})
	body := bodyOf(t, get(t, h, "/"))
	if strings.Contains(body, "/files/header/") {
		t.Error("the page links a header image that the board lacks")
	}
	if !strings.Contains(body, "Example Board") {
		t.Error("the board name is missing")
	}
}

func TestNotFound(t *testing.T) {
	h, _ := smallSite(t)
	for _, target := range []string{"/nope", "/static/missing.css", "/f/99999", "/index.php"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s: status %d, want 404", target, rec.Code)
			continue
		}
		body := bodyOf(t, rec)
		if !strings.Contains(body, "Page not found") || !strings.Contains(body, "Example Board") {
			t.Errorf("%s: the 404 page lacks the layout or the text", target)
		}
	}
}

func TestBasePath(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Small(), Options{BasePath: "/forum/"})
	rec := get(t, h, "/forum/")
	if rec.Code != http.StatusOK {
		t.Fatalf("/forum/ = %d", rec.Code)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{
		`src="/forum/files/header/banner.png"`,
		`href="/forum/static/gobb.css`,
		`href="/forum/p/4"`,
		`href="/forum/"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body lacks %q", want)
		}
	}
	// Every site path in the page starts with the base path.
	for _, m := range regexp.MustCompile(`(?:href|src|action)="(/[^"]*)"`).FindAllStringSubmatch(body, -1) {
		if !strings.HasPrefix(m[1], "/forum/") {
			t.Errorf("path %q lacks the base path", m[1])
		}
	}
	if rec := get(t, h, "/"); rec.Code != http.StatusNotFound {
		t.Errorf("/ with base /forum/ = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/files/header/banner.png"); rec.Code != http.StatusNotFound {
		t.Errorf("/files without the base path = %d, want 404", rec.Code)
	}
	if rec := get(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 at the host root", rec.Code)
	}
	if rec := get(t, h, "/forum/files/header/banner.png"); rec.Code != http.StatusOK {
		t.Errorf("file under the base path = %d", rec.Code)
	}
}

func TestHealthzAtRoot(t *testing.T) {
	h, _ := smallSite(t)
	rec := get(t, h, "/healthz")
	if rec.Code != http.StatusOK || strings.TrimSpace(bodyOf(t, rec)) != "ok" {
		t.Errorf("/healthz = %d %q", rec.Code, bodyOf(t, rec))
	}
}

// The templates carry no inline script, no style attribute and no style
// element, so that a strict content security policy can forbid them.
func TestTemplatesAreCSPClean(t *testing.T) {
	h, _ := smallSite(t)
	for _, target := range []string{"/", "/nope"} {
		body := strings.ToLower(bodyOf(t, get(t, h, target)))
		if strings.Contains(body, "<style") || regexp.MustCompile(`\sstyle\s*=`).MatchString(body) {
			t.Errorf("%s: inline style", target)
		}
		for _, m := range regexp.MustCompile(`<script[^>]*>`).FindAllString(body, -1) {
			if !strings.Contains(m, "src=") {
				t.Errorf("%s: inline script %s", target, m)
			}
		}
		if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(body) {
			t.Errorf("%s: inline event handler", target)
		}
	}
}
