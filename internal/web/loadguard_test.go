package web

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/config"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/metrics"
)

// loadSite imports b and returns the site on it, its handler and its pool.
func loadSite(t *testing.T, b *bundletest.Board, opt Options) (*site, http.Handler, *pgxpool.Pool) {
	t.Helper()
	pool, dataDir := boardtest.Load(t, b)
	opt.DataDir = dataDir
	if opt.LoginFloor == 0 {
		opt.LoginFloor = time.Millisecond
	}
	var s *site
	onNew = func(got *site) { s = got }
	t.Cleanup(func() { onNew = nil })
	h, err := New(t.Context(), pool, opt)
	onNew = nil
	if err != nil {
		t.Fatalf("web.New: %v", err)
	}
	return s, h, pool
}

// holdAll takes every connection of pool and returns the function that gives
// them back.
func holdAll(t *testing.T, pool *pgxpool.Pool) func() {
	t.Helper()
	var conns []*pgxpool.Conn
	for range pool.Stat().MaxConns() {
		c, err := pool.Acquire(t.Context())
		if err != nil {
			t.Fatal(err)
		}
		conns = append(conns, c)
	}
	var once sync.Once
	release := func() {
		once.Do(func() {
			for _, c := range conns {
				c.Release()
			}
		})
	}
	t.Cleanup(release)
	return release
}

// A request that finds every connection taken gets 503 with Retry-After after
// the wait limit (1 s by default), not after its 10 s deadline. The page comes
// from memory. When the connections come back, the pages work again.
func TestPoolWait503(t *testing.T) {
	_, h, pool := loadSite(t, bundletest.Small(), Options{})
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Fatalf("GET / before the flood = %d", rec.Code)
	}
	release := holdAll(t, pool)
	before := metrics.PoolWaitTimeouts.Value()
	for _, target := range []string{"/", "/t/1", "/search"} {
		start := time.Now()
		rec := get(t, h, target)
		took := time.Since(start)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("GET %s with a full pool = %d, want 503", target, rec.Code)
		}
		if took > 1500*time.Millisecond {
			t.Errorf("GET %s took %v, want at most about 1.5 s", target, took)
		}
		if got := rec.Header().Get("Retry-After"); got != "2" {
			t.Errorf("GET %s: Retry-After = %q, want 2", target, got)
		}
		body := bodyOf(t, rec)
		if !strings.Contains(body, "Try again soon") || !strings.Contains(body, "Example Board") {
			t.Errorf("GET %s: the 503 is not the page of the board:\n%s", target, body)
		}
	}
	if got := metrics.PoolWaitTimeouts.Value(); got != before+3 {
		t.Errorf("gobb_db_pool_wait_timeouts_total = %d, want %d", got, before+3)
	}
	release()
	if rec := get(t, h, "/"); rec.Code != http.StatusOK {
		t.Errorf("GET / after the flood = %d, want 200", rec.Code)
	}
}

