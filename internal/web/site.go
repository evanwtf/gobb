package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/batch"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/legacy"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/metrics"
	"github.com/evanwtf/gobb/internal/ratelimit"
	"github.com/evanwtf/gobb/internal/session"
	"github.com/evanwtf/gobb/markup"
)

//go:embed templates/*.html
var templateFS embed.FS

//go:embed static
var staticFS embed.FS

// Options configures a site.
type Options struct {
	// BasePath is the URL path prefix of the site. It starts and ends with "/".
	// The empty string means "/".
	BasePath string
	// DataDir is the data directory that holds the files the site serves.
	DataDir string
	// TrustedProxies lists the proxies whose forwarding headers the site trusts.
	TrustedProxies []netip.Prefix
	// CookieSecure marks the cookies of the site Secure.
	CookieSecure bool
	// Now returns the current time. The default is time.Now.
	Now func() time.Time
	// LoginFloor is the least time that a failed sign-in takes. Zero means the
	// floor that New times at start (login.Service.LoadFloor): at least 400 ms.
	// Tests set it, and skip the timing.
	LoginFloor time.Duration
	// LoginGate is the memory gate of the password checks. Nil means a gate
	// with the default budget. cmd/gobb sizes it to the memory limit of the
	// process (login.BudgetKiB). Tests use it to fill the gate.
	LoginGate *login.Gate
	// MemberPosts, GuestPosts and NameChecks are the counts of the limits on the
	// posting forms in ten minutes: POSTs for each member, POSTs for each guest
	// address, and guest-name checks for each address. Zero means the default
	// (60, 20 and 10). Tests that post a lot raise them.
	MemberPosts, GuestPosts, NameChecks int
	// MemberSaves is the count of saves of the account settings that each member
	// may make in ten minutes. Zero means the default (120). A test that saves a
	// lot raises it.
	MemberSaves int
	// MessageSends is the count of messages that each member may send in ten
	// minutes. Zero means the default (10). A test that sends a lot raises it.
	MessageSends int
	// LoginGateWait is how long a password check waits for the gate. Zero means
	// [login.DefaultGateWait] (1 s). Tests set it small.
	LoginGateWait time.Duration
	// Mail turns the password reset by email on. Nil turns it off: the reset
	// routes answer 404 and the sign-in page has no link to them.
	Mail *MailOptions
	// ResetFloor is the least time that a request for a reset takes. Zero means
	// 400 ms. Tests set it lower.
	ResetFloor time.Duration
	// AdminWrites is the count of POSTs to the admin area that each admin may
	// make in ten minutes. Zero means the default (120). A test sets it low.
	AdminWrites int
	// PublicURL is the address of the site: absolute, with the base path as its
	// path. The sitemap, robots.txt and the canonical links take their scheme and
	// host from it. Empty means the PublicURL of Mail, and with no Mail, the
	// host of each request.
	PublicURL string
	// PoolWait is the longest time that a request waits for a database
	// connection. A request that waits longer gets 503 with Retry-After. Zero
	// means 1 s.
	PoolWait time.Duration
	// SearchPerSecond is the most searches that the whole board runs in any
	// second. Zero means 5.
	SearchPerSecond int
	// SearchMaxRunning is the most searches that run at one time. Zero means 2.
	SearchMaxRunning int
	// RefuseSearch, when set, sees each search before the limits of search, and
	// may refuse it with the page of a search over the limit, and a Retry-After
	// of retry. It must not touch the database. Nil means the check of
	// Defensive Mode: a search with no session cookie while Defensive Mode is on
	// ([site.refuseGuestSearch]). A guest with a made-up cookie passes it; the
	// search checks the viewer again after the session lookup.
	RefuseSearch func(r *http.Request) (refuse bool, retry time.Duration)
	// Signin is the sign-in schedule. Nil means the owner's defaults
	// ([login.DefaultSchedule]).
	Signin *login.ScheduleConfig
	// Defense is Defensive Mode. Nil means a new one with the defaults. The
	// caller that passes one refreshes it on a timer ([login.Defense.Refresh]).
	Defense *login.Defense
	// MaxInFlight is the most requests that the server handles at one time.
	// A request past it gets 503 at once ([site.limitInFlight]). Zero means
	// [DefaultMaxInFlight]; a negative value turns the limit off.
	MaxInFlight int
	// GuestCache is the guest page cache. Nil turns it off: every page is
	// rendered for its request. One cache serves one site.
	GuestCache *GuestCache
	// Writes collects the view counts, the activity times of members and the
	// addresses of their visits, for a flush on a timer ([batch.Writer.Run]).
	// Nil writes each one at once, in the request.
	Writes *batch.Writer
}

// The defaults of Options.PoolWait, SearchPerSecond and SearchMaxRunning.
const (
	defaultPoolWait         = time.Second
	defaultSearchPerSecond  = 5
	defaultSearchMaxRunning = 2
)

