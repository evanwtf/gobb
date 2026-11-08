package web

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/pagecache"
	"github.com/evanwtf/gobb/internal/session"
)

// The guest page cache (spec 10, "Load protection").
//
// A page that a guest can see (the index, a forum page, a topic page, a static
// page) is built once, as a guest with no cookies would see it, and served from
// memory to every guest. Members always get a live page.
//
// What makes the page of one guest differ from the page of another:
//
//   - The csrf token of the forms. The cached page holds a slot that is filled
//     in for each request; a guest with no secret yet gets one, with its cookie,
//     as on a live page.
//   - The build line of the footer, which holds the time of the request. It is
//     a slot too.
//   - The color scheme (the gobb_scheme cookie, or a ?scheme= preview). The key
//     holds it, with no scheme keyed as the board default; there are three.
//   - The note of a held post (?held=1) on a forum or topic page: a slot.
//   - The page number: the key holds it. Every other query parameter is left
//     out of the key and of the page, so a random query string cannot force a
//     render. The forms of the page send the guest back to the canonical
//     address.
//   - A dead session cookie: the request clears it, then gets the guest page.
//
// The page never holds the Host of a request: the render runs on a request that
// the cache makes, with a fixed host and no headers from the visitor.
//
// The time zone of a guest is the board's, the date format of a guest is the
// standard one, and the guest has no read marks, so none of these differ between
// guests. The index shows today's birthdays, so it ends at the next midnight of
// the board's zone. A topic page shows the view count of its render, which is
// behind until a change to the topic drops the page.

// GuestCache is the guest page cache of a site, with its two workers: the
// start-up render, and the watch on the writes of other processes. Pass it in
// Options.GuestCache; [New] ties it to the site. The zero value is not usable;
// call [NewGuestCache].
type GuestCache struct {
	// Pages holds the pages.
	Pages *pagecache.Cache
	// Warm turns the start-up render on ([GuestCache.Run]).
	Warm bool

	site atomic.Pointer[site]
	kick chan struct{}

	warmRendered, warmPasses atomic.Uint64
	warmRunning              atomic.Bool

	// warmMu makes the passes of the start-up render run one at a time, and
	// guards skip.
	warmMu sync.Mutex
	// skip holds the keys that answered other than 200 or 503 (a page that a
	// guest cannot see, a redirect), or whose render panicked. The passes do not
	// render them again until a drop reaches them ([skipMark]).
	skip map[string]skipMark
}

// skipMark is why the start-up render skips a key: the generation of the cache
// before its render, and what the page depends on. A drop after gen that
// reaches the page ends the skip. A page that a guest cannot see may become one
// with a new topic, so the mark counts as a listing too: a drop of any topic or
// of the listings ends it. A render that panicked has no entry; any drop ends
// its skip (all is true).
type skipMark struct {
	gen   uint64
	entry pagecache.Entry
	all   bool
}

// ended reports whether a drop since the mark reaches the page.
func (m skipMark) ended(c *pagecache.Cache) bool {
	if m.all {
		return c.Gen() != m.gen
	}
	return c.DroppedSince(m.gen, m.entry)
}

// NewGuestCache returns a cache that holds at most limit bytes of pages. warm
// turns the start-up render on.
func NewGuestCache(limit int64, warm bool) *GuestCache {
	g := &GuestCache{Warm: warm, kick: make(chan struct{}, 1)}
	g.Pages = pagecache.New(limit, func() time.Time {
		if s := g.site.Load(); s != nil {
			return s.opt.Now()
		}
		return time.Now()
	})
	return g
}

// WarmStats are the counts of the start-up render.
type WarmStats struct {
	// Rendered counts the pages that the worker rendered and stored. Passes
	// counts the passes that began. Running is true while a pass runs.
	Rendered, Passes uint64
	Running          bool
}

// WarmStats returns the counts of the start-up render.
func (g *GuestCache) WarmStats() WarmStats {
	return WarmStats{Rendered: g.warmRendered.Load(), Passes: g.warmPasses.Load(), Running: g.warmRunning.Load()}
}

// cacheKind names the routes whose pages the cache keeps.
type cacheKind byte

const (
	cacheIndex  cacheKind = 'i'
	cacheForum  cacheKind = 'f'
	cacheTopic  cacheKind = 't'
	cacheStatic cacheKind = 'p'
)

// maxSlugKey is the longest slug of a static page that the cache keys on. A
// longer one is no page; it gets the live 404.
const maxSlugKey = 200

