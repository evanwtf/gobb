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

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/legacy"
	"github.com/evanwtf/gobb/internal/login"
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
	// LoginGateWait is how long a password check waits for the gate. Zero means
	// the default of 5 seconds. Tests set it small.
	LoginGateWait time.Duration
}

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
	// gate is the memory gate of the password checks. An avatar decode takes its
	// share from the same gate, so one budget covers both.
	gate *login.Gate
	// uploadSlots is the semaphore of the avatar uploads that are in flight: each
	// one holds up to a megabyte of body in memory before the decode takes its
	// share of the gate. A test makes it smaller.
	uploadSlots chan struct{}
	// uploading holds the IDs of the members who have an avatar upload in flight:
	// a member has one at a time.
	uploading sync.Map
	// patterns lists every pattern registered on the muxes of the site, for a test
	// that checks the routes against its tables.
	patterns []string
	// staticErrors holds the themed 500 and 503 pages, built once in New. They
	// answer when even the error template fails.
	staticErrors map[int][]byte
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
var pageFiles = []string{"index.html", "forum.html", "topic.html", "new.html", "login.html", "error.html", "postform.html", "confirm.html", "profile.html", "members.html", "team.html", "page.html", "search.html", "account.html"}

// New returns the handler of the whole server. It serves /healthz at the host
// root, and the site under opt.BasePath. Other paths get 404. New parses the
// templates once, so a broken template fails here and not on a request. It reads
// the legacy routes once too, sets the gate charge of the password checks from
// the stored hashes ([login.Service.LoadCharge]), and times the checks to set the
// floor of a failed sign-in ([login.Service.LoadFloor]).
func New(ctx context.Context, pool *pgxpool.Pool, opt Options) (http.Handler, error) {
	lo := login.DefaultOptions()
	if opt.LoginGate != nil {
		lo.Gate = opt.LoginGate
	}
	s := &site{pool: pool, opt: opt, login: login.NewWith(pool, lo), crossOrigin: http.NewCrossOriginProtection(), tries: newTries(), postLimits: newPostLimits(opt),
		saves: newSaveLimits(opt), gate: lo.Gate, uploadSlots: make(chan struct{}, maxUploadsInFlight)}
	s.crossOrigin.SetDenyHandler(http.HandlerFunc(s.denyCrossOrigin))
	s.crossOriginPosting = http.NewCrossOriginProtection()
	s.crossOriginPosting.SetDenyHandler(http.HandlerFunc(s.denyExpired))
	if opt.LoginFloor != 0 {
		s.login.Floor = opt.LoginFloor
	}
	if opt.LoginGateWait != 0 {
		s.login.GateWait = opt.LoginGateWait
	}
	if s.opt.Now == nil {
		s.opt.Now = time.Now
	}
	s.base = normalizeBase(opt.BasePath)
	s.assetVersion = assetVersion()
	if err := s.parseTemplates(); err != nil {
		return nil, err
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
	if s.staticErrors, err = s.buildStaticErrors(); err != nil {
		return nil, err
	}
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
	inner.HandleFunc("GET /{$}", s.index)
	inner.HandleFunc("GET /f/{id}", s.forum)
	inner.HandleFunc("GET /t/{id}", s.topic)
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
	inner.Handle("POST /read", s.protect(maxPostBody, s.markAllRead))
	inner.Handle("POST /f/{id}/read", s.protect(maxPostBody, s.markForumRead))
	inner.HandleFunc("GET /p/{id}", s.post)
	inner.HandleFunc("GET /a/{id}", s.attachment)
	inner.HandleFunc("GET /u/{id}", s.profile)
	inner.HandleFunc("GET /members", s.members)
	inner.HandleFunc("GET /team", s.team)
	inner.HandleFunc("GET /pages/{slug}", s.staticPage)
	inner.HandleFunc("GET /account", s.account)
	inner.HandleFunc("GET /search", s.search)
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
	if s.base == "/" {
		root.Handle("/", inner)
	} else {
		root.Handle(s.base, s.stripBase(inner))
		// A path outside the base path gets the themed 404 too, not the plain
		// text of the mux.
		root.HandleFunc("/", s.notFound)
	}
	s.patterns = append(inner.patterns, root.patterns...)
	if onNew != nil {
		onNew(s)
	}
	return timed(secure(s.guard(root))), nil
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
	r.ServeMux.Handle(pattern, h)
}

func (r *routes) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	r.patterns = append(r.patterns, pattern)
	r.ServeMux.HandleFunc(pattern, h)
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
// cannot hold a connection for long. It answers 404 at once for a path that is not in its clean form, such as
// one with a ".." element or an empty element. The mux would redirect such a
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
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

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
		if isNavigation(r) {
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
// Team. A page with no request context (the frame of an empty database) keeps the
// nav it has.
func (s *site) addNav(p *page) {
	if p.ctx == nil {
		return
	}
	s.addNewLink(p)
	if p.Viewer.Perms.Has("account.view_profiles", 0) {
		p.Nav = append(p.Nav, navLink{Label: "Members", Href: s.path("members"), Current: p.navKey == "members"})
	}
	p.Nav = append(p.Nav, navLink{Label: "Team", Href: s.path("team"), Current: p.navKey == "team"})
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
	label := "New since your last visit"
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
	}
	p.Nav = append(p.Nav, navLink{Label: label, Href: s.path("new"), Current: p.navKey == "new"})
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
	if p.Controls {
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

// framePage returns the page of a guest built from the settings in memory: the
// board name, the header image, the default scheme (or a valid ?scheme= of r,
// which may be nil) and the board's time zone. It reads no database, no
// session and no nav count, and it has no forms. It is an error page, so it
// carries the robots meta. With no settings in memory (a
// database with no board), the frame is gobb's own: the name gobb, the zone
// UTC and the first scheme.
func (s *site) framePage(r *http.Request, title string) *page {
	set, loc, scheme := board.Settings{BoardName: "gobb"}, time.UTC, board.Schemes[0]
	if f := s.frame.Load(); f != nil {
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
	p := s.framePage(nil, d.Heading)
	p.Data = d
	var buf bytes.Buffer
	t, ok := s.pages["error.html"]
	if ok {
		if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
			slog.Error("render: the error template failed", "err", err)
			ok = false
		}
	}
	if !ok {
		s.writeStatic(w, status)
		return
	}
	writeHTML(w, status, buf.Bytes())
}

// writeStatic writes the themed page that New built for status: the 503 for
// 503, and the 500 for any other status.
func (s *site) writeStatic(w http.ResponseWriter, status int) {
	b, ok := s.staticErrors[status]
	if !ok {
		status = http.StatusInternalServerError
		b = s.staticErrors[status]
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

// buildStaticErrors builds the themed 500 and 503 pages from the settings in
// memory, once, at start. A broken error template fails New here.
func (s *site) buildStaticErrors() (map[int][]byte, error) {
	out := map[int][]byte{}
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable} {
		d := failTexts[status]
		d.Status = status
		p := s.framePage(nil, d.Heading)
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
func (s *site) serverError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, board.ErrNoSettings) {
		if s.noDataLogged.CompareAndSwap(false, true) {
			slog.Warn("the database holds no board; run gobb import")
		}
		s.fail(w, r, http.StatusServiceUnavailable, "No data yet", "This board has no data yet. Run `gobb import`.")
		return
	}
	slog.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	s.fail(w, r, http.StatusInternalServerError, "Something went wrong", "The server could not build this page. Try again in a moment.")
}