// site holds what the handlers share: the pool, the options and the parsed
// templates.
type site struct {
	pool *pgxpool.Pool
	opt  Options
	base string
	// pages maps a page template name to its parsed set, which holds the layout.
	pages map[string]*template.Template
	// noDataLogged is set once the WARN line for a board with no data is
	// written, and cleared when a request finds the board again.
	noDataLogged atomic.Bool
	// assetVersion changes when the stylesheet or the script changes. Page links
	// carry it, so a browser fetches the new file.
	assetVersion string
	// login checks the sign-ins.
	login *login.Service
	// crossOrigin guards every POST route. crossOriginPosting guards the posting
	// forms, whose refusal never echoes the body.
	crossOrigin, crossOriginPosting *http.CrossOriginProtection
	// legacy holds the old routes and file prefixes of the board. It is read
	// once, in New: the server stops for an import.
	legacy *legacy.Table
	// frame is the copy of the settings that an error page needs. It lives in
	// memory, so an error page needs no database. New loads it, and every page
	// that reads the settings replaces it when they change. It is nil for a
	// database with no board.
	frame atomic.Pointer[frame]
	// tries counts the tries at the account changes that need the current
	// password, per member and form.
	tries *ratelimit.Limiter
	// postLimits counts the POSTs of the posting forms and the guest-name checks.
	postLimits postLimits
	// saves counts the saves of the settings and the avatar uploads of each member.
	saves saveLimits
	// messageSends counts the messages that each member sends.
	messageSends *ratelimit.Limiter
	// resets counts the requests for a password reset and the hits on dead links.
	resets resetLimits
	// gate is the memory gate of the password checks. An avatar decode takes its
	// share from the same gate, so one budget covers both.
	gate *login.Gate
	// uploadSlots is the semaphore of the avatar uploads that are in flight: each
	// one holds up to a megabyte of body in memory before the decode takes its
	// share of the gate. A test makes it smaller.
	uploadSlots chan struct{}
	// inFlight is the semaphore of the requests under way, or nil for no
	// limit ([site.limitInFlight]).
	inFlight chan struct{}
	// uploading holds the IDs of the members who have an avatar upload in flight:
	// a member has one at a time.
	uploading sync.Map
	// patterns lists every pattern registered on the muxes of the site, for a test
	// that checks the routes against its tables.
	patterns []string
	// staticErrors holds the themed 500 and 503 pages. New builds them, and a save
	// of the board settings builds them again ([site.refreshFrame]). A request
	// reads the map it finds and never changes it. They answer when even the error
	// template fails.
	staticErrors atomic.Pointer[map[int][]byte]
	// refreshing makes the rebuilds of the frame and the static pages run one
	// after the other.
	refreshing sync.Mutex
	// adminWrites counts the POSTs of each admin to the admin area.
	adminWrites *ratelimit.Limiter
	// mailTests counts the test emails of each admin: one a minute.
	mailTests *ratelimit.Limiter
	// passes holds the sessions whose admin confirmed the password lately.
	passes adminPasses
	// publicOrigin is the scheme and host of the site from the options, or empty
	// to take them from each request ([site.origin]).
	publicOrigin string
	// sitemaps caches the sitemap ([site.sitemapFor]).
	sitemaps sitemapCache
	// searches holds the limits of search ([site.searchGate]).
	searches searchLimits
	// guest is the guest page cache, or nil. cacheHandlers are the handlers
	// whose pages it keeps, by kind ([site.cached]).
	guest         *GuestCache
	cacheHandlers map[cacheKind]http.HandlerFunc
	// slotTokens stand for the per-request parts of a page that the cache
	// renders ([newSlotTokens]).
	slotTokens [slotCount]string
	// heldNote is the note of a held post, which fills the held slot of a cached
	// forum or topic page for a ?held=1 request.
	heldNote string
	// writes collects the small writes of page views, or is nil.
	writes *batch.Writer
}

// frame is what an error page needs of the board: the settings, for the board
// name, the header image and the default scheme, and the board's time zone.
type frame struct {
	settings board.Settings
	loc      *time.Location
}

// onNew, when set, sees the site that New built, before New returns. A test
// sets it, to break the templates of a running site.
var onNew func(*site)

// pageFiles are the page templates. Each one defines "content" and runs inside
// layout.html.
var pageFiles = []string{"index.html", "forum.html", "topic.html", "new.html", "login.html", "error.html", "postform.html", "confirm.html", "profile.html", "members.html", "team.html", "page.html", "search.html", "account.html", "messages.html", "message.html", "message-new.html", "reset.html", "reset-link.html", "queue.html", "postip.html",
	"admin.html", "admin-bans.html", "admin-confirm.html", "admin-delete.html", "admin-forum.html", "admin-forums.html", "admin-group.html", "admin-groups.html", "admin-grid.html", "admin-log.html", "admin-member.html", "admin-members.html", "admin-page.html", "admin-pages.html", "admin-perms.html", "admin-rank.html", "admin-ranks.html", "admin-role.html", "admin-roles.html", "admin-settings.html", "admin-smiley.html", "admin-smilies.html", "admin-spammer.html", "admin-user-grants.html"}