// A handler that answers a failed query with its own 500 sends 503 with
// Retry-After instead, when the query found no free connection in time. Any
// other 500 stays a 500.
func TestPoolBusyWriter(t *testing.T) {
	_, _, pool := loadSite(t, bundletest.Small(), Options{})
	plain := httptest.NewRecorder()
	(&poolBusyWriter{ResponseWriter: plain, ctx: db.WithPoolWait(t.Context(), time.Second)}).WriteHeader(500)
	if plain.Code != 500 || plain.Header().Get("Retry-After") != "" {
		t.Errorf("a 500 with no pool timeout = %d, %q", plain.Code, plain.Header().Get("Retry-After"))
	}

	defer holdAll(t, pool)()
	ctx := db.WithPoolWait(t.Context(), 50*time.Millisecond)
	if _, err := pool.Exec(ctx, "select 1"); err == nil {
		t.Fatal("a query ran on a full pool")
	}
	rec := httptest.NewRecorder()
	(&poolBusyWriter{ResponseWriter: rec, ctx: ctx}).WriteHeader(500)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "2" {
		t.Errorf("a 500 after a pool timeout = %d, Retry-After %q; want 503, 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	// failStatus gives a deadline 503: it gets the Retry-After too.
	rec = httptest.NewRecorder()
	(&poolBusyWriter{ResponseWriter: rec, ctx: ctx}).WriteHeader(503)
	if rec.Code != 503 || rec.Header().Get("Retry-After") != "2" {
		t.Errorf("a 503 after a pool timeout = %d, Retry-After %q; want 503, 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	// A Retry-After of the handler stays.
	rec = httptest.NewRecorder()
	rec.Header().Set("Retry-After", "5")
	(&poolBusyWriter{ResponseWriter: rec, ctx: ctx}).WriteHeader(503)
	if got := rec.Header().Get("Retry-After"); got != "5" {
		t.Errorf("the handler's Retry-After = %q, want 5", got)
	}
	rec = httptest.NewRecorder()
	(&poolBusyWriter{ResponseWriter: rec, ctx: ctx}).WriteHeader(404)
	if rec.Code != 404 || rec.Header().Get("Retry-After") != "" {
		t.Errorf("a 404 after a pool timeout = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

// A save from gobb.js (X-Gobb-Save: 1) under a full pool gets 503 with
// Retry-After, as JSON.
func TestPoolWaitJSONSave(t *testing.T) {
	_, h, pool := loadSite(t, postingBoard(), Options{PoolWait: 100 * time.Millisecond})
	a := signIn(t, h, "alice", testPassword)
	token := accountToken(t, h, a)
	release := holdAll(t, pool)
	rec := postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}, saveHeader)
	if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "2" {
		t.Errorf("a save under a full pool = %d, Retry-After %q; want 503, 2", rec.Code, rec.Header().Get("Retry-After"))
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type = %q, want the JSON of a save", ct)
	}
	release()
	rec = postWith(t, h, "/account/timezone", a, url.Values{"timezone": {"Asia/Tokyo"}, "csrf": {token}}, saveHeader)
	if rec.Code != http.StatusNoContent {
		t.Errorf("the save after the flood = %d, want 204", rec.Code)
	}
}

// A sitemap build that finds the pool full answers 503, for the request that
// built it and for a request that waited for that build. The waiting request
// does not wait for the pool again.
func TestPoolWaitSitemap(t *testing.T) {
	_, h, pool := loadSite(t, bundletest.Small(), Options{PoolWait: 500 * time.Millisecond})
	release := holdAll(t, pool)
	builds, timeouts := sitemapBuilds.Load(), metrics.PoolWaitTimeouts.Value()
	codes := make([]*httptest.ResponseRecorder, 2)
	var wg sync.WaitGroup
	for i := range codes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/sitemap.xml", nil))
			codes[i] = rec
		}()
		// The second request comes while the first build waits for the pool.
		time.Sleep(100 * time.Millisecond)
	}
	wg.Wait()
	for i, rec := range codes {
		if rec.Code != http.StatusServiceUnavailable || rec.Header().Get("Retry-After") != "2" {
			t.Errorf("request %d: %d, Retry-After %q; want 503, 2", i+1, rec.Code, rec.Header().Get("Retry-After"))
		}
	}
	if got := sitemapBuilds.Load() - builds; got != 1 {
		t.Errorf("%d builds, want 1: the second request did not wait for the first", got)
	}
	// Only the build waited for the pool. The request that waited for the build
	// answered from it, and did not wait again for an error page.
	if got := metrics.PoolWaitTimeouts.Value() - timeouts; got != 1 {
		t.Errorf("%d pool timeouts, want 1", got)
	}
	// The failed build is not kept.
	release()
	if rec := get(t, h, "/sitemap.xml"); rec.Code != http.StatusOK {
		t.Errorf("the sitemap after the flood = %d, want 200", rec.Code)
	}
}

// The serve defaults in config and the zero-value defaults of Options agree.
func TestLoadDefaultsAgree(t *testing.T) {
	if config.DefaultDBPoolWait != defaultPoolWait {
		t.Errorf("pool wait: config %v, web %v", config.DefaultDBPoolWait, defaultPoolWait)
	}
	if config.DefaultSearchPerSecond != defaultSearchPerSecond {
		t.Errorf("searches per second: config %d, web %d", config.DefaultSearchPerSecond, defaultSearchPerSecond)
	}
	if config.DefaultSearchMaxRunning != defaultSearchMaxRunning {
		t.Errorf("searches at once: config %d, web %d", config.DefaultSearchMaxRunning, defaultSearchMaxRunning)
	}
}

// fakeClock is a clock that a test moves by hand.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Add(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// checkSearchOff checks the page of a refused search: 429, Retry-After, the
// owner's text in the layout of the board.
func checkSearchOff(t *testing.T, rec *httptest.ResponseRecorder, retry string) {
	t.Helper()
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status %d, want 429", rec.Code)
	}
	if got := rec.Header().Get("Retry-After"); got != retry {
		t.Errorf("Retry-After = %q, want %q", got, retry)
	}
	body := bodyOf(t, rec)
	for _, want := range []string{"Search functionality is currently disabled.", "Example Board", "/static/gobb.css"} {
		if !strings.Contains(body, want) {
			t.Errorf("the 429 page lacks %q:\n%s", want, body)
		}
	}
}

// The whole board runs at most 5 searches in a second. The 6th gets the 429
// page and touches no database. A second later, search works again. The bare
// form is not a search and does not count.
func TestSearchRateLimit(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	_, h, pool := loadSite(t, searchSiteBoard(), Options{Now: clk.Now})
	for i := range 5 {
		if rec := get(t, h, "/search?q=example"); rec.Code != http.StatusOK {
			t.Fatalf("search %d = %d, want 200", i+1, rec.Code)
		}
		clk.Add(100 * time.Millisecond)
	}
	if rec := get(t, h, "/search"); rec.Code != http.StatusOK {
		t.Errorf("the bare form after 5 searches = %d, want 200", rec.Code)
	}
	before := pool.Stat().AcquireCount()
	limited := metrics.RateLimited.Value("search")
	rec := get(t, h, "/search?author=alice")
	checkSearchOff(t, rec, "1")
	if got := pool.Stat().AcquireCount(); got != before {
		t.Errorf("the refused search took %d connections, want none", got-before)
	}
	if got := metrics.RateLimited.Value("search"); got != limited+1 {
		t.Errorf(`gobb_rate_limited_total{limit="search"} = %d, want %d`, got, limited+1)
	}
	// One second after the first search, it has left the window.
	clk.Add(500 * time.Millisecond)
	if rec := get(t, h, "/search?q=example"); rec.Code != http.StatusOK {
		t.Errorf("search the next second = %d, want 200", rec.Code)
	}
}

// The setting changes the limit, and the limit is for the whole board: a
// member and a guest share it.
func TestSearchRateLimitSetting(t *testing.T) {
	clk := &fakeClock{now: time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)}
	_, h, _ := loadSite(t, searchSiteBoard(), Options{Now: clk.Now, SearchPerSecond: 2})
	if rec := get(t, h, "/search?q=example"); rec.Code != http.StatusOK {
		t.Fatalf("guest search = %d", rec.Code)
	}
	cookies := signIn(t, h, "bob", "example-password")
	if rec := getAs(t, h, "/search?q=example", cookies); rec.Code != http.StatusOK {
		t.Fatalf("member search = %d", rec.Code)
	}
	checkSearchOff(t, getAs(t, h, "/search?q=example", cookies), "1")
	checkSearchOff(t, get(t, h, "/search?q=example"), "1")
}

// At most SearchMaxRunning searches run at once. One more gets the 429 page.
func TestSearchMaxRunning(t *testing.T) {
	s, h, pool := loadSite(t, searchSiteBoard(), Options{SearchMaxRunning: 1})
	if cap(s.searches.running) != 1 {
		t.Fatalf("running cap = %d, want 1", cap(s.searches.running))
	}
	s.searches.running <- struct{}{} // a search that runs now
	before, busy := pool.Stat().AcquireCount(), metrics.SearchBusy.Value()
	checkSearchOff(t, get(t, h, "/search?q=example"), "1")
	if got := pool.Stat().AcquireCount(); got != before {
		t.Errorf("the refused search took %d connections, want none", got-before)
	}
	if got := metrics.SearchBusy.Value(); got != busy+1 {
		t.Errorf("gobb_search_busy_total = %d, want %d", got, busy+1)
	}
	<-s.searches.running
	if rec := get(t, h, "/search?q=example"); rec.Code != http.StatusOK {
		t.Errorf("search with a free slot = %d, want 200", rec.Code)
	}
	if n := len(s.searches.running); n != 0 {
		t.Errorf("%d slots still held after the search", n)
	}
	// The defaults.
	s, _, _ = loadSite(t, bundletest.Small(), Options{})
	if cap(s.searches.running) != 2 {
		t.Errorf("default running cap = %d, want 2", cap(s.searches.running))
	}
}

// RefuseSearch, the hook of Defensive Mode, refuses a search with the same page,
// before the rate of the board counts it.
func TestRefuseSearchHook(t *testing.T) {
	refuse := true
	_, h, pool := loadSite(t, searchSiteBoard(), Options{SearchPerSecond: 1,
		RefuseSearch: func(*http.Request) (bool, time.Duration) { return refuse, 30 * time.Second }})
	before := pool.Stat().AcquireCount()
	checkSearchOff(t, get(t, h, "/search?q=example"), "30")
	if got := pool.Stat().AcquireCount(); got != before {
		t.Errorf("the refused search took %d connections, want none", got-before)
	}
	refuse = false
	if rec := get(t, h, "/search?q=example"); rec.Code != http.StatusOK {
		t.Errorf("search after the hook lets it pass = %d, want 200", rec.Code)
	}
}
