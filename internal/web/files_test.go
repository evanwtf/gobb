package web

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestFiles(t *testing.T) {
	h, dataDir := smallSite(t)

	// A file outside the data directory, a symlink to it, and some bad names.
	outside := filepath.Join(t.TempDir(), "outside.png")
	want, err := os.ReadFile(filepath.Join(dataDir, "avatars", "1.png"))
	if err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string][]byte{
		outside: want,
		filepath.Join(dataDir, "avatars", ".hidden.png"):     want,
		filepath.Join(dataDir, "avatars", "x.png"):           []byte("<script>alert(1)</script>"),
		filepath.Join(dataDir, ".import-x", "a.png"):         want,
		filepath.Join(dataDir, "avatars", ".d", "b.png"):     want,
		filepath.Join(dataDir, "avatars", "nested", "n.png"): want,
	} {
		if path != outside {
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(outside, filepath.Join(dataDir, "avatars", "link.png")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dataDir, "attachments"), filepath.Join(dataDir, "avatars", "dirlink")); err != nil {
		t.Fatal(err)
	}

	rec := get(t, h, "/files/avatars/1.png")
	if rec.Code != http.StatusOK {
		t.Fatalf("/files/avatars/1.png = %d", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); got != "image/png" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Errorf("X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "public, max-age=86400" {
		t.Errorf("Cache-Control = %q", got)
	}
	if !bytes.Equal(rec.Body.Bytes(), want) {
		t.Error("the body differs from the file")
	}

	for _, ok := range []struct{ target, ctype string }{
		{"/files/header/banner.png", "image/png"},
		{"/files/smilies/smile.gif", "image/gif"},
		{"/files/ranks/newcomer.png", "image/png"},
		{"/files/avatars/nested/n.png", "image/png"},
	} {
		rec := get(t, h, ok.target)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ok.ctype {
			t.Errorf("%s = %d %q, want 200 %q", ok.target, rec.Code, rec.Header().Get("Content-Type"), ok.ctype)
		}
	}
	if rec := do(t, h, http.MethodHead, "/files/avatars/1.png"); rec.Code != http.StatusOK || rec.Body.Len() != 0 {
		t.Errorf("HEAD = %d with %d body bytes", rec.Code, rec.Body.Len())
	}

	for _, target := range []string{
		"/files/attachments/1.png",
		"/files/avatars/../avatars/1.png",
		"/files/avatars/%2e%2e/avatars/1.png",
		"/files/avatars/..%2favatars/1.png",
		"/files/.import-x/a.png",
		"/files/.replaced-2026/a.png",
		"/files/avatars/.hidden.png",
		"/files/avatars/.d/b.png",
		"/files/avatars/link.png",
		"/files/avatars/dirlink/1.png",
		"/files/avatars/x.png",
		"/files/avatars/nested",
		"/files/avatars/nested/",
		"/files/avatars/missing.png",
		"/files/avatars",
		"/files/avatars/",
		"/files/",
		"/files/other/1.png",
		"/files/avatars//1.png",
		"/files/avatars/1.png/",
	} {
		rec := get(t, h, target)
		if rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", target, rec.Code)
		}
		if got := rec.Header().Get("Content-Type"); got == "image/png" {
			t.Errorf("%s answered an image", target)
		}
	}
}

