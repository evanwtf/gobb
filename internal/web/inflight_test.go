package web

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
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
	if cap(s.inFlight.all) != 2 || cap(s.inFlight.signin) != 2 || s.login.Slots != s.inFlight.signin {
		t.Fatalf("the limits hold %d and %d slots, want 2 and 2 (the sign-in default of 32, cut to the main limit)",
			cap(s.inFlight.all), cap(s.inFlight.signin))
	}
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Fatalf("/ with free slots = %d", rec.Code)
	}
	if n := len(s.inFlight.all); n != 0 {
		t.Fatalf("%d slots held after a request ended, want 0", n)
	}

	// Take both slots, as two requests under way would.
	s.inFlight.all <- struct{}{}
	s.inFlight.all <- struct{}{}
	before := metrics.InFlightRefusals.Value("all")
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
	if got := metrics.InFlightRefusals.Value("all") - before; got != 1 {
		t.Errorf("gobb_in_flight_refusals_total rose by %d, want 1", got)
	}
	for _, path := range []string{"/healthz", "/static/gobb.css", "/favicon.ico"} {
		if rec := get(t, h, path); rec.Code != http.StatusOK {
			t.Errorf("%s with the limit full = %d, want 200", path, rec.Code)
		}
	}
	// Only GET or HEAD of a known static file skips the limit: a miss renders
	// the 404 page, which runs queries, and so does any other method.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/static/nope.css"},
		{http.MethodPost, "/static/gobb.css"},
		{http.MethodPost, "/favicon.ico"},
		{http.MethodPost, "/healthz"},
		{http.MethodHead, "/healthz"},
		{http.MethodGet, "/static/gobb%2Ecss"},
	} {
		if rec := do(t, h, c.method, c.path); rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s with the limit full = %d, want 503", c.method, c.path, rec.Code)
		}
	}
	if n := len(s.inFlight.all); n != 2 {
		t.Errorf("%d slots held, want the 2 taken by the test", n)
	}

	<-s.inFlight.all
	if rec := get(t, h, "/t/1"); rec.Code != http.StatusOK {
		t.Errorf("/t/1 after a slot freed = %d, want 200", rec.Code)
	}
	<-s.inFlight.all
	if n := len(s.inFlight.all); n != 0 {
		t.Errorf("%d slots held at the end, want 0", n)
	}
}

// A handler that panics gives its slot back.
func TestInFlightSlotFreedAfterPanic(t *testing.T) {
	l := inFlightLimits{all: make(chan struct{}, 1), signin: make(chan struct{}, 1)}
	h := limitInFlight(l, "/", refuse503, nil, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))
	for i := range 4 {
		func() {
			defer func() {
				if p := recover(); p != "boom" {
					t.Errorf("try %d: recovered %v, want the handler's panic", i, p)
				}
			}()
			req := httptest.NewRequest(http.MethodGet, "/t/1", nil)
			if i%2 == 1 {
				req = httptest.NewRequest(http.MethodPost, "/t/1/reply", nil)
			}
			h.ServeHTTP(httptest.NewRecorder(), req)
		}()
		if a := len(l.all); a != 0 {
			t.Fatalf("try %d: %d slots held after a panic, want 0", i, a)
		}
	}
}

