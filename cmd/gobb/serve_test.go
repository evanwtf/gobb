package main

import (
	"context"
	"net"
	"net/http"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
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
		for _, v := range []string{"GOBB_DSN", "GOBB_LISTEN", "GOBB_DATA_DIR", "GOBB_BASE_PATH", "GOBB_TRUSTED_PROXIES"} {
			if !strings.Contains(string(b), v) {
				t.Errorf("%s does not name %s", file, v)
			}
		}
	}
}