func TestFilesOnlyImages(t *testing.T) {
	b := bundletest.Small()
	h, dataDir := newTestSite(t, b, Options{})
	// Real file names, but content that is not an image type gobb serves.
	for name, content := range map[string][]byte{
		"avatars/page.png":  []byte("<html><body>hi</body></html>"),
		"avatars/svg.png":   []byte(`<svg xmlns="http://www.w3.org/2000/svg"></svg>`),
		"avatars/text.png":  []byte("plain text"),
		"avatars/empty.png": {},
	} {
		if err := os.WriteFile(filepath.Join(dataDir, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
		if rec := get(t, h, "/files/"+name); rec.Code != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", name, rec.Code)
		}
	}
	// A GIF and a JPEG header are served with their sniffed type, whatever the name.
	if err := os.WriteFile(filepath.Join(dataDir, "avatars", "g.txt"), []byte("GIF89a\x01\x00\x01\x00"), 0o644); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/files/avatars/g.txt"); rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/gif" {
		t.Errorf("sniffed gif = %d %q", rec.Code, rec.Header().Get("Content-Type"))
	}
}

func TestStaticFiles(t *testing.T) {
	h, _ := smallSite(t)
	for target, ctype := range map[string]string{
		"/static/gobb.css": "text/css; charset=utf-8",
		"/static/gobb.js":  "text/javascript; charset=utf-8",
	} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != ctype {
			t.Errorf("%s = %d %q, want 200 %q", target, rec.Code, rec.Header().Get("Content-Type"), ctype)
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s lacks nosniff", target)
		}
		if rec.Body.Len() == 0 {
			t.Errorf("%s is empty", target)
		}
	}
}

// The site icon is a small SVG under /static/, and the layout links it under
// the base path. /favicon.ico at the host root answers with the same icon and
// does no database work: it answers while the pool is closed.
func TestSiteIcon(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		pool, dataDir := boardtest.Load(t, bundletest.Small())
		h, err := New(t.Context(), pool, Options{DataDir: dataDir, BasePath: base, LoginFloor: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		page := bodyOf(t, get(t, h, base))
		link := regexp.MustCompile(`<link rel="icon" type="image/svg\+xml" href="([^"]+)">`).FindStringSubmatch(page)
		if link == nil {
			t.Fatalf("base %s: the layout links no SVG icon", base)
		}
		if !strings.HasPrefix(link[1], base+"static/icon.svg?v=") {
			t.Errorf("base %s: icon link %q, want %sstatic/icon.svg?v=…", base, link[1], base)
		}
		icon := get(t, h, link[1])
		svg := icon.Body.String()
		if icon.Code != http.StatusOK || icon.Header().Get("Content-Type") != "image/svg+xml" ||
			icon.Header().Get("X-Content-Type-Options") != "nosniff" || !strings.HasPrefix(svg, "<svg ") {
			t.Errorf("base %s: icon = %d %q, body %.40q", base, icon.Code, icon.Header().Get("Content-Type"), svg)
		}
		if strings.Contains(strings.ToLower(svg), "script") || strings.Contains(svg, "href") {
			t.Errorf("base %s: the icon holds a script or a link", base)
		}
		pool.Close()
		rec := get(t, h, "/favicon.ico")
		if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/svg+xml" || rec.Body.String() != svg {
			t.Errorf("base %s: /favicon.ico with the pool closed = %d %q, want 200 and the icon", base, rec.Code, rec.Header().Get("Content-Type"))
		}
	}
}

// A browser that asks again with If-Modified-Since never learns from a 304 that
// a file exists, or how old it is: a viewer without the right to download gets
// 403, and a viewer who cannot read the forum gets 404, whatever the date.
func TestAttachmentRevalidationNeedsTheRight(t *testing.T) {
	h, _ := smallSite(t)
	// On the second board, attachment 1 sits on post 5, in forum 3.
	b := bundletest.Small()
	b.Attachments[0].PostID = ptr(int64(5))
	hidden, _ := newTestSite(t, b, Options{})
	for _, c := range []struct {
		h    http.Handler
		want int
	}{
		{h, http.StatusForbidden},     // forum 2: the guest reads it, and may not download
		{hidden, http.StatusNotFound}, // forum 3: the guest cannot read it
	} {
		for _, since := range []string{"Mon, 01 Jan 2035 00:00:00 GMT", "Sat, 01 Jan 2000 00:00:00 GMT"} {
			req := httptest.NewRequest(http.MethodGet, "/a/1", nil)
			req.Header.Set("If-Modified-Since", since)
			rec := httptest.NewRecorder()
			c.h.ServeHTTP(rec, req)
			if rec.Code != c.want || rec.Header().Get("Last-Modified") != "" {
				t.Errorf("/a/1 since %s: %d, Last-Modified %q; want %d and none", since, rec.Code, rec.Header().Get("Last-Modified"), c.want)
			}
		}
	}
}
