package main

import (
	"bytes"
	"context"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/mail/mailtest"
	"github.com/evanwtf/gobb/internal/memlimit"
)

func TestServerLimits(t *testing.T) {
	h := http.NotFoundHandler()
	srv := newHTTPServer(h)
	if srv.Handler == nil {
		t.Error("no handler")
	}
	for name, got := range map[string][2]time.Duration{
		"ReadHeaderTimeout": {srv.ReadHeaderTimeout, 10 * time.Second},
		"ReadTimeout":       {srv.ReadTimeout, 30 * time.Second},
		"WriteTimeout":      {srv.WriteTimeout, 120 * time.Second},
		"IdleTimeout":       {srv.IdleTimeout, 120 * time.Second},
	} {
		if got[0] != got[1] {
			t.Errorf("%s = %v, want %v", name, got[0], got[1])
		}
	}
	if srv.MaxHeaderBytes != 64<<10 {
		t.Errorf("MaxHeaderBytes = %d, want 65536", srv.MaxHeaderBytes)
	}
}

// listen opens a listener on a free port of the loopback address.
func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

// setWindow shortens the shutdown window for one test.
func setWindow(t *testing.T, d time.Duration) {
	t.Helper()
	old := shutdownTimeout
	shutdownTimeout = d
	t.Cleanup(func() { shutdownTimeout = old })
}

func TestServeUntilCancel(t *testing.T) {
	ln := listen(t)
	srv := newHTTPServer(http.NotFoundHandler())
	var closed atomic.Bool
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		errc <- serveUntil(ctx, srv, ln, func() { closed.Store(true) }, nil, func(int) { t.Error("exit called") })
	}()
	// The server answers while it runs.
	resp, err := http.Get("http://" + ln.Addr().String() + "/")
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	cancel()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serveUntil = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntil did not return")
	}
	if !closed.Load() {
		t.Error("closePool did not run before serveUntil returned")
	}
}

func TestServeUntilSignal(t *testing.T) {
	ln := listen(t)
	srv := newHTTPServer(http.NotFoundHandler())
	sigs := make(chan os.Signal, 2)
	var closed atomic.Bool
	errc := make(chan error, 1)
	go func() {
		errc <- serveUntil(context.Background(), srv, ln, func() { closed.Store(true) }, sigs, func(int) { t.Error("exit called") })
	}()
	sigs <- os.Interrupt
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serveUntil = %v, want nil", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntil did not return after a signal")
	}
	if !closed.Load() {
		t.Error("closePool did not run")
	}
}

