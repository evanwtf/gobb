// Package pagecache holds the guest page cache (spec 10, "Load protection"): the
// pages that a guest sees, built once and served from memory.
//
// The cache knows nothing of HTTP or of the board. It keeps entries under a
// memory limit and drops the least recently used one when a new entry needs
// room. It renders each missing key once at a time, however many requests ask
// for it ([Cache.Do]). It drops entries on request: all of them, those of one
// topic, those that list topics, or those that show a member's posts.
//
// Each drop is one step of a counter, the generation ([Cache.Gen]), and the
// cache keeps a record of the recent drops: their generation and their scope
// (every page, one topic and the listings, the listings, or one member). This
// record is the generation of each scope: the generation of a scope is the
// last drop in the record that reaches it. A render notes the generation when
// it starts. When it ends, the cache finds the first drop after that start
// that reaches the page, from what the page depends on: the [Scope] that the
// caller gave, and the topic, listing and members of the entry. When there is
// one, the cache does not store the page, because it may hold what the drop
// removed. A drop of another topic or another member does not stop the store.
//
// The members of a topic page are known only once it renders (Entry.Users), so
// a drop of a member cannot be matched while the render runs. The record
// solves this: the check at the end reads every drop since the start, the drops
// of members among them, against the members that the page shows.
//
// A result that may not be stored (a 404, a redirect) can depend on more than
// its entry names: a topic page that answered 404 before the topic existed
// names no member, and the new topic drops only the listings. So for such a
// result every drop counts as one that reaches it.
//
// The requests that waited for a render get its result when they came before
// the first drop that reaches it: the drop had not happened for them. A request
// that comes after such a drop does not get the old result. When the drop
// reaches the scope known at the start, the request starts a new render at
// once. When it reaches a member, which the cache learns only at the end, the
// request waits for the render, then asks again. On that second ask it joins a
// render only when no drop since its start reaches what the stale result
// depended on. After a second stale result it renders on its own, so a request
// waits for at most three renders.
//
// The record holds the last [historySize] drops, so its memory is fixed, however
// many topics and members the drops name. A render that began before the
// oldest drop in the record is not stored: the cache cannot tell what came
// between, and it takes the safe side.
package pagecache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// Entry is one page of the cache, with what the drops need to know about it.
type Entry struct {
	// Value is what the caller stores. The cache never reads it.
	Value any
	// Size is the number of bytes that the entry counts against the limit.
	Size int64
	// Topic is the topic whose page this is, or 0 for a page of no topic.
	Topic int64
	// Listing is true for a page that lists forums or topics: the index and the
	// forum pages. A change to any topic drops every listing.
	Listing bool
	// Users are the members whose posts the page shows. A change to a member
	// that shows on each of their posts (a post count, a signature) drops the
	// page.
	Users []int64
	// Expires is the time after which the entry is not served, or zero for
	// never. A page whose content depends on the date (the birthdays of the
	// index) ends at the next midnight.
	Expires time.Time
}

// Scope is what the caller knows of a page before it renders: its topic, or 0,
// and whether it is a listing. The cache uses it to start a new render, not join
// the one in progress, when a drop reaches the page. The check before the store
// also reads the topic, listing and members of the entry that the render gave.
type Scope struct {
	Topic   int64
	Listing bool
}

// Result is what one render gave: the entry, and whether it may be stored. A
// page that is not stored (an error page, a redirect) still goes to every
// request that waited for the render.
type Result struct {
	Entry Entry
	Store bool
}

// Stats are the counts of a cache, for the metrics.
type Stats struct {
	Hits, Misses, Evictions, Drops uint64
	Bytes                          int64
	Entries                        int
}

