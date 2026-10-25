package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/version"
)

// buildLine finds the text of the build line in a page. It fails the test when
// the page holds no line, or two.
func buildLine(t *testing.T, body string) string {
	t.Helper()
	m := buildLineRE.FindAllStringSubmatch(body, -1)
	if len(m) != 1 {
		t.Fatalf("the page holds %d build lines, want 1", len(m))
	}
	return m[0][1]
}

var buildLineRE = regexp.MustCompile(`<p class="build-line">([^<]*)</p>`)

// adminLine is the line of an admin on a 2xx GET page: the time and the count.
var adminLine = regexp.MustCompile(`^Rendered in (\d+)\.(\d\d) s with (\d+) quer(?:y|ies) · gobb ` +
	regexp.QuoteMeta(version.Version) + ` · build ([0-9a-f]{8}|unknown)$`)

// timedLine is the line of every other viewer on a 2xx GET page: the time, and
// no count.
var timedLine = regexp.MustCompile(`^Rendered in (\d+)\.(\d\d) s · gobb ` +
	regexp.QuoteMeta(version.Version) + ` · build ([0-9a-f]{8}|unknown)$`)

var plainLine = regexp.MustCompile(`^gobb ` + regexp.QuoteMeta(version.Version) + ` · build ([0-9a-f]{8}|unknown)$`)

func TestFooterShowsBuildAndTiming(t *testing.T) {
	h, _ := smallSite(t)
	for _, target := range []string{"/", "/t/1", "/f/1"} {
		rec := get(t, h, target)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d", target, rec.Code)
		}
		line := buildLine(t, bodyOf(t, rec))
		m := timedLine.FindStringSubmatch(line)
		if m == nil {
			t.Errorf("%s: the line %q does not match", target, line)
			continue
		}
		if m[1] == "0" && m[2] == "00" {
			t.Errorf("%s: the line shows 0.00 s: %q", target, line)
		}
		if strings.Contains(line, "quer") {
			t.Errorf("%s: a guest sees a query count: %q", target, line)
		}
	}
}

func TestFooterHasNoTimingOnErrors(t *testing.T) {
	h, _ := siteWithPool(t, leakBoard(), Options{})

	missing := get(t, h, "/t/99")
	hidden := get(t, h, "/t/2")
	for name, rec := range map[string]*httptest.ResponseRecorder{"missing topic": missing, "hidden topic": hidden} {
		if rec.Code != http.StatusNotFound {
			t.Fatalf("%s: status %d, want 404", name, rec.Code)
		}
		if line := buildLine(t, bodyOf(t, rec)); !plainLine.MatchString(line) {
			t.Errorf("%s: the line %q is not the plain line", name, line)
		}
	}
	if shape(bodyOf(t, missing), "/t/99") != shape(bodyOf(t, hidden), "/t/2") {
		t.Error("the 404 of a hidden topic differs from the 404 of a missing one")
	}
	if headerShape(missing) != headerShape(hidden) {
		t.Error("the headers of the two 404 answers differ")
	}

	// A 403: a form with no token.
	rec := request(t, h, http.MethodPost, "/login", nil, map[string][]string{"name": {"carol"}})
	if rec.Code != http.StatusForbidden {
		t.Fatalf("POST with no token: status %d, want 403", rec.Code)
	}
	if line := buildLine(t, bodyOf(t, rec)); !plainLine.MatchString(line) {
		t.Errorf("403: the line %q is not the plain line", line)
	}
}

// TestStaticErrorPagesCarryThePlainLine covers the pages that need no database:
// the themed 500 and 503 that New builds, and the page that writeFailure builds.
func TestStaticErrorPagesCarryThePlainLine(t *testing.T) {
	var s *site
	onNew = func(x *site) { s = x }
	t.Cleanup(func() { onNew = nil })
	smallSite(t)
	if s == nil {
		t.Fatal("onNew did not run")
	}
	for status, body := range s.staticErrors {
		if line := buildLine(t, string(body)); !plainLine.MatchString(line) {
			t.Errorf("static %d: the line %q is not the plain line", status, line)
		}
	}
	rec := httptest.NewRecorder()
	s.writeFailure(rec, http.StatusServiceUnavailable)
	if line := buildLine(t, rec.Body.String()); !plainLine.MatchString(line) {
		t.Errorf("writeFailure: the line %q is not the plain line", line)
	}
}

// recTracer sees every query that the pool runs, besides the counter.
type recTracer struct {
	db.CountTracer
	n atomic.Int64
}

func (r *recTracer) TraceQueryStart(ctx context.Context, c *pgx.Conn, d pgx.TraceQueryStartData) context.Context {
	r.n.Add(1)
	return r.CountTracer.TraceQueryStart(ctx, c, d)
}