func TestServeUntilStuckPool(t *testing.T) {
	setWindow(t, 100*time.Millisecond)
	ln := listen(t)
	srv := newHTTPServer(http.NotFoundHandler())
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start := time.Now()
	err := serveUntil(ctx, srv, ln, func() { <-release }, nil, func(int) {})
	if err == nil || !strings.Contains(err.Error(), "pool") {
		t.Fatalf("serveUntil = %v, want an error that names the pool", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Errorf("took %v, want about the 100 ms window", d)
	}
}

func TestServeUntilSecondSignal(t *testing.T) {
	setWindow(t, 5*time.Second)
	ln := listen(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := newHTTPServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		close(entered)
		<-release
	}))
	sigs := make(chan os.Signal, 2)
	exits := make(chan int, 2)
	errc := make(chan error, 1)
	go func() {
		errc <- serveUntil(context.Background(), srv, ln, func() {}, sigs, func(code int) { exits <- code })
	}()
	// One request stays open, so the graceful shutdown cannot finish.
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	sigs <- os.Interrupt
	select {
	case code := <-exits:
		t.Fatalf("exit(%d) after the first signal", code)
	case <-time.After(200 * time.Millisecond):
	}
	sigs <- os.Interrupt
	select {
	case code := <-exits:
		if code != 1 {
			t.Errorf("exit code = %d, want 1", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("a second signal did not call exit")
	}
	close(release)
	select {
	case <-errc:
	case <-time.After(10 * time.Second):
		t.Fatal("serveUntil did not return")
	}
}

// The README and .env.example must name every variable that serve reads, so
// that a person can run the server from the docs alone.
func TestDocsNameEveryServeVariable(t *testing.T) {
	for _, file := range []string{"../../README.md", "../../.env.example"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range []string{"GOBB_DSN", "GOBB_LISTEN", "GOBB_DATA_DIR", "GOBB_BASE_PATH", "GOBB_TRUSTED_PROXIES", "GOBB_COOKIE_SECURE", "GOBB_METRICS_LISTEN"} {
			if !strings.Contains(string(b), v) {
				t.Errorf("%s does not name %s", file, v)
			}
		}
	}
}

// A request that outlasts the window gives the shutdown error. A pool that
// closes at once must not take the blame.
func TestServeUntilSlowRequestFastPool(t *testing.T) {
	setWindow(t, 100*time.Millisecond)
	ln := listen(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := newHTTPServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveUntil(ctx, srv, ln, func() {}, nil, func(int) {}) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("the request never reached the handler")
	}
	cancel()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "shutdown") || strings.Contains(err.Error(), "pool") {
			t.Fatalf("serveUntil = %v, want the shutdown error and no pool error", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntil did not return")
	}
}

// A stuck pool after a slow request gets its own short grace, and both errors
// show.
func TestServeUntilSlowRequestStuckPool(t *testing.T) {
	setWindow(t, 100*time.Millisecond)
	old := poolGrace
	poolGrace = 100 * time.Millisecond
	t.Cleanup(func() { poolGrace = old })
	ln := listen(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	t.Cleanup(func() { close(release) })
	srv := newHTTPServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		close(entered)
		<-release
	}))
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String() + "/")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- serveUntil(ctx, srv, ln, func() { <-release }, nil, func(int) {}) }()
	<-entered
	cancel()
	select {
	case err := <-errc:
		if err == nil || !strings.Contains(err.Error(), "shutdown") || !strings.Contains(err.Error(), "pool") {
			t.Fatalf("serveUntil = %v, want both errors", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("serveUntil did not return")
	}
}

// A signal that arrives while serve connects and migrates stops it before it
// listens. The address is taken, so a serve that went on would fail to bind.
func TestServeStopsOnSignalDuringSetup(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	taken := listen(t)
	defer func() { _ = taken.Close() }()
	t.Setenv("GOBB_DSN", dsnOf(t, pool))
	t.Setenv("GOBB_LISTEN", taken.Addr().String())
	t.Setenv("GOBB_DATA_DIR", t.TempDir())
	sigs := make(chan os.Signal, 2)
	sigs <- os.Interrupt
	errc := make(chan error, 1)
	go func() { errc <- serveWith(context.Background(), sigs, func(int) {}) }()
	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("serveWith = %v, want nil after a signal during setup", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serveWith did not return")
	}
}

// At start, serve logs one INFO line with the Go memory limit, its source and
// the gate budget of the password checks that follows from it.
func TestGateForLogsTheMemoryLimit(t *testing.T) {
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, c := range []struct {
		in   memlimit.Result
		want string
	}{
		{memlimit.Result{Limit: 187904819, Source: memlimit.SourceCgroup}, "limit_bytes=187904819 source=cgroup gate_budget_kib=131072"},
		{memlimit.Result{Limit: 100 << 20, Source: memlimit.SourceEnv}, "limit_bytes=104857600 source=GOMEMLIMIT gate_budget_kib=53248"},
		{memlimit.Result{Source: memlimit.SourceNone}, "limit_bytes=0 source=none gate_budget_kib=131072"},
	} {
		buf.Reset()
		if g := gateFor(c.in); g == nil {
			t.Fatalf("%+v: no gate", c.in)
		}
		out := strings.TrimSpace(buf.String())
		if strings.Count(out, "\n") != 0 || !strings.Contains(out, "level=INFO") || !strings.Contains(out, c.want) {
			t.Errorf("%+v: log = %q, want one INFO line with %q", c.in, out, c.want)
		}
	}
}

// compose.yaml runs gobb under the memory cap that CI tests, with no swap on
// top, and the docs name the variable that changes it.
func TestComposeCapsMemory(t *testing.T) {
	b, err := os.ReadFile("../../compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mem_limit: ${GOBB_MEM_LIMIT:-256m}", "memswap_limit: ${GOBB_MEM_LIMIT:-256m}"} {
		if !strings.Contains(string(b), want) {
			t.Errorf("compose.yaml lacks %q", want)
		}
	}
	for _, file := range []string{"../../README.md", "../../.env.example"} {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "GOBB_MEM_LIMIT") {
			t.Errorf("%s does not name GOBB_MEM_LIMIT", file)
		}
	}
}

// With email off, startMail starts nothing and hands back a wake channel that
// nobody reads: a non-blocking send on it falls through to the default case.
func TestStartMailOff(t *testing.T) {
	wake, stop := startMail(t.Context(), nil, nil)
	defer stop()
	select {
	case wake <- struct{}{}:
		t.Error("a send on the wake channel of a board with no email went through")
	default:
	}
}

// With email on, the worker sends a queued row on a wake, the wake channel has
// room for one value, and stop returns after the worker has ended.
func TestStartMailSendsOnWake(t *testing.T) {
	pool := dbtest.New(t)
	srv := mailtest.Start(t, mailtest.Options{})
	old := mailRoots
	mailRoots = srv.CertPool()
	t.Cleanup(func() { mailRoots = old })
	cfg := &config.Mail{
		Host: "127.0.0.1", Port: srv.Port(), TLS: "starttls",
		From: "board@example.org", PublicURL: "https://board.example.org/",
	}
	wake, stop := startMail(t.Context(), pool, cfg)
	stopped := false
	defer func() {
		if !stopped {
			stop()
		}
	}()

	// The channel holds one value without a reader.
	if cap(wake) != 1 {
		t.Fatalf("cap(wake) = %d, want 1", cap(wake))
	}
	id, err := mail.Enqueue(t.Context(), pool, mail.Message{To: "someone@example.org", Subject: "s", Body: "b\r\n"},
		0, time.Time{}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case wake <- struct{}{}:
	default:
		t.Fatal("the wake channel is full")
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		var sent bool
		if err := pool.QueryRow(t.Context(), `select sent_at is not null from email_queue where id = $1`, id).Scan(&sent); err != nil {
			t.Fatal(err)
		}
		if sent {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the worker did not send the row")
		}
		time.Sleep(10 * time.Millisecond)
	}
	stop()
	stopped = true
	// After stop, a row that comes due is not sent.
	id2, err := mail.Enqueue(t.Context(), pool, mail.Message{To: "someone@example.org", Subject: "s", Body: "b\r\n"},
		0, time.Time{}, time.Now().Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	select {
	case wake <- struct{}{}:
	default:
	}
	time.Sleep(200 * time.Millisecond)
	var sent bool
	if err := pool.QueryRow(t.Context(), `select sent_at is not null from email_queue where id = $1`, id2).Scan(&sent); err != nil || sent {
		t.Errorf("a row was sent after stop (sent = %v, err = %v)", sent, err)
	}
}

// TestListenersAvoidMultipathTCP holds the godebug line in go.mod. Go turns
// multipath TCP on for listeners by default, and CVE-2026-98432 was exploited
// through a multipath TCP listener. gobb has no use for it.
func TestListenersAvoidMultipathTCP(t *testing.T) {
	var lc net.ListenConfig
	if lc.MultipathTCP() {
		t.Error("listeners use multipath TCP; want godebug multipathtcp=0 in go.mod")
	}
}