// Concurrent requests never hold more slots than the limit, and each gets
// either the handler or a refusal.
func TestInFlightLimitHoldsUnderConcurrency(t *testing.T) {
	slots := make(chan struct{}, 3)
	release := make(chan struct{})
	entered := make(chan struct{}, 10)
	h := limitInFlight(inFlightLimits{all: slots, signin: make(chan struct{}, 1)}, "/", refuse503, nil, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
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

// The password POSTs skip the main limit, and only they do.
func TestIsPasswordPost(t *testing.T) {
	for _, c := range []struct {
		method, path, base string
		want               bool
	}{
		{"POST", "/login", "/", true},
		{"POST", "/account/password", "/", true},
		{"POST", "/account/email", "/", true},
		{"POST", "/admin/confirm", "/", true},
		{"POST", "/reset/tok123", "/", true},
		{"POST", "/forum/login", "/forum/", true},
		{"POST", "/forum/reset/tok", "/forum/", true},
		{"POST", "/log%69n", "/", false}, // escaped: takes a main slot
		{"GET", "/login", "/", false},
		{"GET", "/reset/tok", "/", false},
		{"POST", "/reset", "/", false},
		{"POST", "/reset/", "/", false},
		{"POST", "/reset/a/b", "/", false},
		{"POST", "/logout", "/", false},
		{"POST", "/account/about", "/", false},
		{"POST", "/login", "/forum/", false},
		{"POST", "/t/1/reply", "/", false},
	} {
		r := httptest.NewRequest(c.method, c.path, nil)
		if got := isPasswordPost(r, c.base); got != c.want {
			t.Errorf("%s %s (base %s) = %v, want %v", c.method, c.path, c.base, got, c.want)
		}
	}
}

// signinSite is a site with a sign-in limit of 1 and its *site.
func signinSite(t *testing.T) (http.Handler, *site) {
	t.Helper()
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, _ := siteAndPool(t, bundletest.Small(), Options{MaxInFlight: 8, MaxSigninInFlight: 1})
	onNew = nil
	if s.login.Slots == nil || cap(s.login.Slots) != 1 {
		t.Fatal("the login service has no sign-in slots of 1")
	}
	return h, s
}

// A sign-in that finds its slots full gets 503 with the busy answer, after the
// floor and without a wait for the gate, and counts. Page views stay admitted
// while the sign-in slots are full.
func TestSigninPastItsLimit(t *testing.T) {
	h, s := signinSite(t)
	s.login.Slots <- struct{}{}
	defer func() { <-s.login.Slots }()
	before := metrics.InFlightRefusals.Value("signin")
	start := time.Now()
	rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong"}})
	took := time.Since(start)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "5" {
		t.Errorf("sign-in past its limit = %d, Retry-After %q; want 503 and 5", rec.Code, rec.Header().Get("Retry-After"))
	}
	if took > 300*time.Millisecond {
		t.Errorf("the refusal took %s; it must not wait for a slot or the gate", took)
	}
	if got := metrics.InFlightRefusals.Value("signin") - before; got != 1 {
		t.Errorf("signin refusals rose by %d, want 1", got)
	}
	for _, target := range []string{"/", "/t/1", "/login"} {
		if rec := get(t, h, target); rec.Code != http.StatusOK {
			t.Errorf("GET %s with the sign-in slots full = %d, want 200", target, rec.Code)
		}
	}
}

// A sign-in whose body has not all come holds no slot: the slot is taken only
// around the password work, after the form is read. With a limit of one, a
// stalled POST /login does not lock out the next sign-in.
func TestSlowSigninBodyHoldsNoSlot(t *testing.T) {
	h, s := signinSite(t)
	pr, pw := io.Pipe()
	req := httptest.NewRequest(http.MethodPost, "/login", pr)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.ContentLength = 1000
	done := make(chan int, 1)
	go func() {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		done <- rec.Code
	}()
	if _, err := pw.Write([]byte("name=alice&pass")); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	if n, m := len(s.login.Slots), len(s.inFlight.all); n != 0 || m != 0 {
		t.Errorf("a stalled body holds %d sign-in and %d main slots, want 0 and 0", n, m)
	}
	rec, _ := postAs(t, h, "/login", nil, url.Values{"name": {"alice"}, "password": {"wrong"}})
	if rec.Code != http.StatusOK {
		t.Errorf("a sign-in beside a stalled one = %d, want 200 (a wrong password)", rec.Code)
	}
	pw.CloseWithError(errors.New("client gone"))
	<-done
	if n := len(s.login.Slots); n != 0 {
		t.Errorf("%d sign-in slots held at the end", n)
	}
}

// refuse503 stands in for the busy page.
func refuse503(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }
