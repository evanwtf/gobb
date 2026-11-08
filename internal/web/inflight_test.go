package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/metrics"
)

// A full in-flight limit answers 503 at once, with Retry-After and the busy
// page, and does not wait for a slot. /healthz and the static files skip it.
// A freed slot lets the next request in.
func TestInFlightLimitRefusesAtOnce(t *testing.T) {
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _ := siteAndPool(t, bundletest.Small(), Options{MaxInFlight: 2})
	onNew = nil
	if cap(s.inFlight) != 2 {
		t.Fatalf("the limit holds %d slots, want 2", cap(s.inFlight))
	}
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ with free slots = %d", rec.Code)
	}
	if n := len(s.inFlight); n != 0 {
		t.Fatalf("%d slots held after a request ended, want 0", n)
	}

	// Take both slots, as two requests under way would.
	s.inFlight <- struct{}{}
	s.inFlight <- struct{}{}
	before := metrics.InFlightRefusals.Value()
	start := time.Now()
	rec := get(t, h, "/t/1")
	took := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "2" {
		t.Fatalf("full limit = %d, Retry-After %q, want 503 and 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	if took > 200*time.Millisecond {
		t.Errorf("the refusal took %s; it must not wait", took)
	}
	if body := bodyOf(t, rec); !strings.Contains(body, "<html") {
		t.Errorf("the refusal is not the busy page:\n%s", body)
	}
	if got := metrics.InFlightRefusals.Value() - before; got != 1 {
		t.Errorf("gobb_in_flight_refusals_total rose by %d, want 1", got)
	}
	for _, path := range []string{"/healthz", "/static/gobb.css", "/favicon.ico"} {
		if rec := get(t, h, path); rec.Code != http.StatusOK {
			t.Errorf("%s with the limit full = %d, want 200", path, rec.Code)
		}
	}
	if n := len(s.inFlight); n != 2 {
		t.Errorf("%d slots held, want the 2 taken by the test", n)
	}

	<-s.inFlight
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK {
		t.Errorf("/t/1 after a slot freed = %d, want 200", rec.Code)
	}
	<-s.inFlight
	if n := len(s.inFlight); n != 0 {
		t.Errorf("%d slots held at the end, want 0", n)
	}
}

// A handler that panics gives its slot back.
func TestInFlightSlotFreedAfterPanic(t *testing.T) {
	slots := make(chan struct{}, 1)
	h := limitInFlight(slots, "/", refuse503, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	for i := range 3 {
		func() {
			defer func() {
				if p := recover(); p != "boom" {
					t.Errorf("try %d: recovered %v, want the handler's panic", i, p)
				}
			}()
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/t/1", nil))
		}()
		if n := len(slots); n != 0 {
			t.Fatalf("try %d: %d slots held after a panic, want 0", i, n)
		}
	}
}

// Concurrent requests never hold more slots than the limit, and each gets
// either the handler or a refusal.
func TestInFlightLimitHoldsUnderConcurrency(t *testing.T) {
	slots := make(chan struct{}, 3)
	release := make(chan struct{})
	entered := make(chan struct{}, 10)
	h := limitInFlight(slots, "/", refuse503, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		entered <- struct{}{}
		<-release
	}))
	codes := make(chan int, 10)
	for range 10 {
		go func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/f/1", nil))
			codes <- rec.Code
		}()
	}
	// Three get in; seven are refused without waiting for the three.
	refused := 0
	for range 7 {
		select {
		case c := <-codes:
			if c != http.StatusServiceUnavailable {
				t.Errorf("a request past the limit = %d, want 503", c)
			}
			refused++
		case <-time.After(5 * time.Second):
			t.Fatal("a refusal waited for a slot")
		}
	}
	for deadline := time.Now().Add(5 * time.Second); len(entered) < 3 && time.Now().Before(deadline); {
		time.Sleep(time.Millisecond)
	}
	if refused != 7 || len(entered) != 3 || len(slots) != 3 {
		t.Errorf("refused %d, entered %d, slots %d, want 7, 3 and 3", refused, len(entered), len(slots))
	}
	close(release)
	for range 3 {
		if c := <-codes; c != http.StatusOK {
			t.Errorf("an admitted request = %d", c)
		}
	}
	if len(slots) != 0 {
		t.Errorf("%d slots held at the end", len(slots))
	}
}

// refuse503 stands in for the busy page.
func refuse503(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
