package pagecache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// put renders e under key through Do, as a miss does.
func put(t *testing.T, c *Cache, key string, e Entry) {
	t.Helper()
	if _, _, err := c.Do(context.Background(), key, func() (Result, error) { return Result{Entry: e, Store: true}, nil }); err != nil {
		t.Fatal(err)
	}
}

func TestLRUEvictionUnderTheLimit(t *testing.T) {
	c := New(300, nil)
	put(t, c, "a", Entry{Value: "a", Size: 100})
	put(t, c, "b", Entry{Value: "b", Size: 100})
	put(t, c, "c", Entry{Value: "c", Size: 100})
	// A use of a makes b the least recently used.
	if _, ok := c.Get("a"); !ok {
		t.Fatal("a is missing")
	}
	put(t, c, "d", Entry{Value: "d", Size: 100})
	if _, ok := c.Peek("b"); ok {
		t.Error("b, the least recently used, stayed")
	}
	for _, k := range []string{"a", "c", "d"} {
		if _, ok := c.Peek(k); !ok {
			t.Errorf("%s was evicted", k)
		}
	}
	st := c.Stats()
	if st.Bytes != 300 || st.Entries != 3 || st.Evictions != 1 {
		t.Errorf("stats = %+v, want 300 bytes, 3 entries, 1 eviction", st)
	}
	// One large entry pushes out as many as it needs.
	put(t, c, "e", Entry{Value: "e", Size: 250})
	if st := c.Stats(); st.Bytes > 300 || st.Entries != 1 {
		t.Errorf("after a large entry: %+v", st)
	}
	// An entry over the limit is not stored, and evicts nothing.
	put(t, c, "huge", Entry{Value: "huge", Size: 301})
	if _, ok := c.Peek("huge"); ok {
		t.Error("an entry over the limit was stored")
	}
	if _, ok := c.Peek("e"); !ok {
		t.Error("an entry over the limit evicted another")
	}
}

func TestPeekDoesNotChangeTheOrder(t *testing.T) {
	c := New(200, nil)
	put(t, c, "a", Entry{Size: 100})
	put(t, c, "b", Entry{Size: 100})
	c.Peek("a")
	put(t, c, "c", Entry{Size: 100})
	if _, ok := c.Peek("a"); ok {
		t.Error("a Peek kept a, the least recently used")
	}
}

func TestSingleflightRendersOnce(t *testing.T) {
	c := New(1<<20, nil)
	const n = 50
	var renders atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	results := make([]any, n)
	for i := range n {
		wg.Go(func() {
			res, _, err := c.Do(context.Background(), "k", func() (Result, error) {
				renders.Add(1)
				<-release
				return Result{Entry: Entry{Value: "page", Size: 10}, Store: true}, nil
			})
			if err != nil {
				t.Error(err)
			}
			results[i] = res.Entry.Value
		})
	}
	// Let every goroutine reach the flight, then end the render.
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := renders.Load(); got != 1 {
		t.Errorf("%d renders for %d concurrent misses, want 1", got, n)
	}
	for i, v := range results {
		if v != "page" {
			t.Errorf("request %d got %v", i, v)
		}
	}
	if _, hit, _ := c.Do(context.Background(), "k", func() (Result, error) { t.Fatal("render after the store"); return Result{}, nil }); !hit {
		t.Error("the page was not stored")
	}
}

func TestNoStoreGoesToTheWaitersOnly(t *testing.T) {
	c := New(1<<20, nil)
	res, _, err := c.Do(context.Background(), "k", func() (Result, error) {
		return Result{Entry: Entry{Value: "404", Size: 10}, Store: false}, nil
	})
	if err != nil || res.Entry.Value != "404" {
		t.Fatalf("Do = %v, %v", res, err)
	}
	if _, ok := c.Peek("k"); ok {
		t.Error("a result with Store false was stored")
	}
}

func TestDropDuringARenderKeepsItOut(t *testing.T) {
	c := New(1<<20, nil)
	started, release := make(chan struct{}), make(chan struct{})
	done := make(chan Result)
	go func() {
		res, _, _ := c.Do(context.Background(), "t1", func() (Result, error) {
			close(started)
			<-release
			return Result{Entry: Entry{Value: "old", Size: 10, Topic: 1}, Store: true}, nil
		})
		done <- res
	}()
	<-started
	c.DropTopic(1)
	// A request after the drop does not join the old render: it renders anew.
	var fresh atomic.Bool
	got := make(chan Result)
	go func() {
		res, _, _ := c.Do(context.Background(), "t1", func() (Result, error) {
			fresh.Store(true)
			return Result{Entry: Entry{Value: "new", Size: 10, Topic: 1}, Store: true}, nil
		})
		got <- res
	}()
	if res := <-got; res.Entry.Value != "new" || !fresh.Load() {
		t.Errorf("a request after the drop got %v", res.Entry.Value)
	}
	close(release)
	if res := <-done; res.Entry.Value != "old" {
		t.Errorf("the waiter of the old render got %v", res.Entry.Value)
	}
	if e, ok := c.Peek("t1"); !ok || e.Value != "new" {
		t.Errorf("the cache holds %v, want the render after the drop", e.Value)
	}
}

