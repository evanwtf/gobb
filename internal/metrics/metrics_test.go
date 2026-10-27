package metrics

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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

// A labeled gauge that a function fills at each scrape: the series sorted by
// label values, escaped, read anew each time, with no series when the function
// gives none, and with a malformed series left out. An Init on a histogram or a
// counter shows a series before its first event.
func TestGaugeSamples(t *testing.T) {
	r := NewRegistry()
	rows := []Sample{
		{[]string{"b", "x"}, 2},
		{[]string{"a", `q"\`}, 0.5},
		{[]string{"a", "x"}, 1},
		{[]string{"only one value"}, 9},
	}
	r.GaugeSamples("t_keys", "Keys.", []string{"limit", "kind"}, func() []Sample { return rows })
	want := "# HELP t_keys Keys.\n# TYPE t_keys gauge\n" +
		`t_keys{limit="a",kind="q\"\\"} 0.5` + "\n" +
		`t_keys{limit="a",kind="x"} 1` + "\n" +
		`t_keys{limit="b",kind="x"} 2` + "\n"
	if got := written(t, r); got != want {
		t.Errorf("output:\n%s\nwant:\n%s", got, want)
	}
	rows = nil
	if got := written(t, r); got != "# HELP t_keys Keys.\n# TYPE t_keys gauge\n" {
		t.Errorf("output with no series = %q", got)
	}

	// The function owns the slice that it returns: a write sorts a copy.
	unsorted := []Sample{{[]string{"b"}, 1}, {[]string{"a"}, 2}}
	r.GaugeSamples("t_own", "Own.", []string{"x"}, func() []Sample { return unsorted })
	written(t, r)
	if unsorted[0].Values[0] != "b" {
		t.Error("a write sorted the slice that the function returned")
	}

	h := r.HistogramVec("t_fill", "Fill.", []float64{1}, "limit")
	h.Init("idle")
	c := r.CounterVec("t_adds_total", "Adds.", "limit")
	c.Add(3, "x")
	c.Add(2, "x")
	out := written(t, r)
	for _, w := range []string{
		`t_fill_bucket{limit="idle",le="1"} 0`, `t_fill_count{limit="idle"} 0`, `t_adds_total{limit="x"} 5`,
	} {
		if !strings.Contains(out, w) {
			t.Errorf("output lacks %q:\n%s", w, out)
		}
	}
}

// A group of gauges comes from one call per write, in the order of the names,
// whatever order the specs come in. A second group with the same lowest name
// replaces the first.
func TestGaugeGroup(t *testing.T) {
	r := NewRegistry()
	calls := 0
	group := func(b float64) {
		r.GaugeGroup([]string{"limit"}, []GaugeSpec{{"t_b", "B."}, {"t_a", "A."}}, func() [][]Sample {
			calls++
			// Rows follow the order of the specs: b, then a.
			return [][]Sample{{{[]string{"y"}, b}, {[]string{"x"}, b}}, {{[]string{"x"}, 1}}}
		})
	}
	group(2)
	want := "# HELP t_a A.\n# TYPE t_a gauge\nt_a{limit=\"x\"} 1\n" +
		"# HELP t_b B.\n# TYPE t_b gauge\nt_b{limit=\"x\"} 2\nt_b{limit=\"y\"} 2\n"
	if got := written(t, r); got != want || calls != 1 {
		t.Errorf("output:\n%s\ncalls = %d, want 1; want:\n%s", got, calls, want)
	}
	group(3)
	if got := written(t, r); !strings.Contains(got, `t_b{limit="x"} 3`) || strings.Count(got, "# TYPE t_a gauge") != 1 {
		t.Errorf("a second group did not replace the first:\n%s", got)
	}
}

// A plain family under the name of any member of a group is a type clash, so
// neither a duplicate family nor a silent replacement of the group. The same
// group registered again replaces the first, and TYPE still says gauge.
func TestGaugeGroupNamesAreHeld(t *testing.T) {
	group := func(r *Registry) {
		r.GaugeGroup([]string{"l"}, []GaugeSpec{{"t_b", "B."}, {"t_a", "A."}, {"t_c", "C."}},
			func() [][]Sample { return [][]Sample{{{[]string{"x"}, 1}}, {{[]string{"x"}, 2}}, {{[]string{"x"}, 3}}} })
	}
	panics := func(name string, f func(r *Registry)) {
		t.Helper()
		r := NewRegistry()
		group(r)
		defer func() {
			if recover() == nil {
				t.Errorf("%s did not panic", name)
			}
		}()
		f(r)
	}
	for _, n := range []string{"t_a", "t_b", "t_c"} {
		panics("Gauge "+n, func(r *Registry) { r.Gauge(n, "Plain.") })
		panics("GaugeFunc "+n, func(r *Registry) { r.GaugeFunc(n, "Plain.", func() (float64, bool) { return 1, true }) })
	}
	panics("a group on a name of another group", func(r *Registry) {
		r.GaugeGroup([]string{"l"}, []GaugeSpec{{"t_z", "Z."}, {"t_c", "C."}}, func() [][]Sample { return nil })
	})

	r := NewRegistry()
	group(r)
	group(r)
	out := written(t, r)
	for _, n := range []string{"t_a", "t_b", "t_c"} {
		if c := strings.Count(out, "# TYPE "+n+" gauge\n"); c != 1 {
			t.Errorf("%s has %d TYPE lines, want 1:\n%s", n, c, out)
		}
	}
	if !strings.Contains(out, `t_c{l="x"} 3`) {
		t.Errorf("the group lost a series:\n%s", out)
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

func TestRegisterOtherTypePanics(t *testing.T) {
	r := NewRegistry()
	r.Gauge("t_x", "A gauge.")
	defer func() {
		if recover() == nil {
			t.Error("a counter under the name of a gauge did not panic")
		}
	}()
	r.CounterVec("t_x", "A counter.")
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
	// With no interval, each call runs f. A failure after a success leaves the
	// gauge out, and the next success brings it back.
	n := 0
	g := Cached("t", 0, time.Second, func(context.Context) (float64, error) {
		n++
		if fail {
			return 0, errors.New("down")
		}
		return float64(n), nil
	})
	if v, ok := g(); !ok || v != 1 {
		t.Fatalf("first = %v, %v", v, ok)
	}
	fail = true
	if _, ok := g(); ok {
		t.Error("a failed run after a success gave a sample")
	}
	fail = false
	if v, ok := g(); !ok || v != 3 {
		t.Errorf("after the failure = %v, %v; want 3, true", v, ok)
	}
}

// A scrape during Observe calls never shows a bucket above the count, and the
// buckets never go down from one bound to the next.
func TestHistogramScrapeDuringObserve(t *testing.T) {
	r := NewRegistry()
	h := r.HistogramVec("t_seconds", "T.", []float64{0.1, 1}, "route")
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
				h.Observe(float64(i%3), "/a")
			}
		}
	}()
	for range 200 {
		var prev float64 = -1
		for _, line := range strings.Split(written(t, r), "\n") {
			if !strings.HasPrefix(line, "t_seconds_bucket") {
				continue
			}
			v, err := strconv.ParseFloat(line[strings.LastIndexByte(line, ' ')+1:], 64)
			if err != nil {
				t.Fatal(err)
			}
			if v < prev {
				t.Fatalf("a bucket went down: %q after %v", line, prev)
			}
			prev = v
		}
	}
	close(stop)
	<-done
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
		"# TYPE gobb_rate_limit_fill_ratio histogram", "# TYPE gobb_rate_limit_allowed_total counter",
		"# TYPE gobb_rate_limit_refunds_total counter", "# TYPE gobb_rate_limit_evictions_total counter",
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
