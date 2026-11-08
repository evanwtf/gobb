package db_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/metrics"
)

// onePool returns a pool of one connection on a test database, with the
// tracer of db.Trace, and the connection held, so every other wait finds the
// pool full. release gives the connection back.
func onePool(t *testing.T) (pool *pgxpool.Pool, release func()) {
	t.Helper()
	cfg := dbtest.New(t).Config()
	cfg.MaxConns, cfg.MinConns = 1, 0
	pool, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	held, err := pool.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return pool, held.Release
}

// A wait for a connection ends at the limit, marks the request, and counts
// once. A later wait of the same request fails at once and does not count
// again.
func TestPoolWaitEndsAtTheLimit(t *testing.T) {
	pool, release := onePool(t)
	defer release()
	before := metrics.PoolWaitTimeouts.Value()
	ctx := db.WithPoolWait(t.Context(), 200*time.Millisecond)
	start := time.Now()
	_, err := pool.Exec(ctx, "select 1")
	took := time.Since(start)
	if err == nil {
		t.Fatal("the query ran on a full pool")
	}
	if took < 150*time.Millisecond || took > time.Second {
		t.Errorf("the wait took %v, want about 200ms", took)
	}
	if !db.PoolWaitTimedOut(ctx) {
		t.Error("PoolWaitTimedOut = false after the wait ran out")
	}
	if got := metrics.PoolWaitTimeouts.Value(); got != before+1 {
		t.Errorf("gobb_db_pool_wait_timeouts_total = %d, want %d", got, before+1)
	}

	start = time.Now()
	if _, err := pool.Exec(ctx, "select 1"); err == nil {
		t.Fatal("the second query ran on a full pool")
	}
	if took := time.Since(start); took > 50*time.Millisecond {
		t.Errorf("the second wait took %v, want an at-once failure", took)
	}
	if got := metrics.PoolWaitTimeouts.Value(); got != before+1 {
		t.Errorf("the second wait counted: %d, want %d", got, before+1)
	}
	// Even with a free connection, the request does not wait again.
	release()
	if _, err := pool.Exec(ctx, "select 1"); err == nil {
		t.Error("a request whose wait ran out got a connection later")
	}
	// Another request is not marked.
	if _, err := pool.Exec(db.WithPoolWait(t.Context(), 200*time.Millisecond), "select 1"); err != nil {
		t.Errorf("a new request on a free pool: %v", err)
	}
}

// The limit covers the wait only: a statement that has its connection runs
// past it.
func TestPoolWaitDoesNotCancelARunningQuery(t *testing.T) {
	pool := dbtest.New(t)
	ctx := db.WithPoolWait(t.Context(), 50*time.Millisecond)
	if _, err := pool.Exec(ctx, "select pg_sleep(0.3)"); err != nil {
		t.Fatalf("a query longer than the wait limit: %v", err)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, "select pg_sleep(0.2)"); err != nil {
		t.Errorf("a statement in a transaction: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Errorf("commit: %v", err)
	}
	if db.PoolWaitTimedOut(ctx) {
		t.Error("PoolWaitTimedOut = true with no wait")
	}
}

// A wait that ends because the request ends first is not a pool timeout.
func TestPoolWaitRequestDeadlineFirst(t *testing.T) {
	pool, release := onePool(t)
	defer release()
	before := metrics.PoolWaitTimeouts.Value()
	parent, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	ctx := db.WithPoolWait(parent, time.Second)
	_, err := pool.Exec(ctx, "select 1")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want the deadline of the request", err)
	}
	if db.PoolWaitTimedOut(ctx) {
		t.Error("PoolWaitTimedOut = true after the request's own deadline")
	}
	if got := metrics.PoolWaitTimeouts.Value(); got != before {
		t.Errorf("gobb_db_pool_wait_timeouts_total = %d, want %d", got, before)
	}
}

// Without WithPoolWait, and with a limit of zero, nothing changes.
func TestPoolWaitOff(t *testing.T) {
	if db.WithPoolWait(t.Context(), 0) != t.Context() {
		t.Error("a zero limit made a new context")
	}
	if db.PoolWaitTimedOut(t.Context()) {
		t.Error("PoolWaitTimedOut = true for a plain context")
	}
}

// OpenSized sets the size of the pool, over any pool_max_conns of the DSN.
func TestOpenSized(t *testing.T) {
	cfg := dbtest.New(t).Config()
	dsn := cfg.ConnString()
	pool, err := db.OpenSized(t.Context(), dsn, 7)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if got := pool.Config().MaxConns; got != 7 {
		t.Errorf("MaxConns = %d, want 7", got)
	}
	if _, ok := pool.Config().ConnConfig.Tracer.(db.Tracer); !ok {
		t.Errorf("Tracer = %T, want db.Tracer", pool.Config().ConnConfig.Tracer)
	}
	if _, err := db.OpenSized(t.Context(), dsn, -1); err == nil {
		t.Error("OpenSized(-1) worked")
	}
}
