package main

import (
	"bytes"
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeBoard is a small stand-in for gobb. The index links forum 1 and topic 1,
// forum 1 links topics 1 and 2, and /t/{n} answers 404 for any other topic or
// any page number. Search answers 429 after the first few. A sign-in as
// member/pw answers 303 with a session; any other answers 200 (a wrong
// password). Each hook may change one answer.
type fakeBoard struct {
	// topic, when set, answers a view of /t/1 or /t/2 in place of 200. A 429
	// or 503 carries Retry-After unless noRetry is set.
	topic   func(member bool) int
	noRetry bool
	// hang, when set, makes a member view of /t/1 wait this long.
	hang time.Duration
	// searchDelay delays every 429 of search.
	searchDelay time.Duration
	// drop closes the connection of every guest view of /f/1, with no answer.
	drop bool
	// loadStarted and loadEnded mark the phases. topic applies during the load
	// only; afterTopic, when set, answers /t/1 after it.
	loadStarted, loadEnded atomic.Bool
	afterTopic             int

	searches atomic.Int64
	signins  atomic.Int64
	sawXFF   atomic.Bool
}

func (b *fakeBoard) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	member := false
	if c, err := r.Cookie("gobb_session"); err == nil && c.Value == "sess" {
		member = true
	}
	page := r.URL.Query().Get("page")
	switch {
	case r.URL.Path == "/":
		fmt.Fprint(w, `<a href="/f/1">F</a> <a href="/t/1#p1">T</a> <a href="https://elsewhere.example/t/9">X</a>`)
	case r.URL.Path == "/f/1" && page == "":
		if b.drop && !member && b.loadStarted.Load() {
			hj, ok := w.(http.Hijacker)
			if !ok {
				panic("no hijacker")
			}
			conn, _, _ := hj.Hijack()
			conn.Close()
			return
		}
		fmt.Fprint(w, `<a href="/t/1">a</a> <a href="/t/2?page=2">b</a>`)
	case (r.URL.Path == "/t/1" || r.URL.Path == "/t/2") && page == "":
		code := http.StatusOK
		if b.loadEnded.Load() && r.URL.Path == "/t/1" && b.afterTopic != 0 {
			code = b.afterTopic
		} else if b.topic != nil && b.loadStarted.Load() && !b.loadEnded.Load() {
			code = b.topic(member)
		}
		if member && b.hang > 0 && r.URL.Path == "/t/1" && b.loadStarted.Load() {
			time.Sleep(b.hang)
		}
		if (code == http.StatusTooManyRequests || code == http.StatusServiceUnavailable) && !b.noRetry {
			w.Header().Set("Retry-After", "2")
		}
		w.WriteHeader(code)
	case strings.HasPrefix(r.URL.Path, "/t/") || strings.HasPrefix(r.URL.Path, "/f/"):
		http.NotFound(w, r)
	case r.URL.Path == "/search":
		if b.searches.Add(1) > 3 {
			time.Sleep(b.searchDelay)
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		}
	case r.URL.Path == "/healthz":
		fmt.Fprint(w, "ok")
	case r.URL.Path == "/login" && r.Method == http.MethodGet:
		http.SetCookie(w, &http.Cookie{Name: "gobb_csrf", Value: "secret"})
		fmt.Fprint(w, `<form><input type="hidden" name="csrf" value="tok"></form>`)
	case r.URL.Path == "/login" && r.Method == http.MethodPost:
		c, err := r.Cookie("gobb_csrf")
		if err != nil || c.Value != "secret" || r.FormValue("csrf") != "tok" {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		if r.FormValue("name") == "member" && r.FormValue("password") == "pw" {
			http.SetCookie(w, &http.Cookie{Name: "gobb_session", Value: "sess"})
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		b.signins.Add(1)
		if strings.HasPrefix(r.Header.Get("X-Forwarded-For"), "2001:db8:") {
			b.sawXFF.Store(true)
		}
	default:
		http.NotFound(w, r)
	}
}

// testConfig is a short run against base with a member.
func testConfig(t *testing.T, base string) config {
	t.Helper()
	cfg, err := parseFlags([]string{
		"-duration", "300ms", "-c", "4", "-pause", "0", "-seed", "7",
		"-member-name", "member", "-member-password", "pw", base,
	}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// runAgainst runs the command against b and returns the exit status and the
// report.
func runAgainst(t *testing.T, b *fakeBoard, edit func(*config)) (int, string) {
	t.Helper()
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	cfg := testConfig(t, srv.URL)
	if edit != nil {
		edit(&cfg)
	}
	var out bytes.Buffer
	code := runWithHooks(context.Background(), cfg, &out, hooks{
		started: func() { b.loadStarted.Store(true) },
		ended:   func() { b.loadEnded.Store(true) },
	})
	return code, out.String()
}

func TestHealthyBoardPasses(t *testing.T) {
	b := &fakeBoard{}
	code, out := runAgainst(t, b, nil)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	for _, want := range []string{
		"targets: 4 guest pages (1 forums, 2 topics), 4 member pages",
		"\npage ", "\nmiss ", "\nsearch ", "\nsignin ", "\nmember ", "\nmsearch ",
		"recovery: 4 guest and 4 member views, /login and /healthz, 0 failed",
		"ok: every answer was allowed",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if b.signins.Load() == 0 || !b.sawXFF.Load() {
		t.Errorf("signins %d, saw an IPv6 X-Forwarded-For %v", b.signins.Load(), b.sawXFF.Load())
	}
}

func TestStatusOutsideTheSetFails(t *testing.T) {
	b := &fakeBoard{topic: func(bool) int { return http.StatusInternalServerError }}
	code, out := runAgainst(t, b, nil)
	if code != 1 || !strings.Contains(out, "status 500 is not allowed") {
		t.Fatalf("exit %d, want 1 and a status 500 line:\n%s", code, out)
	}
}

// A 404 is allowed for a miss only: a page that answered 200 at set-up and
// then 404 is a failure.
func TestNotFoundOnARealPageFails(t *testing.T) {
	b := &fakeBoard{topic: func(bool) int { return http.StatusNotFound }}
	code, out := runAgainst(t, b, nil)
	if code != 1 || !strings.Contains(out, "status 404 is not allowed") {
		t.Fatalf("exit %d, want 1 and a status 404 line:\n%s", code, out)
	}
	for line := range strings.Lines(out) {
		if strings.HasPrefix(strings.TrimSpace(line), "miss ") && strings.Contains(line, "not allowed") {
			t.Errorf("a miss was refused for its 404: %s", line)
		}
	}
}

// Quick 429s and 503s with Retry-After pass for the classes that may be
// refused: the refusals are the load protection at work.
func TestQuickRefusalsPass(t *testing.T) {
	b := &fakeBoard{topic: func(member bool) int {
		if member {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	code, out := runAgainst(t, b, nil)
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
	if !strings.Contains(out, "503 x") || !strings.Contains(out, "429 x") {
		t.Errorf("the report lacks the 429 and 503 counts:\n%s", out)
	}
}

// A guest page that the board caches must never be refused: a 503 or 429 on
// the page class fails.
func TestRefusedGuestPageFails(t *testing.T) {
	for _, status := range []int{http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		b := &fakeBoard{topic: func(member bool) int {
			if member {
				return http.StatusOK
			}
			return status
		}}
		code, out := runAgainst(t, b, nil)
		if code != 1 || !strings.Contains(out, fmt.Sprintf("page /t/1: status %d is not allowed", status)) {
			t.Errorf("guest page %d: exit %d, want 1 and a page line:\n%s", status, code, out)
		}
	}
}

// A refusal with no Retry-After fails.
func TestRefusalWithoutRetryAfterFails(t *testing.T) {
	b := &fakeBoard{noRetry: true, topic: func(member bool) int {
		if member {
			return http.StatusServiceUnavailable
		}
		return http.StatusOK
	}}
	code, out := runAgainst(t, b, nil)
	if code != 1 || !strings.Contains(out, "has no Retry-After") {
		t.Fatalf("exit %d, want 1 and a Retry-After line:\n%s", code, out)
	}
}

// A request that the end of the load cuts off counts as an error when it had
// already run longer than the refusal bound.
func TestStuckRequestAtTheEndFails(t *testing.T) {
	b := &fakeBoard{hang: 2 * time.Second}
	code, out := runAgainst(t, b, func(c *config) { c.refuseBound = 100 * time.Millisecond })
	if code != 1 || !strings.Contains(out, "cut off at the end of the load") {
		t.Fatalf("exit %d, want 1 and a cut-off line:\n%s", code, out)
	}
}

func TestSlowRefusalFails(t *testing.T) {
	b := &fakeBoard{searchDelay: 60 * time.Millisecond}
	code, out := runAgainst(t, b, func(c *config) { c.refuseBound = 20 * time.Millisecond })
	if code != 1 || !strings.Contains(out, "refusals (429 or 503) slower than the bound") {
		t.Fatalf("exit %d, want 1 and a slow refusal:\n%s", code, out)
	}
	if !strings.Contains(out, "status 429 took") {
		t.Errorf("the report lacks an example of the slow 429:\n%s", out)
	}
}

// A slow 200 is not a refusal, so the bound does not apply to it. The views
// are slow only in the first 200 ms of the 300 ms load, so none is still
// running when the load ends (that one would count as stuck).
func TestSlowSuccessPasses(t *testing.T) {
	var (
		once  sync.Once
		start time.Time
	)
	b := &fakeBoard{topic: func(bool) int {
		once.Do(func() { start = time.Now() })
		if time.Since(start) < 200*time.Millisecond {
			time.Sleep(40 * time.Millisecond)
		}
		return http.StatusOK
	}}
	code, out := runAgainst(t, b, func(c *config) { c.refuseBound = 10 * time.Millisecond })
	if code != 0 {
		t.Fatalf("exit %d, want 0:\n%s", code, out)
	}
}

func TestDroppedConnectionFails(t *testing.T) {
	// The drop starts with the load: set-up reads /f/1 as a guest.
	code, out := runAgainst(t, &fakeBoard{drop: true}, nil)
	if code != 1 || !strings.Contains(out, "requests with no answer") {
		t.Fatalf("exit %d, want 1 and a connection error:\n%s", code, out)
	}
}

func TestTimeoutFails(t *testing.T) {
	b := &fakeBoard{topic: func(bool) int { time.Sleep(200 * time.Millisecond); return http.StatusOK }}
	code, out := runAgainst(t, b, func(c *config) { c.timeout = 50 * time.Millisecond })
	if code != 1 || !strings.Contains(out, "requests with no answer") {
		t.Fatalf("exit %d, want 1 and a timeout:\n%s", code, out)
	}
}

// The board answers well during the load and 503 after it: the recovery check
// must catch that.
func TestNoRecoveryFails(t *testing.T) {
	b := &fakeBoard{afterTopic: http.StatusServiceUnavailable}
	code, out := runAgainst(t, b, nil)
	if code != 1 {
		t.Fatalf("exit %d, want 1:\n%s", code, out)
	}
	for _, want := range []string{"recovery guest /t/1: status 503, want 200", "recovery member /t/1: status 503", "recovery views did not answer 200"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "not allowed") || strings.Contains(out, "slower than") {
		t.Errorf("the load itself failed:\n%s", out)
	}
}

func TestForbiddenGuestSearch(t *testing.T) {
	// /search answers 403 to a guest; the flag allows it for guest searches only.
	mux := http.NewServeMux()
	b := &fakeBoard{}
	mux.HandleFunc("/search", func(w http.ResponseWriter, r *http.Request) {
		if _, err := r.Cookie("gobb_session"); err != nil {
			w.WriteHeader(http.StatusForbidden)
		}
	})
	mux.Handle("/", b)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	for _, forbidden := range []bool{false, true} {
		cfg := testConfig(t, srv.URL)
		cfg.searchForbidden = forbidden
		var out bytes.Buffer
		code := run(context.Background(), cfg, &out)
		want := 1
		if forbidden {
			want = 0
		}
		if code != want {
			t.Errorf("-search-forbidden=%v: exit %d, want %d:\n%s", forbidden, code, want, out.String())
		}
	}
}

func TestSetupFailures(t *testing.T) {
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			fmt.Fprint(w, "no links")
			return
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(empty.Close)
	var out bytes.Buffer
	if code := run(context.Background(), testConfig(t, empty.URL), &out); code != 2 || !strings.Contains(out.String(), "sees no topic") {
		t.Errorf("an empty board: exit %d, want 2:\n%s", code, out.String())
	}

	down := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(down.Close)
	out.Reset()
	if code := run(context.Background(), testConfig(t, down.URL), &out); code != 2 || !strings.Contains(out.String(), "the index answered 503") {
		t.Errorf("a board that answers 503: exit %d, want 2:\n%s", code, out.String())
	}

	b := &fakeBoard{}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	cfg := testConfig(t, srv.URL)
	cfg.memberPassword = "wrong"
	out.Reset()
	if code := run(context.Background(), cfg, &out); code != 2 || !strings.Contains(out.String(), "member sign-in answered 200, want 303") {
		t.Errorf("a wrong member password: exit %d, want 2:\n%s", code, out.String())
	}
}

func TestParseFlags(t *testing.T) {
	ok := func(args ...string) config {
		t.Helper()
		cfg, err := parseFlags(args, &bytes.Buffer{})
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		return cfg
	}
	cfg := ok("http://127.0.0.1:8080")
	if cfg.base.String() != "http://127.0.0.1:8080/" || cfg.duration != 60*time.Second || cfg.refuseBound != 2*time.Second {
		t.Errorf("defaults: %s %s %s", cfg.base, cfg.duration, cfg.refuseBound)
	}
	if got := ok("http://localhost:8080/board").base.Path; got != "/board/" {
		t.Errorf("base path %q, want /board/", got)
	}
	ok("-any-host", "http://192.0.2.1/")
	ok("http://[::1]:8080/")

	for _, args := range [][]string{
		{},
		{"http://192.0.2.1/"},
		{"ftp://127.0.0.1/"},
		{"-c", "0", "http://127.0.0.1/"},
		{"-duration", "0s", "http://127.0.0.1/"},
		{"-mix", "page=0", "http://127.0.0.1/"},
		{"-mix", "page=1,nope=1", "http://127.0.0.1/"},
		{"-mix", "page=1,page=2", "http://127.0.0.1/"},
		{"-mix", "page=-1", "http://127.0.0.1/"},
		{"-member-name", "a", "http://127.0.0.1/"},
		{"-member-name", "a", "-member-password", "b", "-member-session", "c", "http://127.0.0.1/"},
		{"http://127.0.0.1/", "extra"},
	} {
		if _, err := parseFlags(args, &bytes.Buffer{}); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

func TestMemberClassesNeedAMember(t *testing.T) {
	b := &fakeBoard{}
	srv := httptest.NewServer(b)
	t.Cleanup(srv.Close)
	cfg, err := parseFlags([]string{"-duration", "100ms", "-c", "2", "-pause", "0", "-mix", "member=1", srv.URL}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if code := run(context.Background(), cfg, &out); code != 2 || !strings.Contains(out.String(), "no class to run") {
		t.Errorf("exit %d, want 2:\n%s", code, out.String())
	}
}

func TestPercentile(t *testing.T) {
	var d []time.Duration
	for i := 1; i <= 100; i++ {
		d = append(d, time.Duration(i)*time.Millisecond)
	}
	for p, want := range map[int]time.Duration{50: 50 * time.Millisecond, 95: 95 * time.Millisecond, 99: 99 * time.Millisecond, 100: 100 * time.Millisecond} {
		if got := percentile(d, p); got != want {
			t.Errorf("p%d = %s, want %s", p, got, want)
		}
	}
	if got := percentile([]time.Duration{7}, 99); got != 7 {
		t.Errorf("one value: %s", got)
	}
	if got := percentile(nil, 50); got != 0 {
		t.Errorf("no value: %s", got)
	}
}

func TestMissPathsDoNotExist(t *testing.T) {
	tt := &tester{forums: []int{1}, topics: []int{1, 2}, maxTopic: 2}
	srv := httptest.NewServer(&fakeBoard{})
	t.Cleanup(srv.Close)
	r := rand.New(rand.NewPCG(1, 2))
	for range 200 {
		p := tt.missPath(r)
		u, err := url.Parse(p)
		if err != nil {
			t.Fatal(err)
		}
		if page := u.Query().Get("page"); page != "" {
			// gobb answers 400 to a page number over 1,000,000.
			if n, err := strconv.Atoi(page); err != nil || n < 1000 || n > 1_000_000 {
				t.Errorf("%s: want a page number from 1000 to 1000000", p)
			}
		}
		resp, err := http.Get(srv.URL + "/" + p)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", p, resp.StatusCode)
		}
	}
}
