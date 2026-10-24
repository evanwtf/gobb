package main

import (
	"bytes"
	"context"
	"log/slog"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/session"
)

// The loop runs once at start, then once per tick, and stops with its context.
func TestCleanupLoop(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	tick := make(chan time.Time)
	ran := make(chan struct{}, 10)
	done := make(chan struct{})
	go func() {
		cleanupLoop(ctx, tick, func(context.Context) { ran <- struct{}{} })
		close(done)
	}()
	wait := func(what string) {
		t.Helper()
		select {
		case <-ran:
		case <-time.After(5 * time.Second):
			t.Fatalf("no cleanup %s", what)
		}
	}
	wait("at start")
	select {
	case <-ran:
		t.Fatal("a second cleanup ran with no tick")
	case <-time.After(50 * time.Millisecond):
	}
	tick <- time.Now()
	wait("after the first tick")
	tick <- time.Now()
	wait("after the second tick")
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the loop ignored its context")
	}
	if len(ran) != 0 {
		t.Error("a cleanup ran after the context ended")
	}
}

// No run starts when the context has ended already.
func TestCleanupLoopStopsBeforeFirstRun(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	var mu sync.Mutex
	runs := 0
	cleanupLoop(ctx, make(chan time.Time), func(context.Context) { mu.Lock(); runs++; mu.Unlock() })
	if runs != 0 {
		t.Errorf("ran %d times with a context that had ended", runs)
	}
}

// cleanup deletes the dead rows and logs the two counts.
func TestCleanupLogsCounts(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	if _, _, err := session.Create(t.Context(), pool, 1, false, netip.MustParseAddr("10.1.0.1"), "", now.Add(-48*time.Hour)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })

	cleanup(t.Context(), pool, func() time.Time { return now })

	out := buf.String()
	if !strings.Contains(out, "sessions=1") || !strings.Contains(out, "ips=0") {
		t.Errorf("log = %q, want sessions=1 and ips=0", out)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("sessions left = %d (%v), want 0", n, err)
	}
}
