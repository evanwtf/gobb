package pagecache

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// inflight is a render that waits in render until finish.
type inflight struct {
	release chan struct{}
	done    chan Result
	renders *atomic.Int32
}

// startRender starts a Do of key in a goroutine whose render gives e, to be
// stored, and returns once the render runs.
func startRender(t *testing.T, c *Cache, key string, sc Scope, e Entry) *inflight {
	t.Helper()
	return startResult(t, c, key, sc, Result{Entry: e, Store: true})
}

// startResult is startRender for a render that gives res.
func startResult(t *testing.T, c *Cache, key string, sc Scope, res Result) *inflight {
	t.Helper()
	f := &inflight{release: make(chan struct{}), done: make(chan Result, 1), renders: &atomic.Int32{}}
	started := make(chan struct{})
	go func() {
		res, _, err := c.Do(context.Background(), key, sc, func() (Result, error) {
			f.renders.Add(1)
			close(started)
			<-f.release
			return res, nil
		})
		if err != nil {
			t.Error(err)
		}
		f.done <- res
	}()
	recv(t, started)
	return f
}

// finish ends the render and returns what its caller got.
func (f *inflight) finish(t *testing.T) Result {
	t.Helper()
	close(f.release)
	return recv(t, f.done)
}

// recv returns the next value of ch, and fails the test when none comes in
// five seconds: a broken cache fails the test rather than hangs it.
func recv[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("timed out")
	}
	var zero T
	return zero
}

func stored(c *Cache, key string) bool {
	_, ok := c.Peek(key)
	return ok
}

func TestTopicDropKeepsOtherTopicRenders(t *testing.T) {
	c := New(1<<20, nil)
	b := startRender(t, c, "tB", Scope{Topic: 2}, Entry{Value: "B", Size: 1, Topic: 2, Users: []int64{7}})
	c.DropTopic(1)
	if res := b.finish(t); res.Entry.Value != "B" {
		t.Errorf("the render of B gave %v", res.Entry.Value)
	}
	if !stored(c, "tB") {
		t.Error("a drop of topic A kept the render of topic B out of the cache")
	}
}

func TestTopicDropDiscardsItsOwnRender(t *testing.T) {
	c := New(1<<20, nil)
	a := startRender(t, c, "tA", Scope{Topic: 1}, Entry{Value: "old", Size: 1, Topic: 1})
	c.DropTopic(1)
	if res := a.finish(t); res.Entry.Value != "old" {
		t.Errorf("the caller of the render got %v", res.Entry.Value)
	}
	if stored(c, "tA") {
		t.Error("a render of topic A that began before a drop of A was stored")
	}
}

// The check before the store reads the entry, not only the scope that the
// caller gave: a wrong scope does not let a stale page in.
func TestTopicDropDiscardsARenderWithNoScope(t *testing.T) {
	c := New(1<<20, nil)
	a := startRender(t, c, "tA", Scope{}, Entry{Size: 1, Topic: 1})
	c.DropTopic(1)
	a.finish(t)
	if stored(c, "tA") {
		t.Error("a render whose entry names topic A was stored after a drop of A")
	}
}

func TestListingsDrop(t *testing.T) {
	c := New(1<<20, nil)
	forum := startRender(t, c, "f1", Scope{Listing: true}, Entry{Size: 1, Listing: true})
	topic := startRender(t, c, "t1", Scope{Topic: 1}, Entry{Size: 1, Topic: 1, Users: []int64{7}})
	static := startRender(t, c, "p", Scope{}, Entry{Size: 1})
	c.DropListings()
	forum.finish(t)
	topic.finish(t)
	static.finish(t)
	if stored(c, "f1") {
		t.Error("a forum page that began before a drop of the listings was stored")
	}
	if !stored(c, "t1") || !stored(c, "p") {
		t.Error("a drop of the listings kept a topic page or a static page out")
	}
	// A drop of a topic reaches the listings too.
	forum = startRender(t, c, "f2", Scope{Listing: true}, Entry{Size: 1, Listing: true})
	c.DropTopic(9)
	forum.finish(t)
	if stored(c, "f2") {
		t.Error("a forum page that began before a drop of a topic was stored")
	}
}

