package db

import (
	"context"
	"sync/atomic"

	"github.com/jackc/pgx/v5"
)

// Counter counts the statements that run with one context. The web layer makes
// one for each request and shows the number in the page footer.
type Counter struct{ n atomic.Int64 }

type counterKey struct{}

// WithCounter returns a context that carries a new Counter, and the Counter.
// Every statement that runs on the pool with this context, or a context derived
// from it, adds one to the Counter. Another context has its own Counter, so
// concurrent requests never mix their counts.
func WithCounter(ctx context.Context) (context.Context, *Counter) {
	c := new(Counter)
	return context.WithValue(ctx, counterKey{}, c), c
}

// CounterFrom returns the Counter of ctx, or nil when it has none.
func CounterFrom(ctx context.Context) *Counter {
	c, _ := ctx.Value(counterKey{}).(*Counter)
	return c
}

// Add records one statement. The tracer calls it; it does nothing on a nil
// Counter.
func (c *Counter) Add() {
	if c != nil {
		c.n.Add(1)
	}
}

// Count returns the number of statements recorded so far.
func (c *Counter) Count() int64 {
	if c == nil {
		return 0
	}
	return c.n.Load()
}

// CountTracer is the pgx tracer that feeds the Counter of the statement's
// context. It counts each Exec, Query and QueryRow, each item of a batch and
// each CopyFrom. A transaction counts its begin and its commit as statements,
// as PostgreSQL runs them. A statement whose context has no Counter counts
// nowhere and does not fail. The tracer reads nothing else: no SQL text, no
// argument and no timing.
type CountTracer struct{}

var (
	_ pgx.QueryTracer    = CountTracer{}
	_ pgx.BatchTracer    = CountTracer{}
	_ pgx.CopyFromTracer = CountTracer{}
)

// TraceQueryStart counts one statement.
func (CountTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	CounterFrom(ctx).Add()
	return ctx
}

// TraceQueryEnd does nothing.
func (CountTracer) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

// TraceBatchStart does nothing: each item counts in TraceBatchQuery.
func (CountTracer) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	return ctx
}

// TraceBatchQuery counts one item of a batch.
func (CountTracer) TraceBatchQuery(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchQueryData) {
	CounterFrom(ctx).Add()
}

// TraceBatchEnd does nothing.
func (CountTracer) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}

// TraceCopyFromStart counts one copy.
func (CountTracer) TraceCopyFromStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceCopyFromStartData) context.Context {
	CounterFrom(ctx).Add()
	return ctx
}

// TraceCopyFromEnd does nothing.
func (CountTracer) TraceCopyFromEnd(context.Context, *pgx.Conn, pgx.TraceCopyFromEndData) {}

// Trace sets the counting tracer on cfg. Open calls it for every pool.
func Trace(cfg *pgx.ConnConfig) { cfg.Tracer = CountTracer{} }