// New returns the handler of the whole server. It serves /healthz at the host
// root, and the site under opt.BasePath. Other paths get 404. New parses the
// templates once, so a broken template fails here and not on a request. It reads
// the legacy routes once too, sets the gate charge of the password checks from
// the stored hashes ([login.Service.LoadCharge]), and times the checks to set the
// floor of a failed sign-in ([login.Service.LoadFloor]).
func New(ctx context.Context, pool *pgxpool.Pool, opt Options) (http.Handler, error) {
	if opt.Mail != nil && (opt.Mail.PerHour < 1 || opt.Mail.PerDay < 1) {
		return nil, errors.New("web: MailOptions.PerHour and PerDay must be at least 1")
	}
	lo := login.DefaultOptions()
	if opt.LoginGate != nil {
		lo.Gate = opt.LoginGate
	}
	if opt.Signin != nil {
		lo.Schedule = login.NewSchedule(*opt.Signin)
	}
	if opt.Defense == nil {
		opt.Defense = lo.Defense
	}
	lo.Defense = opt.Defense
	s := &site{pool: pool, opt: opt, login: login.NewWith(pool, lo), crossOrigin: http.NewCrossOriginProtection(), tries: newTries(), postLimits: newPostLimits(opt),
		saves: newSaveLimits(opt), messageSends: newMessageSends(opt), resets: newResetLimits(opt.Mail), gate: lo.Gate, uploadSlots: make(chan struct{}, maxUploadsInFlight),
		adminWrites: newAdminWrites(opt), mailTests: newMailTests(), searches: newSearchLimits(opt)}
	s.crossOrigin.SetDenyHandler(http.HandlerFunc(s.denyCrossOrigin))
	s.crossOriginPosting = http.NewCrossOriginProtection()
	s.crossOriginPosting.SetDenyHandler(http.HandlerFunc(s.denyExpired))
	if opt.LoginFloor != 0 {
		s.login.Floor = opt.LoginFloor
	}
	switch {
	case opt.MaxInFlight == 0:
		s.inFlight = make(chan struct{}, DefaultMaxInFlight)
	case opt.MaxInFlight > 0:
		s.inFlight = make(chan struct{}, opt.MaxInFlight)
	}
	if opt.LoginGateWait != 0 {
		s.login.GateWait = opt.LoginGateWait
	}
	if s.opt.Now == nil {
		s.opt.Now = time.Now
	}
	if s.opt.PoolWait <= 0 {
		s.opt.PoolWait = defaultPoolWait
	}
	if s.opt.RefuseSearch == nil {
		s.opt.RefuseSearch = s.refuseGuestSearch
	}
	s.writes = opt.Writes
	if opt.GuestCache != nil {
		if !opt.GuestCache.site.CompareAndSwap(nil, s) {
			return nil, errors.New("web: a GuestCache serves one site")
		}
		s.guest = opt.GuestCache
		s.cacheHandlers = map[cacheKind]http.HandlerFunc{}
		var err error
		if s.slotTokens, err = newSlotTokens(); err != nil {
			return nil, err
		}
	}
	s.base = normalizeBase(opt.BasePath)
	var err error
	if s.publicOrigin, err = publicOrigin(opt.PublicURL, s.base, true); err != nil {
		return nil, err
	}
	if s.publicOrigin == "" && opt.Mail != nil {
		if s.publicOrigin, err = publicOrigin(opt.Mail.PublicURL, s.base, false); err != nil {
			return nil, err
		}
	}
	if s.publicOrigin == "" {
		slog.Warn("no public URL is set (GOBB_PUBLIC_URL): the pages have no canonical link, and the sitemap and robots.txt take the host of each request")
	}
	s.assetVersion = assetVersion()
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}
	if s.guest != nil {
		var note bytes.Buffer
		if err := s.pages["forum.html"].ExecuteTemplate(&note, "heldnote", true); err != nil {
			return nil, fmt.Errorf("web: build the held note: %w", err)
		}
		s.heldNote = note.String()
	}
	tab, err := legacy.Load(ctx, pool)
	if err != nil {
		return nil, err
	}
	s.legacy = tab
	// The error pages need the settings without the database.
	switch set, err := board.LoadSettings(ctx, pool); {
	case err == nil:
		s.keepFrame(set)
	case !errors.Is(err, board.ErrNoSettings):
		return nil, err
	}
	static, err := s.buildStaticErrors(s.frame.Load())
	if err != nil {
		return nil, err
	}
	s.staticErrors.Store(&static)
	// The charge of every password check follows the most costly stored hash.
	// Like the legacy table, it is read once: the server stops for an import.
	if err := s.login.LoadCharge(ctx); err != nil {
		return nil, err
	}
	// The floor of a failed sign-in covers the slowest sign-in on this host. A
	// test that sets LoginFloor skips the timing.
	if opt.LoginFloor == 0 {
		if err := s.login.LoadFloor(ctx); err != nil {
			return nil, err
		}
	}

	inner := newRoutes()
	inner.HandleFunc("GET /{$}", s.cached(cacheIndex, s.index))
	inner.HandleFunc("GET /f/{id}", s.cached(cacheForum, s.forum))
	inner.HandleFunc("GET /t/{id}", s.cached(cacheTopic, s.topic))
	inner.HandleFunc("GET /t/{id}/new", s.topicNew)
	inner.HandleFunc("GET /new", s.newPage)
	inner.HandleFunc("GET /f/{id}/post", s.postForm(kindTopic))
	inner.Handle("POST /f/{id}/post", s.protectPosting(maxPostingBody, s.postSubmit(kindTopic)))
	inner.HandleFunc("GET /t/{id}/reply", s.postForm(kindReply))
	inner.Handle("POST /t/{id}/reply", s.protectPosting(maxPostingBody, s.postSubmit(kindReply)))
	inner.HandleFunc("GET /p/{id}/edit", s.postForm(kindEdit))
	inner.Handle("POST /p/{id}/edit", s.protectPosting(maxPostingBody, s.postSubmit(kindEdit)))
	inner.Handle("POST /p/{id}/delete", s.protect(maxPostBody, s.deletePost))
	inner.Handle("POST /p/{id}/restore", s.protect(maxPostBody, s.postAction("restore this post", board.RestorePost)))
	inner.Handle("POST /p/{id}/approve", s.protect(maxPostBody, s.postAction("approve this post", board.ApprovePost)))
	inner.Handle("POST /t/{id}/lock", s.protect(maxPostBody, s.topicLock(true)))
	inner.Handle("POST /t/{id}/unlock", s.protect(maxPostBody, s.topicLock(false)))
	inner.Handle("POST /t/{id}/type", s.protect(maxPostBody, s.topicKind))
	inner.Handle("POST /read", s.protect(maxPostBody, s.markAllRead))
	inner.Handle("POST /f/{id}/read", s.protect(maxPostBody, s.markForumRead))
	inner.HandleFunc("GET /p/{id}", s.post)
	inner.HandleFunc("GET /p/{id}/ip", s.postIP)
	inner.HandleFunc("GET /a/{id}", s.attachment)
	inner.HandleFunc("GET /u/{id}", s.profile)
	inner.HandleFunc("GET /members", s.members)
	inner.HandleFunc("GET /team", s.team)
	inner.HandleFunc("GET /pages/{slug}", s.cached(cacheStatic, s.staticPage))
	inner.HandleFunc("GET /account", s.account)
	inner.HandleFunc("GET /messages", s.messageList(true))
	inner.HandleFunc("GET /messages/sent", s.messageList(false))
	inner.HandleFunc("GET /messages/new", s.messageNew)
	inner.Handle("POST /messages/new", s.protectPosting(maxPostingBody, s.messageSubmit))
	inner.HandleFunc("GET /messages/{id}", s.messageShow)
	inner.HandleFunc("GET /messages/{id}/reply", s.messageReply)
	inner.Handle("POST /messages/{id}/delete", s.protect(maxPostBody, s.messageDelete))
	inner.HandleFunc("GET /search", s.search)
	inner.HandleFunc("GET /queue", s.queuePage)
	inner.HandleFunc("GET /sitemap.xml", s.sitemap)
	inner.HandleFunc("GET /sitemaps/{name}", s.sitemapPart)
	if opt.Mail != nil {
		// The reset pages exist only when email is on. The token is in the path
		// of the last two, so their answers carry Referrer-Policy: no-referrer.
		inner.HandleFunc("GET /reset", s.resetForm)
		inner.Handle("POST /reset", s.protect(maxLoginBody, s.resetSubmit))
		inner.Handle("GET /reset/{token}", noReferrer(http.HandlerFunc(s.resetLinkPage)))
		inner.Handle("POST /reset/{token}", noReferrer(s.protect(maxLoginBody, s.resetComplete)))
	}
	inner.HandleFunc("GET /login", s.loginPage)
	inner.Handle("POST /login", s.protect(maxLoginBody, s.loginSubmit))
	inner.Handle("POST /logout", s.protectOr(maxPostBody, s.logout, s.toIndex))
	inner.Handle("POST /scheme", s.protect(maxPostBody, s.setScheme))
	inner.Handle("POST /account/password", s.protect(maxAccountBody, s.changePassword))
	inner.Handle("POST /account/email", s.protect(maxAccountBody, s.changeEmail))
	inner.Handle("POST /account/signature", s.protectPosting(maxPostingBody, s.changeSignature))
	inner.Handle("POST /account/timezone", s.protect(maxPostBody, s.setTimezone))
	inner.Handle("POST /account/dateformat", s.protect(maxPostBody, s.setDateFormat))
	inner.Handle("POST /account/about", s.protect(maxAccountBody, s.setAbout))
	inner.Handle("POST /account/avatar", s.protectUpload(maxAvatarBody, s.uploadAvatar))
	inner.Handle("POST /account/avatar/remove", s.protect(maxPostBody, s.removeAvatar))
	// The admin area has its own mux behind one gate. "/admin" is a route of
	// the area too, so the mux does not redirect it to "/admin/" for a visitor
	// whom the gate would refuse.
	admin := s.adminArea()
	inner.Handle("/admin", admin)
	inner.Handle("/admin/", admin)
	inner.HandleFunc("GET /static/{name}", s.static)
	inner.HandleFunc("GET /files/{dir}/{name...}", s.files)
	// Without this, the mux would redirect /files/avatars to /files/avatars/.
	inner.HandleFunc("GET /files/{dir}", s.notFound)
	// The legacy table runs last: a request that no route above matched.
	inner.HandleFunc("/", s.fallback)
	s.warnHidden(inner.ServeMux)

	root := newRoutes()
	root.Handle("/healthz", Health(pool))
	root.HandleFunc("GET /favicon.ico", s.favicon)
	// A crawler reads robots.txt at the host root only, whatever the base path.
	root.HandleFunc("GET /robots.txt", s.robots)
	if s.base == "/" {
		root.Handle("/", inner)
	} else {
		root.Handle(s.base, s.stripBase(inner))
		// A path outside the base path gets the themed 404 too, not the plain
		// text of the mux.
		root.HandleFunc("/", s.notFound)
	}
	s.patterns = append(s.patterns, inner.patterns...)
	s.patterns = append(s.patterns, root.patterns...)
	if onNew != nil {
		onNew(s)
	}
	metrics.SigninFloor.Set(s.login.Floor.Seconds())
	return metered(timed(secure(s.limitInFlight(s.guard(root))))), nil
}