// Cache is the page cache. The zero value is not usable; call [New].
type Cache struct {
	limit int64
	now   func() time.Time

	mu    sync.Mutex
	bytes int64
	order *list.List // front: most recently used; each value is *item
	items map[string]*list.Element
	// gen counts the drops. history holds the last historySize of them: the
	// drop of generation g is at history[(g-1)%historySize].
	gen     uint64
	history []dropRec
	// flights holds the render in progress of each key: the last one to start.
	flights map[string]*flight

	hits, misses, evictions, drops atomic.Uint64
}

type item struct {
	key   string
	entry Entry
}

// historySize is the number of drops that the cache keeps a record of. A render
// that began more than historySize drops ago is not stored. A record is 24
// bytes, so the record of a cache is at most 96 KiB.
const historySize = 4096

// dropKind names the scope of a drop.
type dropKind uint8

const (
	dropAll      dropKind = iota // every page
	dropTopic                    // the pages of one topic, and every listing
	dropListings                 // every listing
	dropUser                     // the pages that show one member
)

// dropRec is one drop in the record.
type dropRec struct {
	gen  uint64
	kind dropKind
	id   int64
}

// deps is what a page depends on: a drop reaches the page when it reaches one
// of these, or any drop when all is true.
type deps struct {
	all bool
	// topics are the topic of the scope and the topic of the entry; 0 is none.
	topics  [2]int64
	listing bool
	users   []int64
}

func depsOf(sc Scope, e Entry) deps {
	return deps{topics: [2]int64{sc.Topic, e.Topic}, listing: sc.Listing || e.Listing, users: e.Users}
}

// reaches reports whether the drop r reaches a page that depends on d.
func (r dropRec) reaches(d deps) bool {
	if d.all {
		return true
	}
	switch r.kind {
	case dropTopic:
		return d.listing || (r.id != 0 && (d.topics[0] == r.id || d.topics[1] == r.id))
	case dropListings:
		return d.listing
	case dropUser:
		return slices.Contains(d.users, r.id)
	}
	return true
}

// flight is one render in progress. done closes when res, err and stale are
// set.
type flight struct {
	done chan struct{}
	// start is the generation when the render began; scope is the scope that
	// its caller gave.
	start uint64
	scope Scope
	res   Result
	err   error
	// deps is what the result depends on: the scope, the entry, and all drops
	// for a result that may not be stored. stale is the generation of the first
	// drop after start that reaches deps, or 0 when there is none.
	deps  deps
	stale uint64
}

// maxStale is the number of stale results that a request takes from the renders
// of others. After that, it renders on its own.
const maxStale = 2

// joinHook, when set, runs each time a request joins a render in progress. A
// test sets it.
var joinHook func(key string)

// New returns a cache that holds at most limit bytes of entries. now gives the
// time for the expiry of the entries; nil means time.Now.
func New(limit int64, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{limit: limit, now: now, order: list.New(), items: map[string]*list.Element{}, flights: map[string]*flight{}}
}

// Gen returns the generation: a number that every drop adds one to. Pass it to
// [Cache.DroppedSince] later to learn whether a drop since reached a page.
func (c *Cache) Gen() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen
}

// Limit returns the memory limit of the cache in bytes.
func (c *Cache) Limit() int64 { return c.limit }

// Stats returns the counts of the cache.
func (c *Cache) Stats() Stats {
	c.mu.Lock()
	bytes, n := c.bytes, len(c.items)
	c.mu.Unlock()
	return Stats{Hits: c.hits.Load(), Misses: c.misses.Load(), Evictions: c.evictions.Load(), Drops: c.drops.Load(), Bytes: bytes, Entries: n}
}

// Get returns the entry of key, and marks it as the most recently used. An
// entry past its expiry is removed and not returned. Get counts neither a hit
// nor a miss; [Cache.Do] does.
func (c *Cache) Get(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(key)
}

func (c *Cache) getLocked(key string) (Entry, bool) {
	el, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	it := el.Value.(*item)
	if !it.entry.Expires.IsZero() && !c.now().Before(it.entry.Expires) {
		c.removeLocked(el)
		return Entry{}, false
	}
	c.order.MoveToFront(el)
	return it.entry, true
}

