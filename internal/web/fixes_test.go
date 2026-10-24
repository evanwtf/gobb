package web

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
)

// linkBoard is the rich board with link forums: 4 holds a site path and 5 an
// https URL. The bundle format refuses any other kind of target.
func linkBoard() *bundletest.Board {
	b := richBoard()
	for i, c := range []struct {
		id  int64
		url string
	}{
		{4, "/pages/rules?x=1#top"}, {5, "https://example.com/out"},
	} {
		b.Forums = append(b.Forums, bundleForum(c.id, 0, "link", "Link "+c.url, 10+i))
		b.Forums[len(b.Forums)-1].LinkURL = ptr(c.url)
		b.Grants = append(b.Grants, guestGrant(c.id, "forum.list"), guestGrant(c.id, "forum.read"))
	}
	return b
}

// A link forum links through /f/{id}, so the index never writes a raw URL, and
// the redirect puts the base path in front of a site path.
func TestLinkForumBasePath(t *testing.T) {
	h, _ := newTestSite(t, linkBoard(), Options{BasePath: "/forum/"})
	body := bodyOf(t, get(t, h, "/forum/"))
	checkBasePath(t, "/forum/", body, "/forum/")
	if !strings.Contains(body, `href="/forum/f/4"`) {
		t.Error("the link forum does not link to /forum/f/4")
	}
	for _, c := range []struct {
		target string
		code   int
		loc    string
	}{
		{"/forum/f/4", http.StatusFound, "/forum/pages/rules?x=1#top"},
		{"/forum/f/5", http.StatusFound, "https://example.com/out"},
	} {
		rec := get(t, h, c.target)
		if rec.Code != c.code || rec.Header().Get("Location") != c.loc {
			t.Errorf("%s = %d %q, want %d %q", c.target, rec.Code, rec.Header().Get("Location"), c.code, c.loc)
		}
	}
}

func TestLinkTarget(t *testing.T) {
	s := &site{base: "/forum/"}
	for in, want := range map[string]string{
		"/pages/rules":      "/forum/pages/rules",
		"https://e.com/x":   "https://e.com/x",
		"mailto:a@e.com":    "mailto:a@e.com",
		"//evil.example/x":  "",
		"/\\evil.example":   "",
		"http://e.com":      "https://e.com",
		"HTTP://e.com/x?a":  "https://e.com/x?a",
		"httpx://e.com":     "",
		"ftp://e.com":       "",
		"javascript:alert1": "",
		"pages/rules":       "",
		"":                  "",
	} {
		got, ok := s.linkTarget(in)
		if got != want || ok != (want != "") {
			t.Errorf("linkTarget(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}

// A symlink inside a directory is not a regular file, even when it points at
// another file of the same directory.
func TestFilesRefuseInRootSymlink(t *testing.T) {
	h, dataDir := smallSite(t)
	png, err := os.ReadFile(filepath.Join(dataDir, "avatars", "1.png"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "avatars", ".secret.png"), png, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".secret.png", filepath.Join(dataDir, "avatars", "s.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("1.png", filepath.Join(dataDir, "avatars", "t.png")); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"s.png", "t.png", ".secret.png"} {
		if rec := get(t, h, "/files/avatars/"+name); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, rec.Code)
		}
	}
	if rec := get(t, h, "/files/avatars/1.png"); rec.Code != http.StatusOK {
		t.Errorf("1.png = %d, want 200", rec.Code)
	}
}

// A database with no imported board answers 503 on every page, with the layout
// and one hint, and keeps /healthz at 200.
func TestNoBoardYet(t *testing.T) {
	pool := dbtest.New(t)
	h, err := New(t.Context(), pool, Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"/", "/f/1", "/t/1", "/p/1"} {
		rec := get(t, h, target)
		body := bodyOf(t, rec)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s = %d, want 503", target, rec.Code)
		}
		if !strings.Contains(body, "<html data-scheme=") ||
			!strings.Contains(body, "This board has no data yet. Run `gobb import`.") {
			t.Errorf("%s: not the layout with the hint:\n%s", target, body)
		}
	}
	if rec := get(t, h, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200", rec.Code)
	}
}

// Each request's database work runs under a deadline of 10 s.
func TestRequestDeadline(t *testing.T) {
	s := &site{base: "/"}
	var got time.Time
	var ok bool
	h := s.guard(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		got, ok = r.Context().Deadline()
	}))
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if !ok {
		t.Fatal("the request context has no deadline")
	}
	if d := got.Sub(start); d < 9*time.Second || d > 11*time.Second {
		t.Errorf("deadline in %v, want about 10 s", d)
	}
}

