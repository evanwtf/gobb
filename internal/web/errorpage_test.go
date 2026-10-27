package web

import (
	"html/template"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// themed fails the test unless rec is an HTML page in the board's layout: the
// layout's stylesheet, the board name of the header, the status in the notice
// and the heading. It returns the body.
func themed(t *testing.T, what string, rec *httptest.ResponseRecorder, base string, status int, heading string) string {
	t.Helper()
	body := rec.Body.String()
	if rec.Code != status {
		t.Errorf("%s: status %d, want %d", what, rec.Code, status)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
		t.Errorf("%s: Content-Type %q, want text/html: a plain-text error reached the visitor:\n%.200s", what, ct, body)
	}
	for _, want := range []string{
		"<!doctype html>",
		`<link rel="stylesheet" href="` + base + `static/gobb.css?v=`,
		`alt="Example Board"`,
		`<p class="notice-status">` + strconv.Itoa(status) + `</p>`,
		`<h1 id="notice-title">` + heading + `</h1>`,
		`<meta name="robots" content="noindex">`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("%s: the page lacks %q:\n%.300s", what, want, body)
		}
	}
	return body
}

// Every error page is themed, 5xx included (spec 13). With the database down,
// a page answers in the board's layout, from the copy of the settings that the
// site keeps in memory: no session, no nav counts, no database.
func TestErrorPagesWithTheDatabaseDown(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		pool, dataDir := boardtest.Load(t, bundletest.Small())
		h, err := New(t.Context(), pool, Options{DataDir: dataDir, BasePath: base, LoginFloor: time.Millisecond})
		if err != nil {
			t.Fatal(err)
		}
		// A member's cookie must not make the page look a session up.
		alice := signInAt(t, h, base, "alice")
		pool.Close()
		for _, c := range []struct {
			target  string
			cookies bool
			status  int
			heading string
		}{
			{base, false, http.StatusInternalServerError, "Something went wrong"},
			{base + "t/1", true, http.StatusInternalServerError, "Something went wrong"},
			{base + "f/2?page=2", false, http.StatusInternalServerError, "Something went wrong"},
			{base + "nope", false, http.StatusNotFound, "Page not found"},
		} {
			var cookies = alice
			if !c.cookies {
				cookies = nil
			}
			rec := getAs(t, h, c.target, cookies)
			body := themed(t, "base "+base+" GET "+c.target, rec, base, c.status, c.heading)
			if strings.Contains(body, "Signed in as") || strings.Contains(body, `name="csrf"`) {
				t.Errorf("base %s GET %s: the error page shows a session or a form", base, c.target)
			}
		}
		// A form post with the database down is themed too.
		rec := request(t, h, http.MethodPost, base+"login", alice, url.Values{"name": {"alice"}, "password": {"x"}, "csrf": {"x"}})
		themed(t, "base "+base+" POST login", rec, base, http.StatusInternalServerError, "Something went wrong")
	}
}

