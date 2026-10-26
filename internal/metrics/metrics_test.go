package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func written(t *testing.T, r *Registry) string {
	t.Helper()
	var b strings.Builder
	if _, err := r.WriteTo(&b); err != nil {
		t.Fatal(err)
	}
	return b.String()
}

// The text format: families sorted by name, series sorted by label values, label
// values and help text escaped, a histogram's buckets cumulative.
func TestWriteFormat(t *testing.T) {
	r := NewRegistry()
	c := r.CounterVec("t_requests_total", "Requests.\nSecond line, a \\ too.", "route", "code")
	c.Inc("/b", "2xx")
	c.Inc("/a", "5xx")
	c.Inc("/a", "5xx")
	c.Init("/q\"x\n", "2xx")
	r.CounterVec("t_plain_total", "A counter with no labels.").Inc()
	r.Gauge("t_floor_seconds", "A gauge.").Set(2.3)
	r.Gauge("t_big_bytes", "A large whole number.").Set(134217728)
	r.Info("t_build_info", "Build.", []string{"version"}, []string{"1.2.3"})
	r.GaugeFunc("t_absent", "No sample.", func() (float64, bool) { return 0, false })
	h := r.HistogramVec("t_duration_seconds", "Durations.", []float64{0.1, 1}, "route")
	h.Observe(0.1, "/a") // on a bound: in le="0.1"
	h.Observe(0.5, "/a") // le="1"
	h.Observe(20, "/a")  // +Inf only
	want := `# HELP t_absent No sample.
# TYPE t_absent gauge
# HELP t_big_bytes A large whole number.
# TYPE t_big_bytes gauge
t_big_bytes 134217728
# HELP t_build_info Build.
# TYPE t_build_info gauge
t_build_info{version="1.2.3"} 1
# HELP t_duration_seconds Durations.
# TYPE t_duration_seconds histogram
t_duration_seconds_bucket{route="/a",le="0.1"} 1
t_duration_seconds_bucket{route="/a",le="1"} 2
t_duration_seconds_bucket{route="/a",le="+Inf"} 3
t_duration_seconds_sum{route="/a"} 20.6
t_duration_seconds_count{route="/a"} 3
# HELP t_floor_seconds A gauge.
# TYPE t_floor_seconds gauge
t_floor_seconds 2.3
# HELP t_plain_total A counter with no labels.
# TYPE t_plain_total counter
t_plain_total 1
# HELP t_requests_total Requests.\nSecond line, a \\ too.
# TYPE t_requests_total counter
t_requests_total{route="/a",code="5xx"} 2
t_requests_total{route="/b",code="2xx"} 1
t_requests_total{route="/q\"x\n",code="2xx"} 0
`
	if got := written(t, r); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	if c.Value("/a", "5xx") != 2 || h.Count("/a") != 3 {
		t.Errorf("Value = %d, Count = %d", c.Value("/a", "5xx"), h.Count("/a"))
	}
}

// A second family with the same name replaces the first, so building the server
// twice in one process registers again with no error and no duplicate.
func TestRegisterReplaces(t *testing.T) {
	r := NewRegistry()
	r.Gauge("t_g", "First.").Set(1)
	r.Gauge("t_g", "Second.").Set(2)
	if got := written(t, r); got != "# HELP t_g Second.\n# TYPE t_g gauge\nt_g 2\n" {
		t.Errorf("output = %q", got)
	}
}

func TestWrongLabelCountPanics(t *testing.T) {
	c := NewRegistry().CounterVec("t_total", "T.", "a")
	defer func() {
		if recover() == nil {
			t.Error("Inc with two values for one label did not panic")
		}
	}()
	c.Inc("x", "y")
}

func TestHandler(t *testing.T) {
	r := NewRegistry()
	r.Gauge("t_g", "G.").Set(1)
	h := Handler(r)
	for _, tc := range []struct {
		method, path string
		code         int
		body         string
	}{
		{"GET", "/metrics", 200, "# HELP t_g G.\n# TYPE t_g gauge\nt_g 1\n"},
		{"HEAD", "/metrics", 200, ""},
		{"POST", "/metrics", 405, "method not allowed\n"},
		{"GET", "/", 404, "404 page not found\n"},
		{"GET", "/metrics/x", 404, "404 page not found\n"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(tc.method, tc.path, nil))
		body, _ := io.ReadAll(rec.Body)
		if rec.Code != tc.code || string(body) != tc.body {
			t.Errorf("%s %s = %d %q, want %d %q", tc.method, tc.path, rec.Code, body, tc.code, tc.body)
		}
		if tc.code == 200 && rec.Header().Get("Content-Type") != ContentType {
			t.Errorf("%s %s: Content-Type %q", tc.method, tc.path, rec.Header().Get("Content-Type"))
		}
		if tc.code == 405 && rec.Header().Get("Allow") != "GET, HEAD" {
			t.Errorf("405 without Allow: %q", rec.Header().Get("Allow"))
		}
	}
}

// Cached runs f once in each interval and gives no sample after a failure.
func TestCached(t *testing.T) {
	calls := 0
	fail := false
	f := Cached("t", time.Hour, time.Second, func(context.Context) (float64, error) {
		calls++
		if fail {
			return 0, errors.New("down")
		}
		return float64(calls), nil
	})
	if v, ok := f(); !ok || v != 1 {
		t.Fatalf("first = %v, %v", v, ok)
	}
	if v, ok := f(); !ok || v != 1 || calls != 1 {
		t.Errorf("second = %v, %v after %d calls, want the cached 1", v, ok, calls)
	}
	fail = true
	g := Cached("t", 0, time.Second, func(context.Context) (float64, error) { return 0, errors.New("down") })
	if _, ok := g(); ok {
		t.Error("a failed run gave a sample")
	}
}

func TestStatusClassAndMethod(t *testing.T) {
	for code, want := range map[int]string{101: "1xx", 200: "2xx", 304: "3xx", 404: "4xx", 503: "5xx", 0: "other", 600: "other"} {
		if got := StatusClass(code); got != want {
			t.Errorf("StatusClass(%d) = %q, want %q", code, got, want)
		}
	}
	for m, want := range map[string]string{"GET": "GET", "HEAD": "HEAD", "POST": "POST", "PUT": "other", "get": "other", "PROPFIND": "other"} {
		if got := Method(m); got != want {
			t.Errorf("Method(%q) = %q, want %q", m, got, want)
		}
	}
}

// Default carries the runtime and process metrics, and the gobb counters with
// their result labels at 0 before any event.
func TestDefaultFamilies(t *testing.T) {
	out := written(t, Default)
	for _, want := range []string{
		"\ngo_goroutines ", "\ngo_memstats_heap_alloc_bytes ", "\ngo_gc_cycles_total ",
		"\nprocess_start_time_seconds ", "\nprocess_cpu_seconds_total ",
		`gobb_signin_attempts_total{result="wrong"} `, `gobb_emails_total{result="failed"} `,
		`gobb_reset_emails_skipped_total{reason="admin"} `, "# TYPE gobb_http_request_duration_seconds histogram",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Default output lacks %q", want)
		}
	}
	if v, ok := readRuntime("/sched/goroutines:goroutines"); !ok || v < 1 {
		t.Errorf("goroutines = %v, %v", v, ok)
	}
	if _, ok := readRuntime("/no/such:metric"); ok {
		t.Error("an unknown runtime metric gave a value")
	}
}

var _ http.Handler = Handler(NewRegistry())