// A request that waits on the database stops when the deadline passes.
func TestRequestDeadlineStopsQuery(t *testing.T) {
	pool := dbtest.New(t)
	old := requestTimeout
	requestTimeout = 100 * time.Millisecond
	t.Cleanup(func() { requestTimeout = old })
	s := &site{base: "/"}
	var err error
	h := s.guard(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		_, err = pool.Exec(r.Context(), `select pg_sleep(5)`)
	}))
	start := time.Now()
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
	if err == nil || time.Since(start) > 3*time.Second {
		t.Errorf("query ended after %v with %v, want a quick deadline error", time.Since(start), err)
	}
}

// A static file asked for with the current version is immutable. Any other
// request gets a short cache.
func TestStaticVersionedCache(t *testing.T) {
	h, _ := smallSite(t)
	body := bodyOf(t, get(t, h, "/"))
	i := strings.Index(body, "/static/gobb.css?v=")
	if i < 0 {
		t.Fatal("the page does not link a versioned stylesheet")
	}
	link := body[i : i+len("/static/gobb.css?v=")+10]
	if got := get(t, h, link).Header().Get("Cache-Control"); got != "public, max-age=31536000, immutable" {
		t.Errorf("%s: Cache-Control %q", link, got)
	}
	for _, target := range []string{"/static/gobb.css", "/static/gobb.css?v=stale", "/static/gobb.css?v="} {
		if got := get(t, h, target).Header().Get("Cache-Control"); got != "public, max-age=86400" {
			t.Errorf("%s: Cache-Control %q, want one day", target, got)
		}
	}
}

func TestNumFormat(t *testing.T) {
	for in, want := range map[int64]string{
		0: "0", 7: "7", 999: "999", 1000: "1,000", 12345: "12,345",
		1076250: "1,076,250", 1234567890: "1,234,567,890", -1234: "-1,234",
	} {
		if got := num(in); got != want {
			t.Errorf("num(%d) = %q, want %q", in, got, want)
		}
	}
	if got := plural(1076250, "view", "views"); got != "1,076,250 views" {
		t.Errorf("plural = %q", got)
	}
	if got := plural(1, "view", "views"); got != "1 view" {
		t.Errorf("plural = %q", got)
	}
}

// View counts and post counts carry thousands separators on the forum and topic
// pages, and the index.
func TestCountsHaveSeparators(t *testing.T) {
	b := richBoard()
	b.Topics[0].ViewCount = 1076250
	b.Users[0].PostCount = 1234567
	h, _ := newTestSite(t, b, Options{})
	forum := bodyOf(t, get(t, h, "/f/2"))
	if !strings.Contains(forum, `<span class="forum-count">1,076,250</span>`) {
		t.Error("the forum page does not format the view count")
	}
	topic := bodyOf(t, get(t, h, "/t/1"))
	if !strings.Contains(topic, "1,076,250 views") {
		t.Error("the topic page does not format the view count")
	}
	if !strings.Contains(topic, "<b>1,234,567</b>") {
		t.Error("the topic page does not format the author's post count")
	}
	if strings.Contains(forum+topic, "1076250") || strings.Contains(topic, "1234567") {
		t.Error("a raw count remains")
	}
}

func TestIndexCountsHaveSeparators(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(t.Context(), `update forums set topic_count = 1076250, post_count = 2345678 where id = 2`); err != nil {
		t.Fatal(err)
	}
	h, err := New(t.Context(), pool, Options{DataDir: dataDir})
	if err != nil {
		t.Fatal(err)
	}
	body := bodyOf(t, get(t, h, "/"))
	for _, want := range []string{`<span class="forum-count">1,076,250</span>`, `<span class="forum-count">2,345,678</span>`} {
		if !strings.Contains(body, want) {
			t.Errorf("index lacks %s", want)
		}
	}
}