// routes is a ServeMux that remembers the patterns registered on it, so a test
// can ask the running mux which routes exist instead of reading the source.
type routes struct {
	*http.ServeMux
	patterns []string
}

func newRoutes() *routes { return &routes{ServeMux: http.NewServeMux()} }

func (r *routes) Handle(pattern string, h http.Handler) {
	r.patterns = append(r.patterns, pattern)
	r.ServeMux.Handle(pattern, withRoute(pattern, h))
}

func (r *routes) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.Handle(pattern, http.HandlerFunc(h))
}

// stripBase removes the base path from the path of a request, as
// http.StripPrefix does, and answers a request whose raw path does not start
// with the base path (a percent-encoded letter of it, such as /fo%72um/) with
// the themed 404 instead of the plain text of the standard library.
func (s *site) stripBase(next http.Handler) http.Handler {
	prefix := strings.TrimSuffix(s.base, "/")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(r.URL.Path, prefix)
		rp := strings.TrimPrefix(r.URL.RawPath, prefix)
		if len(p) == len(r.URL.Path) || (r.URL.RawPath != "" && len(rp) == len(r.URL.RawPath)) {
			s.notFound(w, r)
			return
		}
		r2 := new(http.Request)
		*r2 = *r
		r2.URL = new(url.URL)
		*r2.URL = *r.URL
		r2.URL.Path, r2.URL.RawPath = p, rp
		next.ServeHTTP(w, r2)
	})
}

// normalizeBase returns p with one leading and one trailing "/".
func normalizeBase(p string) string {
	p = path.Clean("/" + p)
	if p == "/" {
		return p
	}
	return p + "/"
}

// requestTimeout is the deadline of the database work of one request. The tests
// shorten it.
var requestTimeout = 10 * time.Second

// guard gives the request a deadline of requestTimeout, so that a slow query
// cannot hold a connection for long, and a limit of Options.PoolWait on each
// wait for a connection ([db.WithPoolWait]). A request whose wait runs out gets
// 503 with Retry-After ([site.serverError], [poolBusyWriter]). It answers 404
// at once for a path that is not in its clean form, such as one with a ".."
// element or an empty element. The mux would redirect such a
// path to the clean one, and a redirect must not tell a visitor what a hidden
// path would have been.
func (s *site) guard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !isCleanPath(r.URL.Path) {
			s.notFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		ctx = db.WithPoolWait(ctx, s.opt.PoolWait)
		next.ServeHTTP(&poolBusyWriter{ResponseWriter: w, ctx: ctx}, r.WithContext(ctx))
	})
}

// poolBusyRetrySeconds is the Retry-After of a request that found no free
// database connection in time.
const poolBusyRetrySeconds = 2

// poolBusyWriter answers a 500 or a 503 of a request that found no free
// database connection in time with 503 and Retry-After. A Retry-After that the
// handler set stays. [site.serverError] answers such a request with the 503 page
// itself; this covers the handlers that answer a failed query with their own
// status, such as the JSON answers of the forms ([failStatus] gives them 503
// for a deadline, and the guard of a save gives 500).
type poolBusyWriter struct {
	http.ResponseWriter
	ctx context.Context
}

func (w *poolBusyWriter) WriteHeader(code int) {
	if (code == http.StatusInternalServerError || code == http.StatusServiceUnavailable) && db.PoolWaitTimedOut(w.ctx) {
		if w.Header().Get("Retry-After") == "" {
			w.Header().Set("Retry-After", strconv.Itoa(poolBusyRetrySeconds))
		}
		code = http.StatusServiceUnavailable
	}
	w.ResponseWriter.WriteHeader(code)
}