// TestQueryCountIsPerRequest counts the queries of two pages in a second way,
// with a tracer of the test, and then sends both pages at the same time. Each
// answer must show its own count. Only an admin sees the count.
func TestQueryCountIsPerRequest(t *testing.T) {
	pool, dataDir := boardtest.Load(t, bundletest.Small())
	cfg := pool.Config()
	rec := &recTracer{}
	cfg.ConnConfig.Tracer = rec
	counted, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(counted.Close)
	h, err := New(t.Context(), counted, Options{DataDir: dataDir, LoginFloor: time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	alice := signIn(t, h, "alice", "example-password")

	countOf := func(line string) string {
		m := adminLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("the line %q does not match", line)
		}
		return m[3]
	}
	// Alone, each page runs a number of queries that the second tracer sees.
	want := map[string]string{}
	for _, target := range []string{"/", "/t/1"} {
		getAs(t, h, target, alice) // warm up
		before := rec.n.Load()
		r := getAs(t, h, target, alice)
		ran := rec.n.Load() - before
		got := countOf(buildLine(t, bodyOf(t, r)))
		if got != strconv.FormatInt(ran, 10) {
			t.Errorf("%s: the line says %s queries, the tracer saw %d", target, got, ran)
		}
		want[target] = got
	}
	if want["/"] == want["/t/1"] {
		t.Fatalf("both pages run %s queries; the test cannot tell them apart", want["/"])
	}

	// Together, each page still shows its own count.
	var wg sync.WaitGroup
	for i := range 24 {
		target := []string{"/", "/t/1"}[i%2]
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			for _, c := range alice {
				req.AddCookie(c)
			}
			h.ServeHTTP(r, req)
			m := buildLineRE.FindStringSubmatch(r.Body.String())
			if m == nil {
				t.Errorf("%s: no build line", target)
				return
			}
			tm := adminLine.FindStringSubmatch(m[1])
			if tm == nil || tm[3] != want[target] {
				t.Errorf("%s: the line is %q, want %s queries", target, m[1], want[target])
			}
		}()
	}
	wg.Wait()
}

// TestFooterShowsCountOnlyToAdmins: the board index shows the count to an admin
// (alice) and to nobody else.
func TestFooterShowsCountOnlyToAdmins(t *testing.T) {
	h, _ := smallSite(t)
	alice := signIn(t, h, "alice", "example-password") // admin
	bob := signIn(t, h, "bob", "example-password")     // member
	for _, tc := range []struct {
		who     string
		cookies []*http.Cookie
		re      *regexp.Regexp
	}{
		{"admin", alice, adminLine},
		{"member", bob, timedLine},
		{"guest", nil, timedLine},
	} {
		for _, target := range []string{"/", "/t/1"} {
			rec := getAs(t, h, target, tc.cookies)
			if rec.Code != http.StatusOK {
				t.Fatalf("%s %s: status %d", tc.who, target, rec.Code)
			}
			if line := buildLine(t, bodyOf(t, rec)); !tc.re.MatchString(line) {
				t.Errorf("%s %s: the line %q does not match %s", tc.who, target, line, tc.re)
			}
		}
	}
	// HEAD follows GET: a HEAD answer is built the same way.
	rec := request(t, h, http.MethodHead, "/", alice, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("HEAD /: status %d", rec.Code)
	}
}

// TestFooterHidesWhetherANameExists: a failed sign-in answers 200 for a known
// name and for an unknown one. The query count used to differ by one. The two
// pages must now be the same once the echoed name and the csrf token are masked.
// The same holds for the email form of the account page.
func TestFooterHidesWhetherANameExists(t *testing.T) {
	h, _ := smallSite(t)
	mask := func(body, name string) string {
		return strings.ReplaceAll(withoutCSRF(body), name, "NAME")
	}
	for _, pair := range [][2]string{
		{"alice", "nobody"},
		{"alice@example.com", "nobody@example.com"},
	} {
		var bodies, lines [2]string
		for i, name := range pair {
			rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {name}, "password": {"wrong-password-here"}})
			if rec.Code != http.StatusOK {
				t.Fatalf("failed sign-in as %q: status %d, want 200", name, rec.Code)
			}
			body := bodyOf(t, rec)
			bodies[i], lines[i] = mask(body, name), buildLine(t, body)
			if !plainLine.MatchString(lines[i]) {
				t.Errorf("failed sign-in as %q: the line %q is not the plain line", name, lines[i])
			}
		}
		if lines[0] != lines[1] {
			t.Errorf("%v: the footer lines differ: %q and %q", pair, lines[0], lines[1])
		}
		if bodies[0] != bodies[1] {
			t.Errorf("%v: the pages differ", pair)
		}
	}

	// The email form: a member asks for an address that another member holds, and
	// for one that nobody holds, with a wrong current password.
	bob := signIn(t, h, "bob", "example-password")
	var lines [2]string
	for i, email := range []string{"alice@example.com", "nobody@example.com"} {
		rec, _ := postAs(t, h, "/account/email", bob, url.Values{"email": {email}, "current": {"wrong-password-here"}})
		lines[i] = buildLine(t, bodyOf(t, rec))
		if !plainLine.MatchString(lines[i]) {
			t.Errorf("email form, %q: the line %q is not the plain line (status %d)", email, lines[i], rec.Code)
		}
	}
	if lines[0] != lines[1] {
		t.Errorf("email form: the footer lines differ: %q and %q", lines[0], lines[1])
	}
}

