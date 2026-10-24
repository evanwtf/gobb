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

	want500, want503 := string(s.staticErrors[http.StatusInternalServerError]), string(s.staticErrors[http.StatusServiceUnavailable])
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
// machines read, does (spec 13).
func TestNoPlainTextErrors(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	plain := regexp.MustCompile(`http\.(Error|NotFound)\(`)
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "health.go" {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(b), "\n") {
			if plain.MatchString(line) && !strings.HasPrefix(strings.TrimSpace(line), "//") {
				t.Errorf("%s:%d writes a plain-text error: %s", f, i+1, strings.TrimSpace(line))
			}
		}
	}
}