// cacheKey is the canonical key of a guest page.
type cacheKey struct {
	kind cacheKind
	id   int64
	slug string
	page int
	// scheme is the scheme that the request asks for, or the board default.
	scheme string
}

func (k cacheKey) String() string {
	var b strings.Builder
	b.WriteByte(byte(k.kind))
	b.WriteByte('|')
	b.WriteString(k.scheme)
	b.WriteByte('|')
	switch k.kind {
	case cacheForum, cacheTopic:
		b.WriteString(strconv.FormatInt(k.id, 10))
		b.WriteByte('|')
		b.WriteString(strconv.Itoa(k.page))
	case cacheStatic:
		b.WriteString(k.slug)
	}
	return b.String()
}

// target returns the path and query of the page of k, under the base path, as
// the routes see a request (the base path removed).
func (k cacheKey) target() *url.URL {
	u := &url.URL{}
	q := url.Values{}
	switch k.kind {
	case cacheIndex:
		u.Path = "/"
	case cacheForum, cacheTopic:
		u.Path = "/" + string(k.kind) + "/" + strconv.FormatInt(k.id, 10)
		if k.page > 1 {
			q.Set("page", strconv.Itoa(k.page))
		}
	case cacheStatic:
		u.Path = "/pages/" + k.slug
	}
	u.RawQuery = q.Encode()
	return u
}

// scope returns what the page of k depends on before it renders: its topic, and
// whether it is a listing. A static page depends on neither: only a drop of
// every page reaches it.
func (k cacheKey) scope() pagecache.Scope {
	sc := pagecache.Scope{Listing: k.kind == cacheIndex || k.kind == cacheForum}
	if k.kind == cacheTopic {
		sc.Topic = k.id
	}
	return sc
}

// keyFor returns the key of the guest page of r, and false when the request is
// not one the cache serves: a malformed ID or page number goes to the live
// handler, which answers it.
func (s *site) keyFor(kind cacheKind, r *http.Request) (cacheKey, bool) {
	q := r.URL.Query()
	k := cacheKey{kind: kind, scheme: s.defaultScheme()}
	if v := q.Get("scheme"); board.ValidScheme(v) {
		k.scheme = v
	} else if c, err := r.Cookie(schemeCookie); err == nil && board.ValidScheme(c.Value) {
		k.scheme = c.Value
	}
	switch kind {
	case cacheForum, cacheTopic:
		id, ok := pathID(r, "id")
		if !ok {
			return cacheKey{}, false
		}
		page, err := board.ParsePage(q.Get("page"))
		if err != nil {
			return cacheKey{}, false
		}
		k.id, k.page = id, page
	case cacheStatic:
		k.slug = r.PathValue("slug")
		if k.slug == "" || len(k.slug) > maxSlugKey {
			return cacheKey{}, false
		}
	}
	return k, true
}

// defaultScheme returns the board's default scheme from the settings in
// memory, so a guest with no scheme and a guest who picked the default share a
// key. A change of the setting drops every page (an admin write).
func (s *site) defaultScheme() string {
	if f := s.frame.Load(); f != nil && board.ValidScheme(f.settings.DefaultScheme) {
		return f.settings.DefaultScheme
	}
	return board.Schemes[0]
}

// renderKey is the context key of the note of a render for the cache. Its
// presence tells the handlers that the request is the cache's own render.
type renderKey struct{}

// renderNote is what a handler tells the cache about the page it rendered.
type renderNote struct {
	// users are the members whose posts the page shows.
	users []int64
	// pages is the number of pages of a topic.
	pages int
	// expires is when the page stops being right, or zero.
	expires time.Time
}

// noteFrom returns the note of a render for the cache, or nil for any other
// request. ctx may be nil.
func noteFrom(ctx context.Context) *renderNote {
	if ctx == nil {
		return nil
	}
	n, _ := ctx.Value(renderKey{}).(*renderNote)
	return n
}

// The slots of a cached page.
const (
	slotCSRF = iota
	slotFooter
	// slotHeld is the note of a held post, which a ?held=1 request shows.
	slotHeld
	slotCount
)

// heldSlot returns the slot of the held note for a render for the cache, and
// "" for any other request: the page then shows the note itself.
func (s *site) heldSlot(ctx context.Context) string {
	if noteFrom(ctx) == nil {
		return ""
	}
	return s.slotTokens[slotHeld]
}

