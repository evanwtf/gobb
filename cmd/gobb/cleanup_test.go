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
	if _, _, err := boardtest.Session(t.Context(), pool, 1, false, netip.MustParseAddr("10.1.0.1"), "", now.Add(-48*time.Hour)); err != nil {
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

// cleanup also deletes the old email rows and the expired reset links, and logs
// a count for each.
func TestCleanupLogsMailCount(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	now := time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	old := now.Add(-31 * 24 * time.Hour)
	if _, err := pool.Exec(t.Context(),
		`insert into email_queue (to_addr, created_at, next_attempt_at, sent_at)
		 values ('someone@example.org', $1, $1, $1)`, old); err != nil {
		t.Fatal(err)
	}
	for i, exp := range []time.Time{now.Add(-time.Minute), now, now.Add(time.Minute)} {
		if _, err := pool.Exec(t.Context(),
			`insert into password_resets (id, user_id, epoch, created_at, expires_at)
			 values ($1, 1, 0, $2, $3)`, string(rune('a'+i)), now.Add(-time.Hour), exp); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	cleanup(t.Context(), pool, func() time.Time { return now })

	if out := buf.String(); !strings.Contains(out, "email cleanup") || !strings.Contains(out, "rows=1") {
		t.Errorf("log = %q, want an email cleanup line with rows=1", out)
	}
	if out := buf.String(); !strings.Contains(out, "reset link cleanup") || !strings.Contains(out, "links=2") {
		t.Errorf("log = %q, want a reset link cleanup line with links=2", out)
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from email_queue`).Scan(&n); err != nil || n != 0 {
		t.Errorf("email rows left = %d (%v), want 0", n, err)
	}
	if err := pool.QueryRow(t.Context(), `select count(*) from password_resets`).Scan(&n); err != nil || n != 1 {
		t.Errorf("reset links left = %d (%v), want 1", n, err)
	}
}