// Unwrap returns the writer below, for [http.NewResponseController].
func (w *poolBusyWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

// isCleanPath reports whether p is in its clean form, as the guard needs it:
// no empty element and no "." or ".." element. One trailing slash is allowed.
func isCleanPath(p string) bool {
	if strings.HasSuffix(p, "/") && len(p) > 1 {
		p = p[:len(p)-1]
	}
	return p == "" || p == path.Clean(p)
}

// path joins the base path and the parts into a site path. A part may hold
// slashes. Each element between slashes is escaped for a URL path.
func (s *site) path(parts ...string) string {
	var elems []string
	for _, p := range parts {
		for _, e := range strings.Split(p, "/") {
			if e != "" {
				elems = append(elems, url.PathEscape(e))
			}
		}
	}
	return s.base + strings.Join(elems, "/")
}

// assetVersion returns a short hash of the stylesheet, the script and the icon.
func assetVersion() string {
	h := sha256.New()
	for _, name := range []string{"static/gobb.css", "static/gobb.js", "static/icon.svg"} {
		b, err := staticFS.ReadFile(name)
		if err != nil {
			continue
		}
		h.Write(b)
	}
	return hex.EncodeToString(h.Sum(nil))[:10]
}

// page is the data every template receives.
type page struct {
	Title    string
	Scheme   string
	Settings board.Settings
	Viewer   board.Viewer
	// Clock says how the page shows times: the zone and the date format of the
	// viewer, and the time of the request.
	Clock clock
	// Nav lists the links of the bar under the header.
	Nav []navLink
	// CSS, JS and Icon are the site paths of the stylesheet, the script and the
	// site icon.
	CSS, JS, Icon string
	// Home is the site path of the board index.
	Home string
	// HeaderSrc is the site path of the header image, or empty.
	HeaderSrc string
	// Controls is true for a page that carries the forms of the layout: sign out
	// and the scheme picker. render then gives it a csrf token.
	Controls bool
	// CSRF is the token for the forms of the page. render fills it in.
	CSRF string
	// Return is the site path that the forms of the layout send the visitor back
	// to. LoginHref is the link of a guest to the sign-in page.
	Return, LoginHref string
	// Schemes are the buttons of the scheme picker.
	Schemes []schemeChoice
	// csrfSecret is the secret of the csrf token: the session token of a member,
	// and the gobb_csrf cookie of a guest, which is empty for a guest who has none.
	csrfSecret string
	// SearchHref is the site path of the search page, or empty for a viewer
	// without account.search. The nav then holds a search box.
	SearchHref    string
	SearchCurrent bool
	// AccountHref is the site path of the account page, where the member's name
	// in the header links. AccountCurrent is true on that page.
	AccountHref    string
	AccountCurrent bool
	// Pages are the static pages that the footer lists. render fills them in.
	Pages []board.StaticPage
	// Data holds the data of the page template.
	Data any
	// ctx and navKey let render add the "new since your last visit" link to the
	// nav of a member: ctx is the request context, navKey the nav item of the
	// page that is current.
	ctx    context.Context
	navKey string
	// navUnread is the count of unread topics of the "new since your last visit"
	// list when the page already knows it. addNewLink then asks the database for
	// nothing.
	navUnread *int
	// UnreadMessages is the count of unread messages in the inbox of the viewer,
	// which addMessagesLink reads for the nav. It is 0 when the viewer has none
	// or the count failed. The inbox page prints it from here.
	UnreadMessages int
	// policy is the Content-Security-Policy of the page. A handler that renders
	// posts adds the embeds of each one with addRendered.
	policy policy
	// Scripts are the URLs of the provider scripts that the embeds on the page
	// need. The layout loads each one with <script async>.
	Scripts []string
	// Noindex adds the robots meta element. render sets it for the pages in
	// noindexPages.
	Noindex bool
	// Footer is the build line at the end of the footer. render sets it from
	// the status; footerLine says what it holds.
	Footer string
	// Canonical is the absolute address of the page for the canonical link, or
	// empty for none. render sets it from canonicalPath and the public URL of the
	// site, for a page of status 200 and
	// no robots meta.
	Canonical string
	// Meta holds the meta description and the Open Graph tags, or nil for none.
	// render sets it on a page that Canonical has, for a guest only: a crawler
	// and a link preview are guests, and a page that only a member sees must
	// not describe itself.
	Meta *pageMeta
	// canonicalPath is the site path of the page, with no query. begin sets it
	// from the clean path of the request. A handler whose page has a number
	// (a forum, a topic) sets it from the ID and the page that it served.
	canonicalPath string
	// description is the meta description that the handler picks, and ogType
	// the Open Graph type. Empty means the board description and "website".
	description, ogType string
	// private marks a page that a guest must not have described, such as a
	// held topic that a guest moderator sees. render then sets no Meta.
	private bool
}

// addRendered adds what the embeds of a rendered tree need to the page: the
// origins for its policy and the provider scripts it loads.
func (p *page) addRendered(r markup.Rendered) {
	p.policy.add(r)
	p.Scripts = mergeSorted(p.Scripts, r.Scripts)
}

// navLink is one link of the nav bar.
type navLink struct {
	Label, Href string
	Current     bool
	// Badge is a short note after the label, such as "2 new", or empty.
	Badge string
	// Short is the label on a phone, or empty for Label on every screen.
	Short string
	// Wide marks a link that the nav shows only on a wide screen. On a phone the
	// account menu holds it, so it is set only for a member, who has the menu.
	Wide bool
}

// begin loads what every page needs: the settings and the viewer. The viewer is
// the member whose session cookie is live, and a guest otherwise. It also picks
// the color scheme: a valid ?scheme= value for this one page, otherwise the
// scheme of the viewer, which for a guest is the gobb_scheme cookie or else the
// board default.
//
// For a member, begin records that the session is in use. It writes at most once
// every five minutes, and then it also sends a remembered session's cookie
// again, so that its year starts over.
func (s *site) begin(w http.ResponseWriter, r *http.Request, title string, nav string) (*page, error) {
	ctx := r.Context()
	settings, err := board.LoadSettings(ctx, s.pool)
	if errors.Is(err, board.ErrNoSettings) {
		s.frame.Store(nil)
	}
	if err != nil {
		return nil, err
	}
	s.keepFrame(settings)
	s.noDataLogged.Store(false)
	id, err := s.identify(w, r)
	if err != nil {
		return nil, err
	}
	var v board.Viewer
	if id.token != "" {
		// A top-level navigation counts as activity and may start a new visit.
		// Any other request, such as the load of an image, writes nothing here.
		navigated := isNavigation(r)
		if navigated && s.writes == nil {
			if err := board.RecordVisit(ctx, s.pool, id.sess.UserID, s.opt.Now()); err != nil {
				slog.Warn("cannot record a visit", "err", err)
			}
		}
		v, err = board.Member(ctx, s.pool, id.sess.UserID, settings)
		switch {
		case errors.Is(err, board.ErrNotFound):
			// Lookup already refuses the session of a user who is not active, so
			// this is the user's removal between the two queries. The session is
			// worth nothing: end it like any dead one.
			id = s.endSession(w, r, id)
		case err != nil:
			return nil, err
		default:
			s.touch(w, r, id.sess)
			if navigated {
				s.recordVisit(r, &v)
			}
		}
	}
	if id.token == "" {
		if v, err = board.Guest(ctx, s.pool, settings); err != nil {
			return nil, err
		}
		if c, err := r.Cookie(schemeCookie); err == nil && board.ValidScheme(c.Value) {
			v.Scheme = c.Value
		}
	}
	scheme := v.Scheme
	if q := r.URL.Query().Get("scheme"); board.ValidScheme(q) {
		scheme = q
	}
	ret := s.returnPath(r)
	p := &page{
		Title:          title,
		Scheme:         scheme,
		Settings:       settings,
		Viewer:         v,
		Clock:          clock{Loc: v.Loc, Format: v.DateFormat, Now: s.opt.Now()},
		CSS:            s.path("static", "gobb.css") + "?v=" + s.assetVersion,
		JS:             s.path("static", "gobb.js") + "?v=" + s.assetVersion,
		Icon:           s.path("static", "icon.svg") + "?v=" + s.assetVersion,
		Home:           s.path(),
		AccountHref:    s.path("account"),
		AccountCurrent: nav == "account",
		Nav:            []navLink{{Label: "Index", Href: s.path(), Current: nav == "index"}},
		Controls:       true,
		Return:         ret,
		Schemes:        choices(scheme),
		csrfSecret:     id.secret(),
		ctx:            ctx,
		navKey:         nav,
	}
	if id.token == "" && nav != "login" {
		p.LoginHref = s.loginHref(ret)
	}
	p.canonicalPath = s.path(r.URL.Path)
	if settings.HeaderImage != "" {
		p.HeaderSrc = s.path("files", settings.HeaderImage)
	}
	return p, nil
}

// touch records the use of a member's session. A failure is logged and does not
// fail the page: the session is still good.
func (s *site) touch(w http.ResponseWriter, r *http.Request, sess session.Session) {
	now := s.opt.Now()
	if !session.NeedsTouch(sess, now) {
		return
	}
	if err := session.Touch(r.Context(), s.pool, sess, now); err != nil {
		slog.Warn("cannot touch a session", "err", err)
		return
	}
	if sess.Remember {
		// The cookie ends a year after this visit, as the session does.
		if c, err := r.Cookie(sessionCookie); err == nil {
			http.SetCookie(w, s.cookie(sessionCookie, c.Value, rememberSeconds))
		}
	}
}

// addNav adds the links that depend on the viewer to the nav: for a member "New
// since your last visit", then Members for a viewer who may see profiles, then
// Team, Messages, Queue for a viewer who may approve somewhere, and Admin for an
// admin. For a member, Members and Team are wide links: on a phone they move to
// the account menu. A page with no request context (the frame of an empty
// database) keeps the nav it has.
func (s *site) addNav(p *page) {
	if p.ctx == nil {
		return
	}
	s.addNewLink(p)
	wide := p.Viewer.UserID != 0
	if p.Viewer.Perms.Has("account.view_profiles", 0) {
		p.Nav = append(p.Nav, navLink{Label: "Members", Href: s.path("members"), Current: p.navKey == "members", Wide: wide})
	}
	p.Nav = append(p.Nav, navLink{Label: "Team", Href: s.path("team"), Current: p.navKey == "team", Wide: wide})
	s.addMessagesLink(p)
	if p.Viewer.UserID != 0 && p.Viewer.Perms.HasAnywhere("mod.approve") {
		p.Nav = append(p.Nav, navLink{Label: "Queue", Href: s.path("queue"), Current: p.navKey == "queue"})
	}
	if p.Viewer.UserID != 0 && p.Viewer.Perms.IsAdmin() {
		p.Nav = append(p.Nav, navLink{Label: "Admin", Href: s.path("admin") + "/", Current: p.navKey == "admin"})
	}
	if p.Viewer.Perms.Has("account.search", 0) {
		p.SearchHref, p.SearchCurrent = s.path("search"), p.navKey == "search"
	}
}

// addPageLinks reads the static pages for the footer. A failure is logged and the
// footer shows none.
func (s *site) addPageLinks(p *page) {
	if p.ctx == nil {
		return
	}
	links, err := board.PageLinks(p.ctx, s.pool)
	if err != nil {
		slog.Warn("cannot list the static pages", "err", err)
		return
	}
	p.Pages = links
}

// addNewLink adds "New since your last visit (N)" to the nav of a member, where N
// is the number of unread topics of the list. The item hides when N is 0. When
// the count cannot be read, the link stays and shows no number.
func (s *site) addNewLink(p *page) {
	if p.Viewer.UserID == 0 {
		return
	}
	label, short := "New since your last visit", "New"
	unread := p.navUnread
	if unread == nil {
		_, n, err := board.NewCounts(p.ctx, s.pool, p.Viewer)
		if err != nil {
			slog.Warn("cannot count the new topics", "err", err)
		} else {
			unread = &n
		}
	}
	if unread != nil {
		if *unread == 0 {
			return
		}
		label += " (" + strconv.Itoa(*unread) + ")"
		short += " (" + strconv.Itoa(*unread) + ")"
	}
	p.Nav = append(p.Nav, navLink{Label: label, Short: short, Href: s.path("new"), Current: p.navKey == "new"})
}

// render executes the page template into a buffer, so that a template error
// gives a clean 500 and not half a page, then writes it with status.
func (s *site) render(w http.ResponseWriter, status int, name string, p *page) {
	s.renderThen(w, status, name, p, nil)
}

// layoutBuilds counts the executions of the layout template. A test reads it to
// see that a request that has no bookkeeping builds the page once.
var layoutBuilds atomic.Int64

// renderThen is [site.render] with a step in the middle: once the page is known
// to build and before the nav count is read, it calls after. A page that fails to
// build never calls it, so bookkeeping that belongs to a page that was shown (a
// view count, a read mark) is not written for a page that was not.
func (s *site) renderThen(w http.ResponseWriter, status int, name string, p *page, after func()) {
	t, ok := s.pages[name]
	if !ok {
		slog.Error("render: no such template", "name", name)
		s.renderFailed(w, status)
		return
	}
	note := noteFrom(p.ctx)
	if p.Controls && note != nil {
		// A render for the guest page cache: the token is a slot, which each
		// request fills in with its own ([site.serveCached]).
		p.CSRF = s.slotTokens[slotCSRF]
	} else if p.Controls {
		secret := p.csrfSecret
		if secret == "" {
			// A guest with no secret yet gets one with the first page that has a form.
			var err error
			if secret, err = newCSRFSecret(); err != nil {
				slog.Error("render: csrf secret", "err", err)
				s.renderFailed(w, status)
				return
			}
			http.SetCookie(w, s.cookie(csrfCookie, secret, 0))
		}
		p.CSRF = session.CSRFToken(secret)
	}
	s.addPageLinks(p)
	p.Noindex = p.Noindex || noindexPages[name]
	s.addMeta(p, status)
	if after != nil {
		// Build the page once to see that it builds, then write the bookkeeping,
		// then build it again: the nav count comes after the write, so the page
		// that marks a topic read does not count that topic as unread.
		layoutBuilds.Add(1)
		if err := t.ExecuteTemplate(io.Discard, "layout", p); err != nil {
			slog.Error("render", "template", name, "err", err)
			s.renderFailed(w, status)
			return
		}
		after()
	}
	s.addNav(p)
	// The last step before the footer is written: the count holds every
	// statement of the page, and the time stops here, about one template run
	// (a millisecond) before the page is written.
	p.Footer = footerLine(p.ctx, status, p.Viewer.Perms.IsAdmin())
	if note != nil {
		p.Footer = s.slotTokens[slotFooter]
	}
	var buf bytes.Buffer
	layoutBuilds.Add(1)
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		slog.Error("render", "template", name, "err", err)
		s.renderFailed(w, status)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", p.policy.String())
	// The value of the middleware is a default. A handler's own value, such as
	// the no-store of the sign-in page, stays.
	if cc := h.Get("Cache-Control"); cc == "" || cc == cacheGuest || cc == cacheMember {
		h.Set("Cache-Control", cacheControl(p.Viewer.UserID != 0))
	}
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// addMeta sets the canonical link of a page that search engines may index, and
// for a guest the description and the Open Graph tags. The canonical link and
// og:url need the public URL of the site: an address built from the Host
// header of a request would let the request choose it.
func (s *site) addMeta(p *page, status int) {
	p.Canonical, p.Meta = "", nil
	if status != http.StatusOK || p.Noindex || p.canonicalPath == "" {
		return
	}
	if s.publicOrigin != "" {
		p.Canonical = s.publicOrigin + p.canonicalPath
	}
	if p.Viewer.UserID != 0 || p.private {
		return
	}
	m := &pageMeta{Title: p.Title, Description: p.description, Type: p.ogType, URL: p.Canonical, SiteName: p.Settings.BoardName}
	if m.Title == "" {
		m.Title = p.Settings.BoardName
	}
	if m.Description == "" {
		m.Description = excerpt(p.Settings.BoardDescription, descriptionLimit)
	}
	if m.Type == "" {
		m.Type = "website"
	}
	p.Meta = m
}

// errorData is the data of error.html.
type errorData struct {
	Status  int
	Heading string
	Message string
	// Href and HrefLabel add one link under the message, such as "Sign in". Both
	// are empty for no link.
	Href, HrefLabel string
}

// fail writes the error page with the given status, in the layout. When the
// page of the viewer cannot be built (the database is down or slow, or holds
// no board), the page is a guest's, from the settings in memory
// ([site.framePage]). It never answers in plain text.
func (s *site) fail(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	s.failLink(w, r, status, heading, message, "", "")
}

// failLink is [site.fail] with a link under the message.
func (s *site) failLink(w http.ResponseWriter, r *http.Request, status int, heading, message, href, label string) {
	p, err := s.begin(w, r, heading, "")
	if err != nil {
		if !errors.Is(err, board.ErrNoSettings) {
			slog.Warn("error page from the settings in memory", "status", status, "err", err)
		}
		p = s.framePage(r, heading)
	}
	p.Data = errorData{Status: status, Heading: heading, Message: message, Href: href, HrefLabel: label}
	s.render(w, status, "error.html", p)
}

// notFound answers 404 with the layout.
func (s *site) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "Page not found", "There is no page at this address.")
}