// cachedPage is the value of a cache entry: an answer of a guest page, with the
// slots that each request fills in.
type cachedPage struct {
	status int
	header http.Header
	// parts are the body between the slots: slots[i] goes between parts[i] and
	// parts[i+1].
	parts [][]byte
	slots []int
	// hash identifies the body less its slots, for the ETag.
	hash string
	// pages is the number of pages of a topic page, for the start-up render.
	pages int
}

func (cp *cachedPage) hasCSRF() bool {
	for _, s := range cp.slots {
		if s == slotCSRF {
			return true
		}
	}
	return false
}

// newSlotTokens returns the strings that stand for the slots in a render:
// random per process, so no text of a post can hold one.
func newSlotTokens() ([slotCount]string, error) {
	var out [slotCount]string
	for i, name := range []string{"csrf", "footer", "held"} {
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			return out, err
		}
		out[i] = "gobbslot" + name + hex.EncodeToString(b)
	}
	return out, nil
}

// splitSlots cuts body at each slot token.
func splitSlots(body []byte, tokens [slotCount]string) (parts [][]byte, slots []int) {
	for {
		at, which := -1, -1
		for i, t := range tokens {
			if j := bytes.Index(body, []byte(t)); j >= 0 && (at < 0 || j < at) {
				at, which = j, i
			}
		}
		if at < 0 {
			return append(parts, body), slots
		}
		parts = append(parts, body[:at])
		slots = append(slots, which)
		body = body[at+len(tokens[which]):]
	}
}

// pageRecorder is the ResponseWriter of a render for the cache.
type pageRecorder struct {
	header http.Header
	status int
	body   bytes.Buffer
}

func (r *pageRecorder) Header() http.Header { return r.header }

func (r *pageRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
}

func (r *pageRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	return r.body.Write(b)
}

// cached serves the route kind from the guest page cache. A member, a request
// that is not GET or HEAD, and a request whose key cannot be read go to h. With
// no cache, it returns h.
func (s *site) cached(kind cacheKind, h http.HandlerFunc) http.HandlerFunc {
	if s.guest == nil {
		return h
	}
	s.cacheHandlers[kind] = h
	return func(w http.ResponseWriter, r *http.Request) {
		if noteFrom(r.Context()) != nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
			h(w, r)
			return
		}
		k, ok := s.keyFor(kind, r)
		if !ok {
			h(w, r)
			return
		}
		id, err := s.identify(w, r)
		if err != nil {
			// The live handler meets the same error and answers it.
			h(w, r)
			return
		}
		if id.token != "" {
			// A member: the live page, with the session already looked up.
			h(w, r.WithContext(context.WithValue(r.Context(), identityKey{}, id)))
			return
		}
		e, _, err := s.guestPage(r.Context(), context.Background(), k)
		switch {
		case errors.Is(err, pagecache.ErrPanic):
			// The handler panicked; the cache logged it. This request gets a 500,
			// as a live page that fails to build does.
			s.renderFailed(w, http.StatusInternalServerError)
			return
		case err != nil:
			// The request ended while it waited for the render of another: its
			// client left, or its deadline passed. No log line: under a flood
			// there are many.
			s.poolBusy(w)
			return
		}
		cp := e.Value.(*cachedPage)
		s.serveCached(w, r, id, cp)
		if kind == cacheTopic && cp.status == http.StatusOK && isNavigation(r) {
			s.addView(r.Context(), k.id)
		}
	}
}

// guestPage returns the entry of the page of k from the cache, rendering it on
// a miss; its Value is a *cachedPage. ctx
// is the wait of the caller. A render that this call starts runs under parent,
// with its own deadline: a request passes context.Background, so no request
// cuts short a render that others wait for; the start-up render passes its own
// context, so the render stops when the server does.
func (s *site) guestPage(ctx, parent context.Context, k cacheKey) (pagecache.Entry, bool, error) {
	res, hit, err := s.guest.Pages.Do(ctx, k.String(), k.scope(), func() (pagecache.Result, error) {
		return s.renderGuest(parent, k)
	})
	if err != nil {
		return pagecache.Entry{}, false, err
	}
	return res.Entry, hit, nil
}