func TestDrops(t *testing.T) {
	fill := func() *Cache {
		c := New(1<<20, nil)
		put(t, c, "index", Entry{Size: 1, Listing: true})
		put(t, c, "forum", Entry{Size: 1, Listing: true})
		put(t, c, "t1", Entry{Size: 1, Topic: 1, Users: []int64{10, 11}})
		put(t, c, "t1p2", Entry{Size: 1, Topic: 1, Users: []int64{12}})
		put(t, c, "t2", Entry{Size: 1, Topic: 2, Users: []int64{11}})
		put(t, c, "page", Entry{Size: 1})
		return c
	}
	for _, tc := range []struct {
		name string
		drop func(*Cache)
		kept []string
	}{
		{"topic", func(c *Cache) { c.DropTopic(1) }, []string{"t2", "page"}},
		{"listings", func(c *Cache) { c.DropListings() }, []string{"t1", "t1p2", "t2", "page"}},
		{"user", func(c *Cache) { c.DropUser(11) }, []string{"index", "forum", "t1p2", "page"}},
		{"user 0", func(c *Cache) { c.DropUser(0) }, []string{"index", "forum", "t1", "t1p2", "t2", "page"}},
		{"all", func(c *Cache) { c.DropAll() }, nil},
	} {
		c := fill()
		tc.drop(c)
		var kept []string
		for _, k := range []string{"index", "forum", "t1", "t1p2", "t2", "page"} {
			if _, ok := c.Peek(k); ok {
				kept = append(kept, k)
			}
		}
		if len(kept) != len(tc.kept) {
			t.Errorf("%s: kept %v, want %v", tc.name, kept, tc.kept)
			continue
		}
		for i := range kept {
			if kept[i] != tc.kept[i] {
				t.Errorf("%s: kept %v, want %v", tc.name, kept, tc.kept)
				break
			}
		}
		if st := c.Stats(); st.Bytes != int64(st.Entries) {
			t.Errorf("%s: bytes %d for %d entries of 1 byte", tc.name, st.Bytes, st.Entries)
		}
	}
}

func TestExpiry(t *testing.T) {
	now := time.Date(2026, 3, 14, 23, 0, 0, 0, time.UTC)
	var mu sync.Mutex
	clock := func() time.Time { mu.Lock(); defer mu.Unlock(); return now }
	c := New(1<<20, clock)
	put(t, c, "index", Entry{Size: 1, Expires: now.Add(time.Hour)})
	if _, ok := c.Get("index"); !ok {
		t.Fatal("the entry is missing before its end")
	}
	mu.Lock()
	now = now.Add(time.Hour)
	mu.Unlock()
	if _, ok := c.Peek("index"); ok {
		t.Error("Peek returned an entry at its end")
	}
	if _, ok := c.Get("index"); ok {
		t.Error("Get returned an entry at its end")
	}
	if st := c.Stats(); st.Entries != 0 || st.Bytes != 0 {
		t.Errorf("the ended entry stayed: %+v", st)
	}
}

func TestWaiterContextEnds(t *testing.T) {
	c := New(1<<20, nil)
	started, release := make(chan struct{}), make(chan struct{})
	go func() {
		_, _, _ = c.Do(context.Background(), "k", func() (Result, error) {
			close(started)
			<-release
			return Result{Entry: Entry{Size: 1}, Store: true}, nil
		})
	}()
	<-started
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.Do(ctx, "k", func() (Result, error) { return Result{}, nil }); !errors.Is(err, context.Canceled) {
		t.Errorf("a waiter whose context ended got %v", err)
	}
	close(release)
}

func TestRenderErrorIsShared(t *testing.T) {
	c := New(1<<20, nil)
	boom := errors.New("boom")
	if _, _, err := c.Do(context.Background(), "k", func() (Result, error) { return Result{}, boom }); !errors.Is(err, boom) {
		t.Errorf("Do = %v", err)
	}
	if _, ok := c.Peek("k"); ok {
		t.Error("a failed render was stored")
	}
}