func TestMemberDrop(t *testing.T) {
	c := New(1<<20, nil)
	shows := startRender(t, c, "t1", Scope{Topic: 1}, Entry{Value: "old", Size: 1, Topic: 1, Users: []int64{10, 7}})
	other := startRender(t, c, "t2", Scope{Topic: 2}, Entry{Size: 1, Topic: 2, Users: []int64{8}})
	c.DropUser(7)
	shows.finish(t)
	other.finish(t)
	if stored(c, "t1") {
		t.Error("a render that shows member 7 and began before a drop of 7 was stored")
	}
	if !stored(c, "t2") {
		t.Error("a drop of member 7 kept out a render that does not show 7")
	}
	// A drop of the member after the render ends does not reach it back.
	c.DropUser(7)
	if !stored(c, "t2") {
		t.Error("a drop of member 7 removed a page that does not show 7")
	}
}

// A request that waits for a render gets its result when it came before the
// drop of a member that the page shows. A request after that drop asks again,
// and gets a render of its own.
func TestMemberDropAndTheWaiters(t *testing.T) {
	c := New(1<<20, nil)
	old := startRender(t, c, "t1", Scope{Topic: 1}, Entry{Value: "old", Size: 1, Topic: 1, Users: []int64{7}})
	waiter := func() chan Result {
		ch := make(chan Result, 1)
		go func() {
			res, _, _ := c.Do(context.Background(), "t1", Scope{Topic: 1}, func() (Result, error) {
				return Result{Entry: Entry{Value: "new", Size: 1, Topic: 1, Users: []int64{7}}, Store: true}, nil
			})
			ch <- res
		}()
		return ch
	}
	before := waiter()
	waitFor(t, func() bool { return c.Stats().Misses == 2 })
	c.DropUser(7)
	after := waiter()
	waitFor(t, func() bool { return c.Stats().Misses == 3 })
	old.finish(t)
	if res := recv(t, before); res.Entry.Value != "old" {
		t.Errorf("a request before the drop got %v, want the render it waited for", res.Entry.Value)
	}
	if res := recv(t, after); res.Entry.Value != "new" {
		t.Errorf("a request after the drop got %v, want a new render", res.Entry.Value)
	}
	if e, ok := c.Peek("t1"); !ok || e.Value != "new" {
		t.Errorf("the cache holds %v, want the render after the drop", e.Value)
	}
}

// A request joins a render in progress when the drops since it began do not
// reach its scope: an unrelated drop costs no second render.
func TestUnrelatedDropKeepsTheFlight(t *testing.T) {
	c := New(1<<20, nil)
	b := startRender(t, c, "tB", Scope{Topic: 2}, Entry{Value: "B", Size: 1, Topic: 2})
	c.DropTopic(1)
	c.DropUser(5)
	got := make(chan Result, 1)
	go func() {
		res, _, _ := c.Do(context.Background(), "tB", Scope{Topic: 2}, func() (Result, error) {
			t.Error("a second render after an unrelated drop")
			return Result{}, nil
		})
		got <- res
	}()
	waitFor(t, func() bool { return c.Stats().Misses == 2 })
	b.finish(t)
	if res := recv(t, got); res.Entry.Value != "B" {
		t.Errorf("the request that joined got %v", res.Entry.Value)
	}
	if n := b.renders.Load(); n != 1 {
		t.Errorf("%d renders, want 1", n)
	}
}

func TestDropAllDiscardsEveryRender(t *testing.T) {
	c := New(1<<20, nil)
	renders := []*inflight{
		startRender(t, c, "t1", Scope{Topic: 1}, Entry{Size: 1, Topic: 1, Users: []int64{7}}),
		startRender(t, c, "f1", Scope{Listing: true}, Entry{Size: 1, Listing: true}),
		startRender(t, c, "p", Scope{}, Entry{Size: 1}),
	}
	c.DropAll()
	for _, r := range renders {
		r.finish(t)
	}
	for _, k := range []string{"t1", "f1", "p"} {
		if stored(c, k) {
			t.Errorf("%s began before a drop of all and was stored", k)
		}
	}
}