// renderGuest renders the page of k as a guest with no cookie but the scheme
// would see it, on a request that the cache makes. The render has its own
// deadline and pool wait: the requests that wait for it do not share one
// context, and none of them may cut it short for the others.
func (s *site) renderGuest(parent context.Context, k cacheKey) (pagecache.Result, error) {
	h := s.cacheHandlers[k.kind]
	ctx, cancel := context.WithTimeout(parent, requestTimeout)
	defer cancel()
	ctx = db.WithPoolWait(ctx, s.opt.PoolWait)
	ctx, _ = db.WithCounter(withClock(ctx, http.MethodGet))
	note := &renderNote{}
	ctx = context.WithValue(ctx, renderKey{}, note)
	u := k.target()
	r, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return pagecache.Result{}, err
	}
	// No header and no cookie of a visitor reaches the render. The host is
	// fixed, so no Host header of a request can reach the page.
	r.Host = "gobb.invalid"
	r.RemoteAddr = "192.0.2.1:1"
	r.AddCookie(&http.Cookie{Name: schemeCookie, Value: k.scheme})
	switch k.kind {
	case cacheForum, cacheTopic:
		r.SetPathValue("id", strconv.FormatInt(k.id, 10))
	case cacheStatic:
		r.SetPathValue("slug", k.slug)
	}
	rec := &pageRecorder{header: http.Header{}}
	if beforeGuestRender != nil {
		beforeGuestRender(k.String())
	}
	h(&poolBusyWriter{ResponseWriter: rec, ctx: ctx}, r)
	if rec.status == 0 {
		rec.status = http.StatusOK
	}
	hdr := rec.header.Clone()
	hdr.Del("Set-Cookie")
	hdr.Del("Content-Length")
	// A copy of the exact size: the buffer of the recorder has room to spare.
	body := bytes.Clone(rec.body.Bytes())
	parts, slots := splitSlots(body, s.slotTokens)
	sum := sha256.New()
	for i, p := range parts {
		sum.Write(p)
		if i < len(slots) {
			sum.Write([]byte{0, byte(slots[i])})
		}
	}
	cp := &cachedPage{status: rec.status, header: hdr, parts: parts, slots: slots, hash: hex.EncodeToString(sum.Sum(nil)), pages: note.pages}
	size := entrySize(k, body, parts, slots, hdr, note.users)
	sc := k.scope()
	e := pagecache.Entry{
		Value: cp, Size: size,
		Topic: sc.Topic, Listing: sc.Listing,
		Users: note.users, Expires: note.expires,
	}
	// Only a whole page is kept. An error page, a redirect and a 503 go to the
	// requests that waited for this render, and the next request renders again.
	return pagecache.Result{Entry: e, Store: rec.status == http.StatusOK}, nil
}

// entryOverhead is the memory of an entry besides its body and the sizes that
// entrySize counts: the list element, the map entry, the item, the Entry and
// the cachedPage, with the header map. With it, the count of 353 topic pages
// of a test board was 103% of the growth of the Go heap after a GC.
const entryOverhead = 1536

// entrySize returns the bytes that an entry counts against the limit: the body,
// the slice headers of its parts, the header values, the users, and the key
// twice (the map and the item hold it).
func entrySize(k cacheKey, body []byte, parts [][]byte, slots []int, hdr http.Header, users []int64) int64 {
	size := int64(len(body)) + int64(cap(body)-len(body)) + int64(len(parts))*24 + int64(len(slots))*8 +
		int64(len(users))*8 + 2*int64(len(k.String())) + entryOverhead
	for name, vs := range hdr {
		size += int64(len(name)) + 16
		for _, v := range vs {
			size += int64(len(v)) + 16
		}
	}
	return size
}