// Lookup returns the stored entry of key, as [Cache.Get] does: it marks the
// entry as used and drops an expired one. It counts a hit when it finds the
// entry, and nothing when it does not. It never renders: a caller that finds
// nothing goes on to [Cache.Do], which counts the miss.
func (c *Cache) Lookup(key string) (Entry, bool) {
	e, ok := c.Get(key)
	if ok {
		c.hits.Add(1)
	}
	return e, ok
}

// Peek returns the entry of key when it has one that has not expired. Unlike
// [Cache.Get], it does not mark the entry as used, so a worker that walks the
// cache does not change the order of eviction.
func (c *Cache) Peek(key string) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[key]
	if !ok {
		return Entry{}, false
	}
	e := el.Value.(*item).entry
	if !e.Expires.IsZero() && !c.now().Before(e.Expires) {
		return Entry{}, false
	}
	return e, true
}

// Do returns the entry of key. On a hit it returns the stored entry, and hit is
// true. On a miss it calls render, once for all the requests that miss key at
// the same time; the others wait for that call and get its result. sc is what
// the caller knows of the page before it renders ([Scope]). The result is
// stored when render says so, when no drop since the render began reaches the
// page, and when the entry fits under the limit.
//
// A request never gets a render that began before a drop that reaches the page
// and came before the request. It starts a new render instead, or, when only
// the result shows that the drop reaches the page (a member, or a result that
// may not be stored), it asks again. It waits for at most three renders.
//
// A request whose ctx ends while it waits gets ctx.Err(). The render goes on for
// the others: render must not depend on the context of one request.
func (c *Cache) Do(ctx context.Context, key string, sc Scope, render func() (Result, error)) (res Result, hit bool, err error) {
	// prev is what the last stale result depended on; stale counts them.
	var prev *deps
	stale := 0
	for first := true; ; first = false {
		if !first {
			if err := ctx.Err(); err != nil {
				return Result{}, false, err
			}
		}
		c.mu.Lock()
		if e, ok := c.getLocked(key); ok {
			c.mu.Unlock()
			if first {
				c.hits.Add(1)
			}
			return Result{Entry: e, Store: true}, true, nil
		}
		if first {
			c.misses.Add(1)
		}
		arrived := c.gen
		// Join the render in progress unless a drop since it began reaches the
		// scope it was given, or what the last stale result depended on. A drop
		// of a member cannot be matched before the render ends; the check after
		// the wait covers it.
		if f, ok := c.flights[key]; ok && stale < maxStale &&
			c.firstDropLocked(f.start, depsOf(f.scope, Entry{})) == 0 &&
			(prev == nil || c.firstDropLocked(f.start, *prev) == 0) {
			c.mu.Unlock()
			if joinHook != nil {
				joinHook(key)
			}
			select {
			case <-f.done:
			case <-ctx.Done():
				return Result{}, false, ctx.Err()
			}
			if f.err == nil && f.stale != 0 && f.stale <= arrived {
				// A drop that reaches the page came before this request.
				d := f.deps
				prev = &d
				stale++
				continue
			}
			return f.res, false, f.err
		}
		f := &flight{done: make(chan struct{}), start: arrived, scope: sc}
		c.flights[key] = f
		c.mu.Unlock()

		// The render runs in this goroutine. A panic becomes an error for the
		// caller and the waiters: a worker that renders in a goroutine of its own
		// must not end the process, and a request must not leave the waiters
		// hanging.
		f.res, f.err = safeRender(key, render)
		c.mu.Lock()
		if c.flights[key] == f {
			delete(c.flights, key)
		}
		if f.err == nil {
			f.deps = depsOf(sc, f.res.Entry)
			f.deps.all = !f.res.Store
			f.stale = c.firstDropLocked(f.start, f.deps)
			if f.res.Store && f.stale == 0 {
				c.putLocked(key, f.res.Entry)
			}
		}
		c.mu.Unlock()
		close(f.done)
		return f.res, false, f.err
	}
}

