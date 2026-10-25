package web

import (
	"context"
	"net/http"
	"net/http/httptest"
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

var timedLine = regexp.MustCompile(`^Rendered in (\d+)\.(\d\d) s with (\d+) quer(?:y|ies) · gobb ` +
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
		if strings.Contains(line, "with 1 queries") || strings.Contains(line, "with 0 query") {
			t.Errorf("%s: wrong plural: %q", target, line)
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
// answer must show its own count.
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

	countOf := func(line string) string {
		m := timedLine.FindStringSubmatch(line)
		if m == nil {
			t.Fatalf("the line %q does not match", line)
		}
		return m[3]
	}
	// Alone, each page runs a number of queries that the second tracer sees.
	want := map[string]string{}
	for _, target := range []string{"/", "/t/1"} {
		get(t, h, target) // warm up
		before := rec.n.Load()
		r := get(t, h, target)
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
			h.ServeHTTP(r, httptest.NewRequest(http.MethodGet, target, nil))
			m := buildLineRE.FindStringSubmatch(r.Body.String())
			if m == nil {
				t.Errorf("%s: no build line", target)
				return
			}
			tm := timedLine.FindStringSubmatch(m[1])
			if tm == nil || tm[3] != want[target] {
				t.Errorf("%s: the line is %q, want %s queries", target, m[1], want[target])
			}
		}()
	}
	wg.Wait()
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
	ctx, _ := db.WithCounter(context.WithValue(context.Background(), startKey{}, time.Now()))
	for _, tc := range []struct {
		name   string
		ctx    context.Context
		status int
		timed  bool
	}{
		{"200", ctx, 200, true},
		{"204", ctx, 204, true},
		{"303", ctx, 303, false},
		{"400", ctx, 400, false},
		{"404", ctx, 404, false},
		{"500", ctx, 500, false},
		{"no context", nil, 200, false},
		{"no timing in the context", context.Background(), 200, false},
	} {
		line := footerLine(tc.ctx, tc.status)
		re := plainLine
		if tc.timed {
			re = timedLine
		}
		if !re.MatchString(line) {
			t.Errorf("%s: %q", tc.name, line)
		}
	}
}

func TestFooterCountsOneQuery(t *testing.T) {
	ctx, c := db.WithCounter(context.WithValue(context.Background(), startKey{}, time.Now()))
	if line := footerLine(ctx, 200); !strings.Contains(line, "with 0 queries") {
		t.Errorf("zero queries: %q", line)
	}
	c.Add()
	if line := footerLine(ctx, 200); !strings.Contains(line, "with 1 query ") {
		t.Errorf("one query: %q", line)
	}
	c.Add()
	if line := footerLine(ctx, 200); !strings.Contains(line, "with 2 queries") {
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