// TestFooterIsPlainOnPost: a 200 answer to a POST shows the plain line, whoever
// the viewer is. A preview and a failed sign-in both answer 200.
func TestFooterIsPlainOnPost(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	alice := signIn(t, h, "alice", "example-password") // admin
	for _, tc := range []struct {
		name    string
		target  string
		cookies []*http.Cookie
		form    url.Values
	}{
		{"failed sign-in", "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong-password-here"}}},
		{"preview by an admin", "/t/1/reply", alice, url.Values{"body": {"A *preview* body."}, "preview": {"1"}}},
	} {
		rec, _ := postAs(t, h, tc.target, tc.cookies, tc.form)
		if rec.Code != http.StatusOK {
			t.Errorf("%s: status %d, want 200", tc.name, rec.Code)
			continue
		}
		if line := buildLine(t, bodyOf(t, rec)); !plainLine.MatchString(line) {
			t.Errorf("%s: the line %q is not the plain line", tc.name, line)
		}
	}
}

// TestQuoteOfHiddenPostLooksMissing is Review Focus 1 with the footer in the
// body: a member who quotes a post in a forum that the member cannot read gets
// the same bytes as one who quotes a post that does not exist.
func TestQuoteOfHiddenPostLooksMissing(t *testing.T) {
	h, _ := siteWithPool(t, postingBoard(), Options{})
	carol := signIn(t, h, "carol", "example-password")
	hidden := getAs(t, h, "/t/1/reply?quote=5", carol)
	missing := getAs(t, h, "/t/1/reply?quote=9999", carol)
	if hidden.Code != http.StatusNotFound || missing.Code != http.StatusNotFound {
		t.Fatalf("status %d and %d, want 404 and 404", hidden.Code, missing.Code)
	}
	hb, mb := shape(bodyOf(t, hidden), "/t/1/reply?quote=5"), shape(bodyOf(t, missing), "/t/1/reply?quote=9999")
	if hb != mb {
		t.Error("the 404 of a hidden post differs from the 404 of a missing one")
	}
	if line := buildLine(t, hb); !plainLine.MatchString(line) {
		t.Errorf("the line %q is not the plain line", line)
	}
}

func TestRenderTimeRoundsUp(t *testing.T) {
	for _, tc := range []struct {
		d    time.Duration
		want string
	}{
		{0, "0.01 s"},
		{time.Microsecond, "0.01 s"},
		{3 * time.Millisecond, "0.01 s"},
		{10 * time.Millisecond, "0.01 s"},
		{11 * time.Millisecond, "0.02 s"},
		{990 * time.Millisecond, "0.99 s"},
		{991 * time.Millisecond, "1.00 s"},
		{1234 * time.Millisecond, "1.24 s"},
		{12 * time.Second, "12.00 s"},
	} {
		if got := formatRender(tc.d); got != tc.want {
			t.Errorf("formatRender(%v) = %q, want %q", tc.d, got, tc.want)
		}
	}
}

func TestFooterLineShapes(t *testing.T) {
	get, _ := db.WithCounter(withClock(context.Background(), http.MethodGet))
	head, _ := db.WithCounter(withClock(context.Background(), http.MethodHead))
	post, _ := db.WithCounter(withClock(context.Background(), http.MethodPost))
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		status int
		admin  bool
		want   *regexp.Regexp
	}{
		{"200", get, 200, false, timedLine},
		{"200, admin", get, 200, true, adminLine},
		{"HEAD 200", head, 200, false, timedLine},
		{"HEAD 200, admin", head, 200, true, adminLine},
		{"204", get, 204, false, timedLine},
		{"POST 200", post, 200, false, plainLine},
		{"POST 200, admin", post, 200, true, plainLine},
		{"POST 303", post, 303, true, plainLine},
		{"303", get, 303, true, plainLine},
		{"400", get, 400, true, plainLine},
		{"404", get, 404, true, plainLine},
		{"500", get, 500, true, plainLine},
		{"no clock in the context", context.Background(), 200, true, plainLine},
		{"no counter in the context", withClock(context.Background(), http.MethodGet), 200, true, timedLine},
	} {
		line := footerLine(tc.ctx, tc.status, tc.admin)
		if !tc.want.MatchString(line) {
			t.Errorf("%s: %q", tc.name, line)
		}
	}
}

func TestFooterCountsOneQuery(t *testing.T) {
	ctx, c := db.WithCounter(withClock(context.Background(), http.MethodGet))
	if line := footerLine(ctx, 200, true); !strings.Contains(line, "with 0 queries") {
		t.Errorf("zero queries: %q", line)
	}
	c.Add()
	if line := footerLine(ctx, 200, true); !strings.Contains(line, "with 1 query ") {
		t.Errorf("one query: %q", line)
	}
	c.Add()
	if line := footerLine(ctx, 200, true); !strings.Contains(line, "with 2 queries") {
		t.Errorf("two queries: %q", line)
	}
}

func TestFooterClassIsInTheStylesheet(t *testing.T) {
	css, err := os.ReadFile("static/gobb.css")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(css), ".build-line") {
		t.Error("gobb.css does not define .build-line")
	}
}