// The record of drops has a fixed size, however many topics and members the
// drops name. A render older than the record is not stored; a newer one is.
func TestDropRecordIsBounded(t *testing.T) {
	c := New(1<<20, nil)
	old := startRender(t, c, "t1", Scope{Topic: 1}, Entry{Size: 1, Topic: 1})
	for i := range int64(3 * historySize) {
		c.DropTopic(1000 + i)
		c.DropUser(1000 + i)
	}
	c.mu.Lock()
	n, cp := len(c.history), cap(c.history)
	c.mu.Unlock()
	if n != historySize || cp > 2*historySize {
		t.Errorf("the record holds %d drops (capacity %d), want %d", n, cp, historySize)
	}
	// No drop in the record reaches t1, but the render began before the oldest
	// drop in it, and a drop that the record lost may have: the safe side is no
	// store.
	old.finish(t)
	if stored(c, "t1") {
		t.Error("a render older than the record of drops was stored")
	}
	put(t, c, "t2", Entry{Size: 1, Topic: 2})
	if !stored(c, "t2") {
		t.Error("a render after the drops was not stored")
	}
	c.mu.Lock()
	flights := len(c.flights)
	c.mu.Unlock()
	if flights != 0 {
		t.Errorf("%d flights left after every render ended", flights)
	}
}

func TestDroppedSince(t *testing.T) {
	c := New(1<<20, nil)
	gen := c.Gen()
	c.DropTopic(1)
	c.DropUser(7)
	for _, tc := range []struct {
		e    Entry
		want bool
	}{
		{Entry{Topic: 1}, true},
		{Entry{Topic: 2}, false},
		{Entry{Listing: true}, true},
		{Entry{Topic: 2, Users: []int64{7}}, true},
		{Entry{Topic: 2, Users: []int64{8}}, false},
		{Entry{}, false},
	} {
		if got := c.DroppedSince(gen, tc.e); got != tc.want {
			t.Errorf("DroppedSince(%+v) = %v, want %v", tc.e, got, tc.want)
		}
	}
	if c.DroppedSince(c.Gen(), Entry{Topic: 1}) {
		t.Error("a drop before gen counted")
	}
	c.DropAll()
	if !c.DroppedSince(gen+2, Entry{}) {
		t.Error("a drop of all did not reach a page of no scope")
	}
}

// waitFor waits until cond holds, for at most a second.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(time.Millisecond)
	}
}

// The record holds exactly historySize drops. A render that began just before
// them is checked against all of them; one drop more and it is too old.
func TestDropRecordBoundary(t *testing.T) {
	c := New(1<<20, nil)
	r := startRender(t, c, "t1", Scope{Topic: 1}, Entry{Size: 1, Topic: 1})
	for i := range int64(historySize) {
		c.DropUser(1000 + i)
	}
	r.finish(t)
	if !stored(c, "t1") {
		t.Error("a render followed by historySize drops that do not reach it was not stored")
	}

	c = New(1<<20, nil)
	r = startRender(t, c, "t1", Scope{Topic: 1}, Entry{Size: 1, Topic: 1})
	c.DropTopic(1)
	for i := range int64(historySize) {
		c.DropUser(1000 + i)
	}
	r.finish(t)
	if stored(c, "t1") {
		t.Error("a render reached by a drop that left the record was stored")
	}
}