// keepFrame stores set as the settings of the error pages, when they differ
// from the stored ones.
func (s *site) keepFrame(set board.Settings) {
	if cur := s.frame.Load(); cur != nil && cur.settings == set {
		return
	}
	loc, err := time.LoadLocation(set.Timezone)
	if err != nil {
		loc = time.UTC
	}
	s.frame.Store(&frame{settings: set, loc: loc})
}

// refreshFrame reads the board settings again and makes the pages that hold the
// board name follow them: the frame of the error pages ([site.keepFrame]) and the
// static 500 and 503 pages. An admin's save of the settings calls it, because
// New built both once. It reads the database itself under a lock, so that two
// saves at once end with the newest values whatever order they finish in. A
// request that holds the old map of pages finishes with it; the next one gets the
// new map. When the settings or the pages cannot be built it returns the error
// and leaves what it had.
func (s *site) refreshFrame(ctx context.Context) error {
	s.refreshing.Lock()
	defer s.refreshing.Unlock()
	set, err := board.LoadSettings(ctx, s.pool)
	if err != nil {
		return fmt.Errorf("web: refresh the error pages: %w", err)
	}
	s.keepFrame(set)
	static, err := s.buildStaticErrors(s.frame.Load())
	if err != nil {
		return err
	}
	s.staticErrors.Store(&static)
	return nil
}

