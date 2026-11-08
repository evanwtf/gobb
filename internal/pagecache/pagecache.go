// Package pagecache holds the guest page cache (spec 10, "Load protection"): the
// pages that a guest sees, built once and served from memory.
//
// The cache knows nothing of HTTP or of the board. It keeps entries under a
// memory limit and drops the least recently used one when a new entry needs
// room. It renders each missing key once at a time, however many requests ask
// for it ([Cache.Do]). It drops entries on request: all of them, those of one
// topic, those that list topics, or those that show a member's posts.
//
// Every drop starts a new generation. A render that began before a drop does
// not store its result, because the result may hold what the drop removed. The
// requests that waited for that render still get it: they came before the drop
// finished. A request that comes after the drop starts a new render.
package pagecache

import (
	"container/list"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
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

	mu      sync.Mutex
	bytes   int64
	order   *list.List // front: most recently used; each value is *item
	items   map[string]*list.Element
	gen     uint64
	flights map[flightKey]*flight

	hits, misses, evictions, drops atomic.Uint64
}

type item struct {
	key   string
	entry Entry
}

type flightKey struct {
	key string
	gen uint64
}

// flight is one render in progress. done closes when res and err are set.
type flight struct {
	done chan struct{}
	res  Result
	err  error
}

// New returns a cache that holds at most limit bytes of entries. now gives the
// time for the expiry of the entries; nil means time.Now.
func New(limit int64, now func() time.Time) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{limit: limit, now: now, order: list.New(), items: map[string]*list.Element{}, flights: map[flightKey]*flight{}}
}

// Gen returns the generation: a number that every drop changes.
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
// the same time and in the same generation; the others wait for that call and
// get its result. The result is stored when render says so, when no drop came
// since the render began, and when the entry fits under the limit.
//
// A request whose ctx ends while it waits gets ctx.Err(). The render goes on for
// the others: render must not depend on the context of one request.
func (c *Cache) Do(ctx context.Context, key string, render func() (Result, error)) (res Result, hit bool, err error) {
	c.mu.Lock()
	if e, ok := c.getLocked(key); ok {
		c.mu.Unlock()
		c.hits.Add(1)
		return Result{Entry: e, Store: true}, true, nil
	}
	c.misses.Add(1)
	fk := flightKey{key: key, gen: c.gen}
	if f, ok := c.flights[fk]; ok {
		c.mu.Unlock()
		select {
		case <-f.done:
			return f.res, false, f.err
		case <-ctx.Done():
			return Result{}, false, ctx.Err()
		}
	}
	f := &flight{done: make(chan struct{})}
	c.flights[fk] = f
	c.mu.Unlock()

	// The render runs in this goroutine. A panic becomes an error for the caller
	// and the waiters: a worker that renders in a goroutine of its own must not
	// end the process, and a request must not leave the waiters hanging.
	f.res, f.err = safeRender(key, render)
	c.mu.Lock()
	delete(c.flights, fk)
	if f.err == nil && f.res.Store && c.gen == fk.gen {
		c.putLocked(key, f.res.Entry)
	}
	c.mu.Unlock()
	close(f.done)
	return f.res, false, f.err
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
	c.drop(func(Entry) bool { return true })
}

// DropTopic drops the pages of topic id and every listing (the index and the
// forum pages): a change to a topic shows on its own pages and in the lists that
// name it.
func (c *Cache) DropTopic(id int64) {
	c.drop(func(e Entry) bool { return e.Listing || (id != 0 && e.Topic == id) })
}

// DropListings drops every listing: the index and the forum pages.
func (c *Cache) DropListings() {
	c.drop(func(e Entry) bool { return e.Listing })
}

// DropUser drops the pages that show a post of member id.
func (c *Cache) DropUser(id int64) {
	if id == 0 {
		return
	}
	c.drop(func(e Entry) bool {
		for _, u := range e.Users {
			if u == id {
				return true
			}
		}
		return false
	})
}

// drop removes the entries for which match is true, and starts a new
// generation, so that no render that began before stores its result.
func (c *Cache) drop(match func(Entry) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.gen++
	c.drops.Add(1)
	for el := c.order.Front(); el != nil; {
		next := el.Next()
		if match(el.Value.(*item).entry) {
			c.removeLocked(el)
		}
		el = next
	}
}