// serveCached writes the cached answer cp to the request r of the guest id,
// with the slots filled in for this request. A page of status 200 carries an
// ETag, and a request whose If-None-Match holds it gets 304.
func (s *site) serveCached(w http.ResponseWriter, r *http.Request, id identity, cp *cachedPage) {
	h := w.Header()
	for name, vs := range cp.header {
		h[name] = append([]string(nil), vs...)
	}
	held := r.URL.Query().Get("held") == "1"
	token := ""
	if cp.hasCSRF() {
		secret := id.guestSecret
		if secret == "" {
			var err error
			if secret, err = newCSRFSecret(); err != nil {
				slog.Error("guest page: csrf secret", "err", err)
				s.renderFailed(w, http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, s.cookie(csrfCookie, secret, 0))
		}
		token = session.CSRFToken(secret)
	}
	if cp.status == http.StatusOK {
		// The tag covers the page and the token. The build line is left out: it
		// holds only the time of the request, which a kept copy may show old.
		sum := sha256.Sum256([]byte(cp.hash + "\x00" + token + "\x00" + strconv.FormatBool(held)))
		etag := `W/"` + hex.EncodeToString(sum[:12]) + `"`
		h.Set("ETag", etag)
		if etagMatch(r.Header.Get("If-None-Match"), etag) {
			h.Del("Content-Type")
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.WriteHeader(cp.status)
	if r.Method == http.MethodHead {
		return
	}
	// The parts go straight to w, with no copy of the page: a flood of hits
	// costs no memory per page. The ETag above comes from the hash of the
	// stored parts, the token and held, so it needs no joined body.
	_, _ = w.Write(cp.parts[0])
	for i, slot := range cp.slots {
		switch slot {
		case slotCSRF:
			_, _ = io.WriteString(w, template.HTMLEscapeString(token))
		case slotFooter:
			_, _ = io.WriteString(w, template.HTMLEscapeString(footerLine(r.Context(), cp.status, false)))
		case slotHeld:
			if held {
				_, _ = io.WriteString(w, s.heldNote)
			}
		}
		_, _ = w.Write(cp.parts[i+1])
	}
}

// etagMatch reports whether the If-None-Match header value list holds etag, by
// the weak comparison of RFC 9110: the W/ prefix does not count.
func etagMatch(list, etag string) bool {
	want := strings.TrimPrefix(etag, "W/")
	for _, v := range strings.Split(list, ",") {
		v = strings.TrimSpace(v)
		if v == "*" || strings.TrimPrefix(v, "W/") == want {
			return true
		}
	}
	return false
}

// The drops. Each write of a handler that changes what a guest may see calls
// one of them once it has written. They do nothing without a cache. Each one
// also wakes the start-up render, which renders the dropped pages again.

// changedAll drops every page: a change to the board settings, the rights, the
// forums, a member's name or rank, a static page, or anything else whose
// reach the site does not follow.
func (s *site) changedAll() {
	if s.guest == nil {
		return
	}
	s.guest.Pages.DropAll()
	s.guest.wake()
}

// changedTopic drops the pages of topic id and every listing.
func (s *site) changedTopic(id int64) {
	if s.guest == nil {
		return
	}
	s.guest.Pages.DropTopic(id)
	s.guest.wake()
}

// changedListings drops every listing: a new topic.
func (s *site) changedListings() {
	if s.guest == nil {
		return
	}
	s.guest.Pages.DropListings()
	s.guest.wake()
}

// changedUser drops the pages that show a post of member id: a change to the
// post count, the signature or the avatar of the member.
func (s *site) changedUser(id int64) {
	if s.guest == nil || id == 0 {
		return
	}
	s.guest.Pages.DropUser(id)
	s.guest.wake()
}

// lateCommitDelay is the wait before the second drop of a write that returned
// an error. Such a write may still commit (a timeout during the COMMIT), after
// the first drop, and a render between the two would keep the old page. The
// tests shorten it.
var lateCommitDelay = 2 * time.Second

// afterWrite runs drop after a board write that returned err, when the write
// may have changed the board ([wrote]). When err is not nil, it runs drop once
// more after lateCommitDelay.
func (s *site) afterWrite(err error, drop func()) {
	if s.guest == nil || !wrote(err) {
		return
	}
	drop()
	if err != nil {
		time.AfterFunc(lateCommitDelay, drop)
	}
}

// postScope is what a change to one post reaches: its topic, and its author,
// whose post count shows on every page with one of their posts.
type postScope struct {
	topic, author int64
	found         bool
}

// postScopeOf reads the scope of post id, before the write that changes it.
// With no cache it reads nothing.
func (s *site) postScopeOf(ctx context.Context, id int64) postScope {
	if s.guest == nil {
		return postScope{}
	}
	topic, author, _, err := board.PostRefs(ctx, s.pool, id)
	if err != nil {
		if !errors.Is(err, board.ErrNotFound) {
			slog.Warn("guest page cache: find the topic of a post", "post", id, "err", err)
		}
		return postScope{}
	}
	return postScope{topic: topic, author: author, found: true}
}

// changedScope drops the pages that sc reaches: its topic, the listings, and
// the pages of its author. A scope that was not found drops every page.
func (s *site) changedScope(sc postScope) {
	if s.guest == nil {
		return
	}
	if !sc.found {
		s.changedAll()
		return
	}
	s.guest.Pages.DropTopic(sc.topic)
	s.guest.Pages.DropUser(sc.author)
	s.guest.wake()
}

// heldPost reports whether post id waits for approval. Such a post changes
// nothing that a guest sees: not the topic, not the counts. A failed read says
// false, so the caller drops.
func (s *site) heldPost(ctx context.Context, id int64) bool {
	_, _, visibility, err := board.PostRefs(ctx, s.pool, id)
	return err == nil && visibility == "unapproved"
}

// wrote reports whether a board write that returned err may have changed the
// board. A refusal (a missing target, no right, a bad value) wrote nothing. Any
// other error, such as a timeout after the commit, may have written: the cache
// drops then, to be safe.
func wrote(err error) bool {
	if err == nil {
		return true
	}
	var in *board.InputError
	switch {
	case errors.As(err, &in), errors.Is(err, board.ErrNotFound), errors.Is(err, board.ErrForbidden),
		errors.Is(err, board.ErrTopicLocked), errors.Is(err, board.ErrForumLocked), errors.Is(err, board.ErrBadKind):
		return false
	}
	return true
}

// adminWrite runs h, the handler of a POST in the admin area, then drops every
// page. An admin write can change anything a guest sees (the settings, the
// rights, the forums, the members, the static pages, the bans), and only an
// admin reaches it, so the drop costs no visitor anything.
func (s *site) adminWrite(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		defer s.changedAll()
		h(w, r)
	}
}

// wake starts a pass of the start-up render, without blocking.
func (g *GuestCache) wake() {
	select {
	case g.kick <- struct{}{}:
	default:
	}
}

// The start-up render and the watch on other processes.

const (
	// epochEvery is the time between two reads of the page cache epoch.
	epochEvery = 5 * time.Second
	// warmSettle is the time that the start-up render waits after a drop before
	// it renders again, so a burst of writes costs one pass.
	warmSettle = 2 * time.Second
	// warmBackoff is the first wait of the start-up render when the pool is
	// busy; each busy look doubles it, up to warmBackoffMax.
	warmBackoff    = 100 * time.Millisecond
	warmBackoffMax = 5 * time.Second
	// warmBatch is the number of topics that one query of the start-up render
	// lists.
	warmBatch = 100
)

// beforeGuestRender, when set, runs before each render for the cache with its
// key. A test makes it panic.
var beforeGuestRender func(key string)

// onWarm, when set, sees the key of each page that the start-up render stores.
// A test sets it.
var onWarm func(key string)

// Run runs the workers of the cache until ctx ends: the watch on the page cache
// epoch, which empties the cache when another process has written the board,
// and, when Warm is on, the start-up render. It returns when both have stopped.
// The site must have been built with this cache.
func (g *GuestCache) Run(ctx context.Context) {
	s := g.site.Load()
	if s == nil {
		return
	}
	// The first read of the epoch comes before the start-up render, so its drop
	// does not throw away the first pages.
	e := &epochWatch{g: g, pool: s.pool}
	e.check(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.watch(ctx, epochEvery)
	}()
	if g.Warm {
		g.warmLoop(ctx, s)
	}
	<-done
}

// watch reads the page cache epoch every interval until ctx ends, and empties
// the cache when it changed ([epochWatch.check]).
func (e *epochWatch) watch(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			e.check(ctx)
		}
	}
}