// framePage returns the page of a guest built from the settings in memory: the
// board name, the header image, the default scheme (or a valid ?scheme= of r,
// which may be nil) and the board's time zone. It reads no database, no
// session and no nav count, and it has no forms. It is an error page, so it
// carries the robots meta. With no settings in memory (a
// database with no board), the frame is gobb's own: the name gobb, the zone
// UTC and the first scheme.
func (s *site) framePage(r *http.Request, title string) *page {
	return s.framePageFrom(s.frame.Load(), r, title)
}

// framePageFrom is [site.framePage] for the frame f, which may be nil.
func (s *site) framePageFrom(f *frame, r *http.Request, title string) *page {
	set, loc, scheme := board.Settings{BoardName: "gobb"}, time.UTC, board.Schemes[0]
	if f != nil {
		set, loc = f.settings, f.loc
		if board.ValidScheme(set.DefaultScheme) {
			scheme = set.DefaultScheme
		}
	}
	if r != nil {
		if q := r.URL.Query().Get("scheme"); board.ValidScheme(q) {
			scheme = q
		}
	}
	p := &page{
		Title:    title,
		Scheme:   scheme,
		Settings: set,
		Viewer:   board.Viewer{Loc: loc, Scheme: scheme, DateFormat: "standard"},
		Clock:    clock{Loc: loc, Format: "standard", Now: s.opt.Now()},
		CSS:      s.path("static", "gobb.css") + "?v=" + s.assetVersion,
		JS:       s.path("static", "gobb.js") + "?v=" + s.assetVersion,
		Icon:     s.path("static", "icon.svg") + "?v=" + s.assetVersion,
		Home:     s.path(),
		Nav:      []navLink{{Label: "Index", Href: s.path()}},
		Noindex:  true,
		Footer:   plainFooter(),
	}
	if set.HeaderImage != "" {
		p.HeaderSrc = s.path("files", set.HeaderImage)
	}
	return p
}

