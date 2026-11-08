package db

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/metrics"
)

// ErrPoolWait is the cause of a connection wait that ran past the limit of
// [WithPoolWait].
var ErrPoolWait = errors.New("db: no free connection within the wait limit")

// poolWait is the budget of the connection waits of one request: the time that
// they spent together, and whether one of them ran out.
type poolWait struct {
	limit    time.Duration
	spent    atomic.Int64 // nanoseconds
	timedOut atomic.Bool
}

type poolWaitKey struct{}

// acquireCancelKey holds the cancel function of the context of one wait.
type acquireCancelKey struct{}

// acquireStartKey holds the time that one wait began.
type acquireStartKey struct{}

// WithPoolWait returns a context in which the waits for a pool connection end
// after limit in total: each wait gets what the earlier waits under the context
// left of the limit. So a page that takes several connections in turn still
// waits at most limit, and its 503 comes within it. The limit covers the waits
// only: a statement that has its connection runs under ctx, with the deadline
// of ctx. After one wait runs out, every later wait under the context fails at
// once, so a request that found the pool full does not wait again for its
// error page. A limit of zero or less returns ctx as it is.
func WithPoolWait(ctx context.Context, limit time.Duration) context.Context {
	if limit <= 0 {
		return ctx
	}
	return context.WithValue(ctx, poolWaitKey{}, &poolWait{limit: limit})
}

// PoolWaitTimedOut reports whether a wait for a connection under ctx ran past
// the limit of [WithPoolWait].
func PoolWaitTimedOut(ctx context.Context) bool {
	w, _ := ctx.Value(poolWaitKey{}).(*poolWait)
	return w != nil && w.timedOut.Load()
}

// Tracer is the pgx tracer of every pool that [Open] makes. It counts the
// statements ([CountTracer]), and it limits the wait for a connection under a
// context from [WithPoolWait].
//
// pgxpool takes a connection inside each Query, Exec and Begin, with the
// context of the statement, so a shorter deadline on that context would also
// cancel a slow statement. The pool hands the context of the acquire tracer to
// the wait alone, and runs the statement under the caller's context. So the
// limit goes on the context that TraceAcquireStart returns.
type Tracer struct{ CountTracer }

var _ pgxpool.AcquireTracer = Tracer{}

// TraceAcquireStart gives the wait a deadline of what is left of the budget of
// ctx. A context whose wait already ran out, or whose budget is spent, gets a
// context that is already canceled, so the wait fails at once.
func (Tracer) TraceAcquireStart(ctx context.Context, _ *pgxpool.Pool, _ pgxpool.TraceAcquireStartData) context.Context {
	w, _ := ctx.Value(poolWaitKey{}).(*poolWait)
	if w == nil {
		return ctx
	}
	var c context.Context
	var cancel context.CancelFunc
	left := w.limit - time.Duration(w.spent.Load())
	if w.timedOut.Load() || left <= 0 {
		var cancelCause context.CancelCauseFunc
		c, cancelCause = context.WithCancelCause(ctx)
		cancelCause(ErrPoolWait)
		cancel = func() {}
	} else {
		c, cancel = context.WithTimeoutCause(ctx, left, ErrPoolWait)
	}
	c = context.WithValue(c, acquireStartKey{}, time.Now())
	return context.WithValue(c, acquireCancelKey{}, cancel)
}

// TraceAcquireEnd frees the deadline of the wait. A wait that failed because
// its own deadline passed, and not the deadline of the request, marks the
// request, counts once in gobb_db_pool_wait_timeouts_total, and goes into the
// WARN line of [timeoutLog].
func (Tracer) TraceAcquireEnd(ctx context.Context, _ *pgxpool.Pool, d pgxpool.TraceAcquireEndData) {
	cancel, ok := ctx.Value(acquireCancelKey{}).(context.CancelFunc)
	if !ok {
		return
	}
	defer cancel()
	w, _ := ctx.Value(poolWaitKey{}).(*poolWait)
	if start, ok := ctx.Value(acquireStartKey{}).(time.Time); ok && w != nil {
		w.spent.Add(int64(time.Since(start)))
	}
	if w != nil && d.Err != nil && errors.Is(context.Cause(ctx), ErrPoolWait) && w.timedOut.CompareAndSwap(false, true) {
		metrics.PoolWaitTimeouts.Inc()
		poolWaitLog.note(time.Now())
	}
}

// poolWaitLog writes the WARN line of the requests whose wait ran out.
var poolWaitLog = &timeoutLog{every: time.Minute}

// timeoutLog writes at most one WARN line in each period of every, with the
// count of the requests whose connection wait ran out since the last line. The
// first timeout after a quiet period writes a line at once. A flood therefore
// writes one line a minute, not one line a request.
type timeoutLog struct {
	every time.Duration
	mu    sync.Mutex
	last  time.Time
	n     int
}

// note records one timeout at now, and writes the line when the period since
// the last line has passed.
func (l *timeoutLog) note(now time.Time) {
	l.mu.Lock()
	l.n++
	if !l.last.IsZero() && now.Sub(l.last) < l.every {
		l.mu.Unlock()
		return
	}
	n := l.n
	l.n, l.last = 0, now
	l.mu.Unlock()
	slog.Warn("requests found no free database connection within GOBB_DB_POOL_WAIT and got 503", "count", n,
		"note", "see gobb_db_pool_wait_timeouts_total; raise GOBB_DB_MAX_CONNS or find the slow queries")
}