// DroppedSince reports whether a drop after generation gen ([Cache.Gen])
// reaches a page that depends on what e names: its topic, whether it is a
// listing, and its members. It reports true when gen is older than the record
// of drops.
func (c *Cache) DroppedSince(gen uint64, e Entry) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.firstDropLocked(gen, depsOf(Scope{}, e)) != 0
}

// firstDropLocked returns the generation of the first drop after generation
// after that reaches a page that depends on d, or 0 when there is none. When
// the record no longer holds the drop after after, it returns after+1: the
// cache cannot tell, so it says that a drop came.
func (c *Cache) firstDropLocked(after uint64, d deps) uint64 {
	if after >= c.gen {
		return 0
	}
	if c.gen-after > uint64(len(c.history)) {
		return after + 1
	}
	for g := after + 1; g <= c.gen; g++ {
		if c.history[(g-1)%historySize].reaches(d) {
			return g
		}
	}
	return 0
}

// ErrPanic is the error of a render that panicked. The panic and its stack are
// logged once, where it happened.
var ErrPanic = errors.New("pagecache: the render panicked")

// safeRender calls render and turns a panic into [ErrPanic].
func safeRender(key string, render func() (Result, error)) (res Result, err error) {
	defer func() {
		if p := recover(); p != nil {
			slog.Error("pagecache: a render panicked", "key", key, "panic", fmt.Sprint(p), "stack", string(debug.Stack()))
			res, err = Result{}, ErrPanic
		}
	}()
	return render()
}

// putLocked stores e under key, and drops the least recently used entries until
// the cache is under its limit again. An entry larger than the limit is not
// stored.
func (c *Cache) putLocked(key string, e Entry) {
	if e.Size > c.limit {
		return
	}
	if el, ok := c.items[key]; ok {
		c.removeLocked(el)
	}
	c.items[key] = c.order.PushFront(&item{key: key, entry: e})
	c.bytes += e.Size
	for c.bytes > c.limit {
		back := c.order.Back()
		if back == nil {
			break
		}
		c.removeLocked(back)
		c.evictions.Add(1)
	}
}

func (c *Cache) removeLocked(el *list.Element) {
	it := el.Value.(*item)
	c.order.Remove(el)
	delete(c.items, it.key)
	c.bytes -= it.entry.Size
}

// DropAll empties the cache. A change to the rights of the guests, to the board
// settings, or to anything that this process cannot see the scope of calls it.
func (c *Cache) DropAll() {
	c.drop(dropRec{kind: dropAll})
}

// DropTopic drops the pages of topic id and every listing (the index and the
// forum pages): a change to a topic shows on its own pages and in the lists that
// name it.
func (c *Cache) DropTopic(id int64) {
	c.drop(dropRec{kind: dropTopic, id: id})
}

// DropListings drops every listing: the index and the forum pages.
func (c *Cache) DropListings() {
	c.drop(dropRec{kind: dropListings})
}

// DropUser drops the pages that show a post of member id.
func (c *Cache) DropUser(id int64) {
	if id == 0 {
		return
	}
	c.drop(dropRec{kind: dropUser, id: id})
}

// drop starts a new generation, adds r to the record of drops, and removes the
// entries that r reaches. A render that began before and whose page r reaches
// does not store its result ([Cache.Do]).
func (c *Cache) drop(r dropRec) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	r.gen = c.gen
	if len(c.history) < historySize {
		c.history = append(c.history, r)
	} else {
		c.history[(r.gen-1)%historySize] = r
	}
	c.drops.Add(1)
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if r.reaches(depsOf(Scope{}, el.Value.(*item).entry)) {
			c.removeLocked(el)
		}
		el = next
	}
}