// epochWatch follows the page cache epoch, which the commands of other
// processes change after they write the board ([board.BumpPageEpoch]).
type epochWatch struct {
	g     *GuestCache
	pool  *pgxpool.Pool
	last  int64
	known bool
}

// check reads the epoch and empties the cache when it is not the value of the
// last read. The first read that works empties the cache too: a write may have
// come before it. A read that fails leaves the cache as it is.
func (e *epochWatch) check(ctx context.Context) {
	rctx, cancel := context.WithTimeout(ctx, epochEvery)
	defer cancel()
	v, err := board.PageEpoch(db.WithPoolWait(rctx, time.Second), e.pool)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("guest page cache: cannot read the epoch", "err", err)
		}
		return
	}
	if e.known && v == e.last {
		return
	}
	e.g.Pages.DropAll()
	if e.known {
		// The first read comes before the first pass, which renders what it
		// needs: no second pass.
		slog.Info("guest page cache: another process changed the board; emptying the cache")
		e.g.wake()
	}
	e.last, e.known = v, true
}

// warmLoop runs a pass of the start-up render, then one more after each drop,
// until ctx ends.
func (g *GuestCache) warmLoop(ctx context.Context, s *site) {
	for {
		g.warmPass(ctx, s)
		select {
		case <-ctx.Done():
			return
		case <-g.kick:
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(warmSettle):
		}
		// The drops in the wait need no pass of their own.
		select {
		case <-g.kick:
		default:
		}
	}
}