// A listing request after a drop of the listings does not wait for the render
// that began before: it renders at once.
func TestListingRequestAfterADropRendersAtOnce(t *testing.T) {
	c := New(1<<20, nil)
	old := startRender(t, c, "f1", Scope{Listing: true}, Entry{Value: "old", Size: 1, Listing: true})
	defer old.finish(t)
	c.DropListings()
	got := make(chan Result, 1)
	go func() {
		res, _, _ := c.Do(context.Background(), "f1", Scope{Listing: true}, func() (Result, error) {
			return Result{Entry: Entry{Value: "new", Size: 1, Listing: true}, Store: true}, nil
		})
		got <- res
	}()
	select {
	case res := <-got:
		if res.Entry.Value != "new" {
			t.Errorf("the request after the drop got %v", res.Entry.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("the request after the drop waited for the render before it")
	}
}

// A result that may not be stored (a 404) depends on more than its entry
// names. A request that came after any drop does not get it: it asks again. A
// new topic drops only the listings, and its page must not stay a 404.
func TestNoStoreResultIsStaleAfterAnyDrop(t *testing.T) {
	c := New(1<<20, nil)
	old := startResult(t, c, "t5", Scope{Topic: 5}, Result{Entry: Entry{Value: "404", Size: 1, Topic: 5}})
	c.DropListings()
	got := make(chan Result, 1)
	go func() {
		res, _, _ := c.Do(context.Background(), "t5", Scope{Topic: 5}, func() (Result, error) {
			return Result{Entry: Entry{Value: "200", Size: 1, Topic: 5}, Store: true}, nil
		})
		got <- res
	}()
	waitFor(t, func() bool { return c.Stats().Misses == 2 })
	if res := old.finish(t); res.Entry.Value != "404" {
		t.Errorf("the caller of the old render got %v", res.Entry.Value)
	}
	if res := recv(t, got); res.Entry.Value != "200" {
		t.Errorf("a request after the drop got %v, want a new render", res.Entry.Value)
	}
	// With no drop, the waiters share a result that is not stored.
	old = startResult(t, c, "t6", Scope{Topic: 6}, Result{Entry: Entry{Value: "404", Size: 1, Topic: 6}})
	go func() {
		res, _, _ := c.Do(context.Background(), "t6", Scope{Topic: 6}, func() (Result, error) {
			t.Error("a second render with no drop")
			return Result{}, nil
		})
		got <- res
	}()
	waitFor(t, func() bool { return c.Stats().Misses == 4 })
	old.finish(t)
	if res := recv(t, got); res.Entry.Value != "404" {
		t.Errorf("a waiter with no drop got %v", res.Entry.Value)
	}
}

// Under drops of members that reach each render it joins, a request takes two
// stale results at most, then renders on its own: it does not join a third.
func TestRetryIsBounded(t *testing.T) {
	c := New(1<<20, nil)
	joined := make(chan struct{}, 8)
	joinHook = func(string) { joined <- struct{}{} }
	t.Cleanup(func() { joinHook = nil })
	sc := Scope{Topic: 1}
	f1 := startRender(t, c, "t1", sc, Entry{Value: "f1", Size: 1, Topic: 1, Users: []int64{7}})
	c.DropUser(7)
	got := make(chan Result, 1)
	var own atomic.Int32
	go func() {
		res, _, err := c.Do(context.Background(), "t1", sc, func() (Result, error) {
			own.Add(1)
			return Result{Entry: Entry{Value: "own", Size: 1, Topic: 1}, Store: true}, nil
		})
		if err != nil {
			t.Error(err)
		}
		got <- res
	}()
	recv(t, joined) // the request waits for f1, which a drop of member 7 reached
	// A drop of the topic makes the next request start f2, not join f1.
	c.DropTopic(1)
	f2 := startRender(t, c, "t1", sc, Entry{Value: "f2", Size: 1, Topic: 1, Users: []int64{8}})
	c.DropUser(8)
	f1.finish(t)
	recv(t, joined) // the request asks again and joins f2: no drop since f2 began reaches member 7
	c.DropTopic(1)
	f3 := startRender(t, c, "t1", sc, Entry{Value: "f3", Size: 1, Topic: 1})
	defer f3.finish(t)
	f2.finish(t)
	select {
	case res := <-got:
		if res.Entry.Value != "own" || own.Load() != 1 {
			t.Errorf("after two stale results the request got %v (%d own renders), want its own render", res.Entry.Value, own.Load())
		}
	case <-joined:
		t.Fatal("the request joined a third render")
	case <-time.After(time.Second):
		t.Fatal("the request did not render on its own")
	}
}
