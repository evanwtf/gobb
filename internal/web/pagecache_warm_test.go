package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/batch"
	"github.com/evanwtf/gobb/internal/board"
)

// recordWarm sets onWarm for the test and returns the keys it saw.
func recordWarm(t *testing.T) func() []string {
	t.Helper()
	var mu sync.Mutex
	var keys []string
	onWarm = func(k string) {
		mu.Lock()
		keys = append(keys, k)
		mu.Unlock()
	}
	t.Cleanup(func() { onWarm = nil })
	return func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(keys)
	}
}

// wantWarmOrder is the order of the start-up render on the board of pool, less
// the pages that a guest cannot see (which are not stored): the index, the
// forums in board order, then the first pages of the topics by activity.
func wantWarmOrder(t *testing.T, pool *pgxpool.Pool, g *GuestCache) []string {
	t.Helper()
	want := []string{cacheKey{scheme: "slate", kind: cacheIndex}.String()}
	forums, err := board.ForumIDs(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range forums {
		want = append(want, cacheKey{scheme: "slate", kind: cacheForum, id: id, page: 1}.String())
	}
	topics, err := board.TopicsByActivity(t.Context(), pool, forums, board.TopicByActivity{}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range topics {
		want = append(want, cacheKey{scheme: "slate", kind: cacheTopic, id: tp.ID, page: 1}.String())
	}
	return slices.DeleteFunc(want, func(k string) bool { _, ok := g.Pages.Peek(k); return !ok })
}

func TestWarmUpOrder(t *testing.T) {
	_, pool, g := cachedSite(t, richBoard(), Options{})
	seen := recordWarm(t)
	g.warmPass(t.Context(), g.site.Load())
	got := seen()
	want := wantWarmOrder(t, pool, g)
	if !slices.Equal(got, want) {
		t.Errorf("warm order:\n got %q\nwant %q", got, want)
	}
	// The index, three forums and four topics that a guest can read.
	if len(got) != 8 {
		t.Errorf("%d pages warmed, want 8: %q", len(got), got)
	}
	if st := g.WarmStats(); st.Rendered != uint64(len(got)) || st.Passes != 1 || st.Running {
		t.Errorf("warm stats = %+v", st)
	}
	// A second pass renders nothing that the cache holds.
	g.warmPass(t.Context(), g.site.Load())
	if n := len(seen()); n != len(got) {
		t.Errorf("a second pass rendered %d more pages", n-len(got))
	}
}

func TestWarmUpRendersEveryPageOfATopic(t *testing.T) {
	_, _, g := cachedSite(t, pagedBoard(), Options{})
	seen := recordWarm(t)
	g.warmPass(t.Context(), g.site.Load())
	var pages []string
	for _, k := range seen() {
		if k[0] == byte(cacheTopic) {
			pages = append(pages, k)
		}
	}
	if !slices.Contains(pages, cacheKey{scheme: "slate", kind: cacheTopic, id: 7, page: 2}.String()) {
		t.Errorf("the second page of topic 7 was not warmed: %q", pages)
	}
}

func TestWarmUpStopsAtTheLimit(t *testing.T) {
	_, _, big := cachedSite(t, richBoard(), Options{})
	big.warmPass(t.Context(), big.site.Load())
	total := big.Pages.Stats().Bytes

	g := NewGuestCache(total/2, false)
	siteWithPool(t, richBoard(), Options{GuestCache: g})
	seen := recordWarm(t)
	g.warmPass(t.Context(), g.site.Load())
	st := g.Pages.Stats()
	if st.Evictions != 0 {
		t.Errorf("the start-up render evicted %d pages", st.Evictions)
	}
	if len(seen()) == 0 || len(seen()) >= 8 {
		t.Errorf("%d pages warmed under half the room", len(seen()))
	}
	if _, ok := g.Pages.Peek(cacheKey{scheme: "slate", kind: cacheIndex}.String()); !ok {
		t.Error("the index, the first page warmed, is gone")
	}
}

func TestWarmUpWaitsWhileThePoolIsBusy(t *testing.T) {
	_, pool, _ := cachedSite(t, richBoard(), Options{})
	cfg := pool.Config()
	cfg.MaxConns = 2
	small, err := pgxpool.NewWithConfig(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(small.Close)
	g := NewGuestCache(4<<20, false)
	if _, err := New(t.Context(), small, Options{LoginFloor: time.Millisecond, Signin: testSchedule(), GuestCache: g}); err != nil {
		t.Fatal(err)
	}
	// One of two connections in use: the pool has none to spare.
	held, err := small.Acquire(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.warmPass(t.Context(), g.site.Load())
	}()
	time.Sleep(500 * time.Millisecond)
	if st := g.WarmStats(); st.Rendered != 0 || !st.Running {
		t.Errorf("while the pool is busy: %+v", st)
	}
	if n := small.Stat().AcquiredConns(); n != 1 {
		t.Errorf("the worker took a connection of a busy pool: %d in use", n)
	}
	held.Release()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("the pass did not finish once the pool was free")
	}
	if st := g.WarmStats(); st.Rendered == 0 {
		t.Error("nothing was warmed once the pool was free")
	}
}

func TestWarmUpRendersADroppedPageAgain(t *testing.T) {
	g := NewGuestCache(4<<20, true)
	_, pool := siteWithPool(t, richBoard(), Options{GuestCache: g})
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		g.Run(ctx)
	}()
	t.Cleanup(func() { cancel(); <-done })
	key := cacheKey{scheme: "slate", kind: cacheTopic, id: 5, page: 1}.String()
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(20 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				t.Fatalf("timed out: %s", what)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("the first pass", func() bool { _, ok := g.Pages.Peek(key); return ok })
	g.site.Load().changedTopic(5)
	if _, ok := g.Pages.Peek(key); ok {
		t.Fatal("the drop kept the page")
	}
	waitFor("the page to come back", func() bool { _, ok := g.Pages.Peek(key); return ok })
	// The watch on the epoch runs too.
	if err := board.BumpPageEpoch(ctx, pool); err != nil {
		t.Fatal(err)
	}
	waitFor("the epoch to empty the cache", func() bool { _, ok := g.Pages.Peek(key); return !ok })
}

// navigate sends a top-level navigation from the address addr.
func navigate(t *testing.T, h http.Handler, target, addr string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, target, nil)
	r.RemoteAddr = addr
	r.Header.Set("Sec-Fetch-Dest", "document")
	for _, c := range cookies {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	return rec
}

func TestBatchedViewsOfCachedPages(t *testing.T) {
	w := batch.New()
	h, pool, _ := cachedSite(t, richBoard(), Options{Writes: w})
	views := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(t.Context(), `select view_count from topics where id = 5`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := views()
	navigate(t, h, "/t/5", "198.51.100.1:1", nil) // a miss
	navigate(t, h, "/t/5", "198.51.100.2:1", nil) // a hit
	get(t, h, "/t/5")                             // not a navigation: no view
	if views() != before {
		t.Fatal("a view reached the database before the flush")
	}
	if err := w.Flush(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if got := views(); got != before+2 {
		t.Errorf("views = %d, want %d", got, before+2)
	}
}

func TestBatchedVisitsAndAddresses(t *testing.T) {
	w := batch.New()
	h, pool := siteWithPool(t, richBoard(), Options{Writes: w})
	alice := signIn(t, h, "alice", testPassword)
	read := func() (visit, active time.Time, addrs int) {
		t.Helper()
		err := pool.QueryRow(t.Context(), `select last_visit_at, last_active_at,
       (select count(*) from user_ips where user_id = 1 and ip = '198.51.100.20')
  from users where id = 1`).Scan(&visit, &active, &addrs)
		if err != nil {
			t.Fatal(err)
		}
		return
	}

	// Ten minutes since the last activity: the new time waits for the flush.
	exec(t, pool, `update users set last_active_at = now() - interval '10 minutes' where id = 1`)
	_, active0, _ := read()
	navigate(t, h, "/t/5", "198.51.100.20:1", alice)
	if _, active, addrs := read(); !active.Equal(active0) || addrs != 0 {
		t.Fatalf("written before the flush: active %s (was %s), %d addresses", active, active0, addrs)
	}
	if err := w.Flush(t.Context(), pool); err != nil {
		t.Fatal(err)
	}
	if _, active, addrs := read(); !active.After(active0) || addrs != 1 {
		t.Errorf("after the flush: active %s (was %s), %d addresses", active, active0, addrs)
	}

	// Two hours since the last activity: a new visit, written at once, because
	// the page reads the last visit.
	exec(t, pool, `update users set last_visit_at = '2020-01-01T00:00:00Z', last_active_at = now() - interval '2 hours' where id = 1`)
	_, old, _ := read()
	navigate(t, h, "/new", "198.51.100.20:1", alice)
	visit, active, _ := read()
	if !visit.Equal(old) || !active.After(old) {
		t.Errorf("a new visit: last_visit %s, want %s; last_active %s", visit, old, active)
	}
	// A second page right after writes nothing more.
	navigate(t, h, "/t/5", "198.51.100.20:1", alice)
	if v, a, _ := read(); !v.Equal(visit) || !a.Equal(active) {
		t.Error("a page right after the start of a visit wrote the visit again")
	}
}