// warmPass renders the pages that a guest is most likely to ask for, in order,
// until the cache is full: the index, the first page of each forum, then the
// pages of the topics by latest activity. A page in the cache already is not
// rendered again. It renders one page at a time, and waits while the pool is
// busy, so the pages of the visitors keep their connections.
func (g *GuestCache) warmPass(ctx context.Context, s *site) {
	g.warmMu.Lock()
	defer g.warmMu.Unlock()
	g.warmPasses.Add(1)
	g.warmRunning.Store(true)
	defer g.warmRunning.Store(false)
	if g.skip == nil {
		g.skip = map[string]skipMark{}
	}
	for key, m := range g.skip {
		if m.ended(g.Pages) {
			delete(g.skip, key)
		}
	}
	scheme := s.defaultScheme()
	var largest int64
	// render renders k when it is missing and returns its page count; ok is false
	// when the pass must end.
	render := func(k cacheKey) (pages int, ok bool) {
		k.scheme = scheme
		key := k.String()
		if e, hit := g.Pages.Peek(key); hit {
			return e.Value.(*cachedPage).pages, true
		}
		if m, ok := g.skip[key]; ok {
			if !m.ended(g.Pages) {
				return 0, true
			}
			delete(g.skip, key)
		}
		for {
			st := g.Pages.Stats()
			if st.Bytes+largest > g.Pages.Limit() {
				return 0, false
			}
			if !waitPool(ctx, s.pool) {
				return 0, false
			}
			gen := g.Pages.Gen()
			e, _, err := s.guestPage(ctx, ctx, k)
			if errors.Is(err, pagecache.ErrPanic) {
				// The cache logged the panic. The page is skipped until a drop;
				// the pass goes on.
				g.skip[key] = skipMark{gen: gen, all: true}
				return 0, true
			}
			if err != nil {
				return 0, false
			}
			if ctx.Err() != nil {
				return 0, false
			}
			cp := e.Value.(*cachedPage)
			if cp.status == http.StatusServiceUnavailable {
				// The pool was busy after all: wait and try again.
				if !sleepCtx(ctx, warmBackoff) {
					return 0, false
				}
				continue
			}
			if kept, stored := g.Pages.Peek(key); stored {
				largest = max(largest, kept.Size)
				g.warmRendered.Add(1)
				if onWarm != nil {
					onWarm(key)
				}
			} else if cp.status != http.StatusOK {
				m := skipMark{gen: gen, entry: pagecache.Entry{Topic: e.Topic, Listing: true, Users: e.Users}}
				// A drop during the render that reaches the page may have
				// changed the answer: no skip then.
				if !m.ended(g.Pages) {
					g.skip[key] = m
				}
			}
			return cp.pages, true
		}
	}
	if _, ok := render(cacheKey{kind: cacheIndex}); !ok {
		return
	}
	forums, readable, err := guestForums(ctx, s)
	if err != nil {
		if ctx.Err() == nil {
			slog.Warn("guest page cache: start-up render: list the forums", "err", err)
		}
		return
	}
	for _, id := range forums {
		if _, ok := render(cacheKey{kind: cacheForum, id: id, page: 1}); !ok {
			return
		}
	}
	var after board.TopicByActivity
	for {
		if !waitPool(ctx, s.pool) {
			return
		}
		lctx, cancel := context.WithTimeout(ctx, requestTimeout)
		topics, err := board.TopicsByActivity(db.WithPoolWait(lctx, s.opt.PoolWait), s.pool, readable, after, warmBatch)
		cancel()
		if err != nil {
			if ctx.Err() == nil {
				slog.Warn("guest page cache: start-up render: list the topics", "err", err)
			}
			return
		}
		for _, t := range topics {
			pages, ok := render(cacheKey{kind: cacheTopic, id: t.ID, page: 1})
			if !ok {
				return
			}
			for p := 2; p <= pages; p++ {
				if _, ok := render(cacheKey{kind: cacheTopic, id: t.ID, page: p}); !ok {
					return
				}
			}
		}
		if len(topics) < warmBatch {
			return
		}
		after = topics[len(topics)-1]
	}
}

