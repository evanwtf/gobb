package web

import (
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// panicOn makes the render of key panic for the rest of the test.
func panicOn(t *testing.T, key string) {
	t.Helper()
	beforeGuestRender = func(k string) {
		if k == key {
			panic("a broken template")
		}
	}
	t.Cleanup(func() { beforeGuestRender = nil })
}

// A handler that panics in a render for a request gives that request a 500,
// as a live page that fails to build does. The server goes on.
func TestGuestCacheRenderPanicIsA500(t *testing.T) {
	h, _, g := cachedSite(t, richBoard(), Options{})
	panicOn(t, cacheKey{scheme: "slate", kind: cacheTopic, id: 5, page: 1}.String())
	if rec := get(t, h, "/t/5"); rec.Code != http.StatusInternalServerError {
		t.Errorf("a render that panicked = %d, want 500", rec.Code)
	}
	if rec := get(t, h, "/t/6"); rec.Code != http.StatusOK {
		t.Errorf("another page after the panic = %d", rec.Code)
	}
	beforeGuestRender = nil
	if rec := get(t, h, "/t/5"); rec.Code != http.StatusOK {
		t.Errorf("the page once the panic is gone = %d", rec.Code)
	}
	if _, ok := g.Pages.Peek(cacheKey{scheme: "slate", kind: cacheTopic, id: 5, page: 1}.String()); !ok {
		t.Error("the page is not cached once it renders")
	}
}

// A handler that panics in the start-up render is logged and skipped: the pass
// goes on with the other pages, and the process lives.
func TestWarmUpSurvivesARenderPanic(t *testing.T) {
	_, _, g := cachedSite(t, richBoard(), Options{})
	panicOn(t, cacheKey{scheme: "slate", kind: cacheIndex}.String())
	seen := recordWarm(t)
	g.warmPass(t.Context(), g.site.Load())
	if n := len(seen()); n != 7 {
		t.Errorf("%d pages warmed around a panic, want 7 (all but the index)", n)
	}
	// The broken page is skipped until a drop: a second pass renders nothing.
	builds := layoutBuilds.Load()
	g.warmPass(t.Context(), g.site.Load())
	if n := layoutBuilds.Load() - builds; n != 0 {
		t.Errorf("a second pass built %d pages", n)
	}
}

// A guest cannot read the topics of a members-only forum: the start-up render
// does not list them, and a second pass with no drop renders nothing, not even
// the pages that answered 404 or 403.
func TestWarmUpSkipsWhatAGuestCannotSee(t *testing.T) {
	_, pool, g := cachedSite(t, bundletest.Small(), Options{})
	forums, readable, err := guestForums(t.Context(), g.site.Load())
	if err != nil {
		t.Fatal(err)
	}
	topics, err := board.TopicsByActivity(t.Context(), pool, readable, board.TopicByActivity{}, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, tp := range topics {
		if tp.ID == 2 {
			t.Error("the topic of the members-only forum 3 is listed")
		}
	}
	if len(forums) != 3 || len(readable) != 1 {
		t.Errorf("forums %v, readable %v; want 3 and [2]", forums, readable)
	}
	g.warmPass(t.Context(), g.site.Load())
	if g.WarmStats().Rendered == 0 {
		t.Fatal("the first pass warmed nothing")
	}
	builds := layoutBuilds.Load()
	g.warmPass(t.Context(), g.site.Load())
	if n := layoutBuilds.Load() - builds; n != 0 {
		t.Errorf("a second pass with no drop built %d pages, want 0", n)
	}
	// A drop that reaches none of the skipped pages keeps them skipped. Member
	// 999999 shows on no page.
	g.Pages.DropUser(999999)
	builds = layoutBuilds.Load()
	g.warmPass(t.Context(), g.site.Load())
	if n := layoutBuilds.Load() - builds; n != 0 {
		t.Errorf("a pass after a drop that reaches no page built %d pages, want 0", n)
	}
	// A drop that reaches them starts over: the skipped pages are tried again.
	g.Pages.DropAll()
	builds = layoutBuilds.Load()
	g.warmPass(t.Context(), g.site.Load())
	if layoutBuilds.Load() == builds {
		t.Error("a pass after a drop built nothing")
	}
}

// A write that failed may commit late: it drops again after lateCommitDelay. A
// refusal drops nothing.
func TestAfterWriteDropsAgainAfterAnError(t *testing.T) {
	_, _, g := cachedSite(t, richBoard(), Options{})
	s := g.site.Load()
	old := lateCommitDelay
	lateCommitDelay = 20 * time.Millisecond
	t.Cleanup(func() { lateCommitDelay = old })
	for _, c := range []struct {
		name string
		err  error
		want int32
	}{
		{"success", nil, 1},
		{"timeout", errors.New("timeout during commit"), 2},
		{"refusal", board.ErrForbidden, 0},
	} {
		var n atomic.Int32
		s.afterWrite(c.err, func() { n.Add(1) })
		time.Sleep(200 * time.Millisecond)
		if got := n.Load(); got != c.want {
			t.Errorf("%s: %d drops, want %d", c.name, got, c.want)
		}
	}
}

// The scope of a post: its topic and author, read before the write.
func TestPostScope(t *testing.T) {
	_, _, g := cachedSite(t, richBoard(), Options{})
	s := g.site.Load()
	sc := s.postScopeOf(t.Context(), 8)
	if !sc.found || sc.topic != 5 || sc.author != 2 {
		t.Fatalf("scope of post 8 = %+v", sc)
	}
	if sc := s.postScopeOf(t.Context(), 999); sc.found {
		t.Error("a missing post has a scope")
	}
}

// A guest who asks for a topic just after it is created gets the topic, not
// the 404 of a render that began before it existed. The render of the first
// guest pauses after it reads the topic (not found yet); the topic is created;
// a second guest asks.
func TestGuestCacheNewTopicIsNotA404(t *testing.T) {
	h, pool, g := cachedSite(t, richBoard(), Options{})
	exec(t, pool, carolPostsFreely)
	const id = 500
	exec(t, pool, `alter table topics alter column id restart with 500`)
	carol := signIn(t, h, "carol", testPassword)
	paused, resume := make(chan struct{}), make(chan struct{})
	var once sync.Once
	afterTopicView = func(got int64, err error) {
		if got == id && errors.Is(err, board.ErrNotFound) {
			once.Do(func() {
				close(paused)
				<-resume
			})
		}
	}
	t.Cleanup(func() { afterTopicView = nil })
	first := make(chan int, 1)
	go func() { first <- get(t, h, "/t/500").Code }()
	<-paused
	rec, _ := postAs(t, h, "/f/2/post", carol, form("title", "Carol's new topic", "body", "Hello."))
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("new topic = %d", rec.Code)
	}
	var topic int64
	if err := pool.QueryRow(t.Context(), `select topic_id from posts where id = $1`, idOf(t, rec)).Scan(&topic); err != nil || topic != id {
		t.Fatalf("the new topic is %d (%v), want %d", topic, err, id)
	}
	second := make(chan int, 1)
	misses := g.Pages.Stats().Misses
	go func() { second <- get(t, h, "/t/500").Code }()
	deadline := time.Now().Add(5 * time.Second)
	for g.Pages.Stats().Misses == misses {
		if time.Now().After(deadline) {
			t.Fatal("the second guest did not reach the cache")
		}
		time.Sleep(time.Millisecond)
	}
	close(resume)
	if code := <-first; code != http.StatusNotFound {
		t.Errorf("the first guest, before the topic, got %d, want 404", code)
	}
	if code := <-second; code != http.StatusOK {
		t.Errorf("a guest after the topic was created got %d, want 200", code)
	}
}
