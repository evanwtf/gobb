package login

import (
	"container/list"
	"context"
	"errors"
	"runtime"
	"sync"
	"time"
)

// ErrBusy means the memory gate stayed full for the whole wait, so the password
// was not checked. The caller answers "try again soon".
var ErrBusy = errors.New("login: too many password checks at once")

// Gate limits the memory that password checks use at one time. A check asks for
// its charge in KiB, and waits until that much is free. Waiters run in
// the order they came, so a run of small checks does not starve a large one. A
// Gate is safe for concurrent use.
type Gate struct {
	budget uint64

	mu      sync.Mutex
	used    uint64
	waiters list.List // of *waiter, oldest first
}

// waiter is a call of Acquire that waits for room.
type waiter struct {
	cost    uint64
	ready   chan struct{} // closed when the cost is held
	granted bool          // set under Gate.mu, with ready
}

// NewGate returns a Gate that lets checks hold budgetKiB of memory in all.
func NewGate(budgetKiB uint64) *Gate {
	return &Gate{budget: budgetKiB}
}

// Budget returns the memory that the gate lets holders use at one time, in KiB.
// It is the value that [NewGate] got, and it does not change while the gate is in
// use. A caller that knows a cost can never fit (a cost above Budget runs alone, as
// the whole budget) may refuse the work before it calls Acquire.
func (g *Gate) Budget() uint64 { return g.budget }

// Acquire waits up to wait for kib of the budget. A cost above the budget counts
// as the whole budget, so that check runs alone. On success it returns a release
// function, which gives the memory back; calling it twice is safe. When the wait
// ends with no room, Acquire returns [ErrBusy]. When ctx ends first, it returns
// the error of ctx.
func (g *Gate) Acquire(ctx context.Context, kib uint64, wait time.Duration) (release func(), err error) {
	cost := min(kib, g.budget)
	g.mu.Lock()
	if g.waiters.Len() == 0 && g.used+cost <= g.budget {
		g.used += cost
		g.mu.Unlock()
		return g.releaser(cost), nil
	}
	w := &waiter{cost: cost, ready: make(chan struct{})}
	elem := g.waiters.PushBack(w)
	g.mu.Unlock()

	timer := time.NewTimer(wait)
	defer timer.Stop()
	var cause error
	select {
	case <-w.ready:
		return g.releaser(cost), nil
	case <-timer.C:
		cause = ErrBusy
	case <-ctx.Done():
		cause = ctx.Err()
	}

	g.mu.Lock()
	defer g.mu.Unlock()
	if w.granted {
		// Room came as the wait ended. Take it.
		return g.releaser(cost), nil
	}
	g.waiters.Remove(elem)
	// The waiter may have been the one that held the line for those behind it.
	g.wake()
	return nil, cause
}

// AcquireCollected is [Gate.Acquire] with a release that runs a garbage
// collection first, then gives the share back.
//
// The memory of a holder is garbage when the holder ends, and the runtime
// collects it only after a later allocation, even under a memory limit. Without
// the collection, the next holder's memory lands on top of that garbage: with
// 64 MiB hashes, the heap held three 64 MiB blocks where the gate allows two,
// and the kernel killed a container under a 256 MiB cap. A holder whose memory
// is a large block of garbage at the end (a password hash, a decoded image) takes
// its share with this method. The collection is cheap when the block holds no
// pointers. Calling the release twice is safe.
func (g *Gate) AcquireCollected(ctx context.Context, kib uint64, wait time.Duration) (release func(), err error) {
	rel, err := g.Acquire(ctx, kib, wait)
	if err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			runtime.GC()
			rel()
		})
	}, nil
}

// fits returns how many holders of kib the gate runs at once. A cost above the
// budget counts as the whole budget, as in Acquire, so the answer is at least 1.
// A gate with no budget gives 0.
func (g *Gate) fits(kib uint64) uint64 {
	cost := min(kib, g.budget)
	if cost == 0 {
		return 0
	}
	return g.budget / cost
}

// releaser returns the function that gives cost back, once.
func (g *Gate) releaser(cost uint64) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			g.mu.Lock()
			defer g.mu.Unlock()
			g.used -= cost
			g.wake()
		})
	}
}

// wake grants room to the waiters at the head of the queue, in order, while they
// fit. The caller holds the lock.
func (g *Gate) wake() {
	for e := g.waiters.Front(); e != nil; e = g.waiters.Front() {
		w := e.Value.(*waiter)
		if g.used+w.cost > g.budget {
			return
		}
		g.used += w.cost
		w.granted = true
		close(w.ready)
		g.waiters.Remove(e)
	}
}