// guestForums returns the forums that have pages, and those whose topics a
// guest may read.
func guestForums(ctx context.Context, s *site) (forums, readable []int64, err error) {
	lctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	lctx = db.WithPoolWait(lctx, s.opt.PoolWait)
	if forums, err = board.ForumIDs(lctx, s.pool); err != nil {
		return nil, nil, err
	}
	set, err := board.LoadSettings(lctx, s.pool)
	if err != nil {
		return nil, nil, err
	}
	guest, err := board.Guest(lctx, s.pool, set)
	if err != nil {
		return nil, nil, err
	}
	for _, id := range forums {
		if guest.Perms.Has("forum.read", id) {
			readable = append(readable, id)
		}
	}
	return forums, readable, nil
}

// poolBusy reports whether the pool has no connection to spare for the
// start-up render: all but one are in use (all, for a pool of one).
func poolIsBusy(pool *pgxpool.Pool) bool {
	st := pool.Stat()
	return st.AcquiredConns() >= max(1, st.MaxConns()-1)
}

// waitPool waits until the pool is not busy, with a growing pause. It returns
// false when ctx ends first.
func waitPool(ctx context.Context, pool *pgxpool.Pool) bool {
	wait := warmBackoff
	for poolIsBusy(pool) {
		if !sleepCtx(ctx, wait) {
			return false
		}
		wait = min(2*wait, warmBackoffMax)
	}
	return ctx.Err() == nil
}

// sleepCtx waits for d, and returns false when ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// nextMidnight returns the start of the day after now, in loc.
func nextMidnight(now time.Time, loc *time.Location) time.Time {
	t := now.In(loc)
	return time.Date(t.Year(), t.Month(), t.Day()+1, 0, 0, 0, 0, loc)
}

// serveGuestHit serves a guest page from the cache before the in-flight
// limits, and reports whether it did. It serves only a GET or HEAD with no
// session cookie (a session, even a forged or dead one, needs a database
// lookup), on a clean path of a cached route (the index, a forum, a topic or a
// static page), whose canonical key ([site.keyFor]) is in the cache. It looks
// the key up with [pagecache.Cache.Lookup], never Do, so it never renders and
// never waits. It fills the slots and answers If-None-Match as the cached
// route does ([site.serveCached]), and counts the hit in
// gobb_page_cache_hits_total. The security headers come from [secure], which
// wraps both paths.
//
// It needs the batched writes: the view of a topic then goes to memory. With
// no batched writes a view writes the database, so it serves nothing.
func (s *site) serveGuestHit(w http.ResponseWriter, r *http.Request) bool {
	if s.guest == nil || s.writes == nil || (r.Method != http.MethodGet && r.Method != http.MethodHead) {
		return false
	}
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		return false
	}
	// A path with escapes goes the slotted way: the mux may read it otherwise.
	if r.URL.RawPath != "" || !isCleanPath(r.URL.Path) {
		return false
	}
	rest, ok := strings.CutPrefix(r.URL.Path, s.base)
	if !ok {
		return false
	}
	var (
		kind      cacheKind
		route     string
		name, val string
	)
	if rest == "" {
		kind, route = cacheIndex, "/{$}"
	} else {
		seg, v, ok := strings.Cut(rest, "/")
		if !ok || v == "" || strings.Contains(v, "/") {
			return false
		}
		switch seg {
		case "f":
			kind, route, name = cacheForum, "/f/{id}", "id"
		case "t":
			kind, route, name = cacheTopic, "/t/{id}", "id"
		case "pages":
			kind, route, name = cacheStatic, "/pages/{slug}", "slug"
		default:
			return false
		}
		val = v
	}
	r2 := new(http.Request)
	*r2 = *r
	if name != "" {
		r2.SetPathValue(name, val)
	}
	k, ok := s.keyFor(kind, r2)
	if !ok {
		return false
	}
	e, ok := s.guest.Pages.Lookup(k.String())
	if !ok {
		return false
	}
	cp := e.Value.(*cachedPage)
	if rec, ok := r.Context().Value(routeKey{}).(*routeRec); ok {
		rec.route = route
	}
	var id identity
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) <= 128 {
		id.guestSecret = c.Value
	}
	s.serveCached(w, r2, id, cp)
	if kind == cacheTopic && cp.status == http.StatusOK && isNavigation(r) {
		s.addView(r.Context(), k.id)
	}
	return true
}