// The copy of the settings follows the database: a page that reads new
// settings replaces it, so the next error page shows them.
func TestErrorPageFollowsTheSettings(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	h, err := New(t.Context(), pool, Options{DataDir: dataDir, LoginFloor: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(t.Context(), `update settings set value = '"Renamed Board"' where key = 'board_name'`); err != nil {
		t.Fatal(err)
	}
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ = %d", rec.Code)
	}
	pool.Close()
	body := bodyOf(t, get(t, h, "/t/1"))
	if !strings.Contains(body, `alt="Renamed Board"`) || strings.Contains(body, `alt="Example Board"`) {
		t.Errorf("the error page does not show the settings of the last page:\n%.400s", body)
	}
}

// A request whose deadline has passed gets a themed answer too.
func TestErrorPageAfterTheDeadline(t *testing.T) {
	old := requestTimeout
	requestTimeout = time.Nanosecond
	t.Cleanup(func() { requestTimeout = old })
	h, _ := smallSite(t)
	themed(t, "GET / after the deadline", get(t, h, "/"), "/", http.StatusInternalServerError, "Something went wrong")
}

// brokenTemplate is a layout that fails when it runs.
var brokenTemplate = template.Must(template.New("broken").Parse(`{{define "layout"}}<p>{{.NoSuchField}}</p>{{end}}`))

// A page template that fails gives the themed 500 of the error template. When
// the error template fails too, the answer is the themed 500 that New built at
// start.
func TestErrorPageWhenATemplateFails(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _ := smallSite(t)
	onNew = nil
	if s == nil {
		t.Fatal("New did not hand the site to the test hook")
	}
	s.pages["topic.html"] = brokenTemplate
	themed(t, "GET /t/1 with a broken topic template", get(t, h, "/t/1"), "/", http.StatusInternalServerError, "Something went wrong")

	want500, want503 := string((*s.staticErrors.Load())[http.StatusInternalServerError]), string((*s.staticErrors.Load())[http.StatusServiceUnavailable])
	for status, b := range map[int]string{500: want500, 503: want503} {
		if !strings.Contains(b, `<link rel="stylesheet" href="/static/gobb.css?v=`) || !strings.Contains(b, `alt="Example Board"`) ||
			!strings.Contains(b, `<p class="notice-status">`) || !strings.Contains(b, `<meta name="robots" content="noindex">`) {
			t.Errorf("the page built at start for %d is not themed:\n%.400s", status, b)
		}
	}
	for name := range s.pages {
		s.pages[name] = brokenTemplate
	}
	for _, target := range []string{"/", "/t/1", "/nope", "/login"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusInternalServerError || rec.Body.String() != want500 ||
			rec.Header().Get("Content-Type") != "text/html; charset=utf-8" || rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("GET %s with every template broken: %d %q %q, want the themed 500 built at start, not stored:\n%.300s",
				target, rec.Code, rec.Header().Get("Content-Type"), rec.Header().Get("Cache-Control"), rec.Body.String())
		}
	}
	// A page that fails with 503 (the sign-in page when the checks are busy)
	// keeps its status, and gets the 503 built at start.
	rec := httptest.NewRecorder()
	s.renderFailed(rec, http.StatusServiceUnavailable)
	if rec.Code != http.StatusServiceUnavailable || rec.Body.String() != want503 {
		t.Errorf("a failed 503 page: %d, want 503 and the page built at start:\n%.300s", rec.Code, rec.Body.String())
	}
}

// Outside the base path, nothing of the site answers, but the 404 is themed.
func TestErrorPageOutsideTheBasePath(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Small(), Options{BasePath: "/forum/"})
	for _, target := range []string{"/", "/other", "/favicon.ico/x"} {
		themed(t, "GET "+target, get(t, h, target), "/forum/", http.StatusNotFound, "Page not found")
	}
	rec := do(t, h, http.MethodPost, "/favicon.ico")
	if rec.Code == http.StatusMethodNotAllowed || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
		t.Errorf("POST /favicon.ico: %d %q, want a themed page", rec.Code, rec.Header().Get("Content-Type"))
	}
}

// No handler of the site writes a plain-text error: only /healthz, which
// machines read, does (spec 13). The source holds no call of http.Error or
// http.NotFound, and the two answers of the standard library that a handler
// does not write itself are themed too: the 416 of http.ServeContent for a
// Range that no byte fits, and the 404 of http.StripPrefix for a path under the
// base path whose prefix is percent-encoded.
func TestNoPlainTextErrors(t *testing.T) {
	t.Run("range", testRangeErrorsAreThemed)
	t.Run("base path prefix", testBasePathPrefixErrorIsThemed)
	t.Run("source", testNoPlainTextCalls)
}

