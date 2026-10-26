package web

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/evanwtf/gobb/internal/metrics"
)

// The request counter and the duration histogram take the route pattern of the
// handler that answered, never the path, and a bounded method.
func TestMeteredCountsByPattern(t *testing.T) {
	h, _ := smallSite(t)
	type key struct{ route, method, code string }
	counts := func() map[key]uint64 {
		m := map[key]uint64{}
		for _, k := range []key{
			{"/{$}", "GET", "2xx"}, {"/t/{id}", "GET", "4xx"}, {"/", "other", "4xx"}, {"/healthz", "GET", "2xx"},
		} {
			m[k] = metrics.HTTPRequests.Value(k.route, k.method, k.code)
		}
		return m
	}
	before := counts()
	durations := metrics.HTTPDuration.Count("/t/{id}")
	get(t, h, "/")
	get(t, h, "/t/999999")
	get(t, h, "/t/888888")
	if rec := do(t, h, "PUT", "/t/1"); rec.Code < 400 || rec.Code > 499 {
		t.Fatalf("PUT /t/1 = %d, want a 4xx from the fallback", rec.Code)
	}
	get(t, h, "/healthz")
	after := counts()
	for k, want := range map[key]uint64{
		{"/{$}", "GET", "2xx"}: 1, {"/t/{id}", "GET", "4xx"}: 2, {"/", "other", "4xx"}: 1, {"/healthz", "GET", "2xx"}: 1,
	} {
		if got := after[k] - before[k]; got != want {
			t.Errorf("%+v: +%d, want +%d", k, got, want)
		}
	}
	if got := metrics.HTTPDuration.Count("/t/{id}") - durations; got != 2 {
		t.Errorf("durations of /t/{id}: +%d, want +2", got)
	}
}

// A handler outside every mux counts as route "none". A handler that writes
// nothing counts as 200. A panic counts as a 500 and goes on.
func TestMeteredEdges(t *testing.T) {
	quiet := metered(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	before := metrics.HTTPRequests.Value("none", "GET", "2xx")
	quiet.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if got := metrics.HTTPRequests.Value("none", "GET", "2xx"); got != before+1 {
		t.Errorf("none/GET/2xx = %d, want %d", got, before+1)
	}

	boom := metered(withRoute("POST /boom", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { panic("boom") })))
	before = metrics.HTTPRequests.Value("/boom", "POST", "5xx")
	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic did not go on")
			}
		}()
		boom.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/boom", nil))
	}()
	if got := metrics.HTTPRequests.Value("/boom", "POST", "5xx"); got != before+1 {
		t.Errorf("/boom/POST/5xx = %d, want %d", got, before+1)
	}

	// A panic after the status went out counts with that status.
	late := metered(withRoute("GET /late", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		panic(http.ErrAbortHandler)
	})))
	before = metrics.HTTPRequests.Value("/late", "GET", "2xx")
	func() {
		defer func() { _ = recover() }()
		late.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/late", nil))
	}()
	if got := metrics.HTTPRequests.Value("/late", "GET", "2xx"); got != before+1 {
		t.Errorf("/late/GET/2xx = %d, want %d", got, before+1)
	}

	// The first final status wins; a 1xx does not count.
	two := metered(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusEarlyHints)
		w.WriteHeader(http.StatusTeapot)
		w.WriteHeader(http.StatusOK)
	}))
	before = metrics.HTTPRequests.Value("none", "GET", "4xx")
	two.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if got := metrics.HTTPRequests.Value("none", "GET", "4xx"); got != before+1 {
		t.Errorf("none/GET/4xx = %d, want %d", got, before+1)
	}
}