// failTexts are the texts of the error pages that need no handler: a page that
// could not be built, and the static pages that New builds.
var failTexts = map[int]errorData{
	http.StatusInternalServerError: {Heading: "Something went wrong", Message: "The server could not build this page. Try again in a moment."},
	http.StatusServiceUnavailable:  {Heading: "Try again soon", Message: "The board cannot answer right now. Try again in a moment."},
	http.StatusNotFound:            {Heading: "Page not found", Message: "There is no page at this address."},
}

// renderFailed answers a page that could not be built (a template or the csrf
// secret failed). A 503 stays a 503; any other status becomes a 500.
func (s *site) renderFailed(w http.ResponseWriter, status int) {
	if status != http.StatusServiceUnavailable {
		status = http.StatusInternalServerError
	}
	s.writeFailure(w, status)
}

// writeFailure answers status with the error page of a guest from the settings
// in memory ([site.framePage]): no database, no session, no csrf token. When
// the error template fails too, it writes the page that New built for the
// status (the 500 for any status but 503).
func (s *site) writeFailure(w http.ResponseWriter, status int) {
	d := failTexts[status]
	d.Status = status
	if !s.writeFrame(w, status, d) {
		s.writeStatic(w, status)
	}
}

// writeFrame writes the error page d with status, as the page of a guest from
// the settings in memory ([site.framePage]): no database, no session, no csrf
// token. It writes nothing and returns false when the error template fails.
func (s *site) writeFrame(w http.ResponseWriter, status int, d errorData) bool {
	p := s.framePage(nil, d.Heading)
	p.Data = d
	var buf bytes.Buffer
	t, ok := s.pages["error.html"]
	if !ok {
		return false
	}
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		slog.Error("render: the error template failed", "err", err)
		return false
	}
	writeHTML(w, status, buf.Bytes())
	return true
}

// writeStatic writes the themed page that New built for status: the 503 for
// 503, and the 500 for any other status.
func (s *site) writeStatic(w http.ResponseWriter, status int) {
	var pages map[int][]byte
	if m := s.staticErrors.Load(); m != nil {
		pages = *m
	}
	b, ok := pages[status]
	if !ok {
		status = http.StatusInternalServerError
		b = pages[status]
	}
	if len(b) == 0 {
		// New always builds the pages. Only a site that New did not build has
		// none.
		b = []byte(lastResort)
	}
	writeHTML(w, status, b)
}

// lastResort is the page of a site with no static error pages. Only a site
// value that New did not build reaches it.
const lastResort = "<!doctype html>\n<html lang=\"en\"><title>Error</title><p>Something went wrong. Try again in a moment.</p></html>\n"

// writeHTML writes an error page with the base policy of the site. No cache
// keeps it: the next request may well succeed.
func writeHTML(w http.ResponseWriter, status int, body []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", baseCSP)
	h.Set("Cache-Control", "no-store")
	h.Del("Content-Length")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// buildStaticErrors builds the themed 500 and 503 pages from the frame f, which
// may be nil. New calls it at start, and a broken error template fails New here.
// [site.refreshFrame] calls it after a save of the board settings.
func (s *site) buildStaticErrors(f *frame) (map[int][]byte, error) {
	out := map[int][]byte{}
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		d := failTexts[status]
		d.Status = status
		p := s.framePageFrom(f, nil, d.Heading)
		p.Data = d
		var buf bytes.Buffer
		if err := s.pages["error.html"].ExecuteTemplate(&buf, "layout", p); err != nil {
			return nil, fmt.Errorf("web: build the %d page: %w", status, err)
		}
		out[status] = buf.Bytes()
	}
	return out, nil
}

// serverError logs err and answers 500 with the layout. A database with no
// board in it gets 503 and a hint, and one WARN line until a board appears.
//
// A request that found no free database connection in time gets 503 with
// Retry-After, from the settings in memory: the page needs no connection. So
// does an error that wraps [db.ErrPoolWait], such as a sitemap build of another
// request that found the pool full. The db package writes the WARN line of
// these requests ([db.Tracer]); serverError logs only an error that is not
// the end of a wait or of a deadline, which the pool timeout did not explain.
func (s *site) serverError(w http.ResponseWriter, r *http.Request, err error) {
	if db.PoolWaitTimedOut(r.Context()) || errors.Is(err, db.ErrPoolWait) {
		if !errors.Is(err, db.ErrPoolWait) && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			slog.Error("request failed", "method", r.Method, "path", logPath(r), "err", err)
		}
		s.poolBusy(w)
		return
	}
	if errors.Is(err, board.ErrNoSettings) {
		if s.noDataLogged.CompareAndSwap(false, true) {
			slog.Warn("the database holds no board; run gobb import")
		}
		s.fail(w, r, http.StatusServiceUnavailable, "No data yet", "This board has no data yet. Run `gobb import`.")
		return
	}
	slog.Error("request failed", "method", r.Method, "path", logPath(r), "err", err)
	s.fail(w, r, http.StatusInternalServerError, "Something went wrong", "The server could not build this page. Try again in a moment.")
}

// poolBusy answers a request that found no free database connection in time:
// 503 with Retry-After, from the settings in memory.
func (s *site) poolBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(poolBusyRetrySeconds))
	s.writeFailure(w, http.StatusServiceUnavailable)
}

// logPath returns the path of r for a log line. The path of a reset link holds
// the token, so a log line gets "/reset/{token}" in its place.
func logPath(r *http.Request) string {
	if strings.HasPrefix(r.URL.Path, "/reset/") {
		return "/reset/{token}"
	}
	return r.URL.Path
}