// A Range beyond the end of a file gets the themed 416 from every handler that
// calls http.ServeContent: /static/, /favicon.ico, /files/ and /a/. The answer
// is not cached, and carries neither the file's type nor its disposition.
func testRangeErrorsAreThemed(t *testing.T) {
	for _, base := range []string{"/", "/forum/"} {
		h, pool := siteWithPool(t, bundletest.Small(), Options{BasePath: base})
		if _, err := pool.Exec(t.Context(), `insert into acl_group_grants (group_id, forum_id, option, setting)
			values (1, 0, 'account.download', 3), (1, 2, 'attach.download', 3)`); err != nil {
			t.Fatal(err)
		}
		for _, target := range []string{"static/gobb.css", "favicon.ico", "files/avatars/1.png", "a/1"} {
			if target == "favicon.ico" {
				// The icon lives at the host root, outside the base path.
				target = "/favicon.ico"
			} else {
				target = base + target
			}
			// The file answers 200 to a Range that fits, so the 416 below is the
			// Range's doing and not a missing file.
			ok := getWith(t, h, target, nil, map[string]string{"Range": "bytes=0-0"})
			if ok.Code != http.StatusPartialContent {
				t.Fatalf("base %s GET %s with Range bytes=0-0: status %d, want 206", base, target, ok.Code)
			}
			for _, method := range []string{http.MethodGet, http.MethodHead} {
				r := httptest.NewRequest(method, target, nil)
				r.Header.Set("Range", "bytes=99999999-")
				rec := httptest.NewRecorder()
				h.ServeHTTP(rec, r)
				what := "base " + base + " " + method + " " + target + " with a Range past the end"
				if method == http.MethodHead {
					if rec.Code != http.StatusRequestedRangeNotSatisfiable || rec.Header().Get("Content-Type") != "text/html; charset=utf-8" {
						t.Errorf("%s: %d %q, want a 416 text/html", what, rec.Code, rec.Header().Get("Content-Type"))
					}
					continue
				}
				themed(t, what, rec, base, http.StatusRequestedRangeNotSatisfiable, "Range not satisfiable")
				for _, k := range []string{"Content-Disposition", "Last-Modified", "Etag"} {
					if v := rec.Header().Get(k); v != "" {
						t.Errorf("%s: header %s = %q, want none", what, k, v)
					}
				}
				if cc := rec.Header().Get("Cache-Control"); strings.Contains(cc, "public") || strings.Contains(cc, "max-age") {
					t.Errorf("%s: Cache-Control %q lets a cache keep the error", what, cc)
				}
				if cr := rec.Header().Get("Content-Range"); !strings.HasPrefix(cr, "bytes */") {
					t.Errorf("%s: Content-Range %q, want bytes */size (RFC 9110)", what, cr)
				}
			}
		}
	}
}

// Under a base path, a request whose raw path spells a letter of the base path
// as a percent escape gets the themed 404, not the plain text of
// http.StripPrefix.
func testBasePathPrefixErrorIsThemed(t *testing.T) {
	h, _ := newTestSite(t, bundletest.Small(), Options{BasePath: "/forum/"})
	for _, target := range []string{"/fo%72um/t/1", "/%66orum/", "/%66orum/static/gobb.css"} {
		themed(t, "GET "+target, get(t, h, target), "/forum/", http.StatusNotFound, "Page not found")
	}
	// The same page by its plain spelling still answers.
	if rec := get(t, h, "/forum/t/1"); rec.Code != http.StatusOK {
		t.Errorf("GET /forum/t/1: %d, want 200", rec.Code)
	}
	// A percent escape after the prefix is fine.
	if rec := get(t, h, "/forum/t/%31"); rec.Code != http.StatusOK {
		t.Errorf("GET /forum/t/%%31: %d, want 200", rec.Code)
	}
}

func testNoPlainTextCalls(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	plain := regexp.MustCompile(`http\.(Error|NotFound|StripPrefix|ServeFile|ServeFileFS|FileServer|FileServerFS)\(`)
	serve := regexp.MustCompile(`http\.ServeContent\(`)
	allowed := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "health.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		// site.serveContent, in files.go, is the one caller of ServeContent. Its
		// body runs from its func line to the next line that starts with "}".
		inServe := false
		for i, line := range strings.Split(string(b), "\n") {
			if f == "files.go" && strings.HasPrefix(line, "func (s *site) serveContent(") {
				inServe = true
			}
			if inServe && strings.HasPrefix(line, "}") {
				inServe = false
			}
			if strings.HasPrefix(strings.TrimSpace(line), "//") {
				continue
			}
			if plain.MatchString(line) {
				t.Errorf("%s:%d writes a plain-text error: %s", f, i+1, strings.TrimSpace(line))
			}
			if serve.MatchString(line) {
				if inServe {
					allowed++
				} else {
					t.Errorf("%s:%d calls http.ServeContent: use site.serveContent, which themes the 416: %s", f, i+1, strings.TrimSpace(line))
				}
			}
		}
	}
	if allowed != 1 {
		t.Errorf("site.serveContent calls http.ServeContent %d times, want 1", allowed)
	}
}
