package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/legacy"
	"github.com/evanwtf/gobb/internal/login"
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
	// default of 400 ms. Tests set it small.
	LoginFloor time.Duration
	// LoginGate replaces the memory gate of password checks. Nil means a gate
	// with the default budget. Tests use it to fill the gate.
	LoginGate *login.Gate
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
	// crossOrigin guards every POST route.
	crossOrigin *http.CrossOriginProtection
	// legacy holds the old routes and file prefixes of the board. It is read
	// once, in New: the server stops for an import.
	legacy *legacy.Table
}

// pageFiles are the page templates. Each one defines "content" and runs inside
// layout.html.
var pageFiles = []string{"index.html", "forum.html", "topic.html", "new.html", "login.html", "error.html", "profile.html", "members.html", "team.html", "page.html", "search.html"}

// New returns the handler of the whole server. It serves /healthz at the host
// root, and the site under opt.BasePath. Other paths get 404. New parses the
// templates once, so a broken template fails here and not on a request. It reads
// the legacy routes once too.
func New(ctx context.Context, pool *pgxpool.Pool, opt Options) (http.Handler, error) {
	lo := login.DefaultOptions()
	if opt.LoginGate != nil {
		lo.Gate = opt.LoginGate
	}
	s := &site{pool: pool, opt: opt, login: login.NewWith(pool, lo), crossOrigin: http.NewCrossOriginProtection()}
	s.crossOrigin.SetDenyHandler(http.HandlerFunc(s.denyCrossOrigin))
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

	inner := http.NewServeMux()
	inner.HandleFunc("GET /{$}", s.index)
	inner.HandleFunc("GET /f/{id}", s.forum)
	inner.HandleFunc("GET /t/{id}", s.topic)
	inner.HandleFunc("GET /t/{id}/new", s.topicNew)
	inner.HandleFunc("GET /new", s.newPage)
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
	inner.Handle("POST /logout", s.protect(maxPostBody, s.logout))
	inner.Handle("POST /scheme", s.protect(maxPostBody, s.setScheme))
	inner.HandleFunc("GET /static/{name}", s.static)
	inner.HandleFunc("GET /files/{dir}/{name...}", s.files)
	// Without this, the mux would redirect /files/avatars to /files/avatars/.
	inner.HandleFunc("GET /files/{dir}", s.notFound)
	// The legacy table runs last: a request that no route above matched.
	inner.HandleFunc("/", s.fallback)
	s.warnHidden(inner)

	root := http.NewServeMux()
	root.Handle("/healthz", Health(pool))
	if s.base == "/" {
		root.Handle("/", inner)
	} else {
		root.Handle(s.base, http.StripPrefix(strings.TrimSuffix(s.base, "/"), inner))
	}
	return secure(s.guard(root)), nil
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

// assetVersion returns a short hash of the stylesheet and the script.
func assetVersion() string {
	h := sha256.New()
	for _, name := range []string{"static/gobb.css", "static/gobb.js"} {
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
	// Nav lists the links of the bar under the header.
	Nav []navLink
	// CSS and JS are the site paths of the stylesheet and the script.
	CSS, JS string
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
	if err != nil {
		return nil, err
	}
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
		Title:      title,
		Scheme:     scheme,
		Settings:   settings,
		Viewer:     v,
		CSS:        s.path("static", "gobb.css") + "?v=" + s.assetVersion,
		JS:         s.path("static", "gobb.js") + "?v=" + s.assetVersion,
		Home:       s.path(),
		Nav:        []navLink{{Label: "Index", Href: s.path(), Current: nav == "index"}},
		Controls:   true,
		Return:     ret,
		Schemes:    choices(scheme),
		csrfSecret: id.secret(),
		ctx:        ctx,
		navKey:     nav,
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
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	if p.Controls {
		secret := p.csrfSecret
		if secret == "" {
			// A guest with no secret yet gets one with the first page that has a form.
			var err error
			if secret, err = newCSRFSecret(); err != nil {
				slog.Error("render: csrf secret", "err", err)
				http.Error(w, "internal server error", http.StatusInternalServerError)
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
			http.Error(w, "internal server error", http.StatusInternalServerError)
			return
		}
		after()
	}
	s.addNav(p)
	var buf bytes.Buffer
	layoutBuilds.Add(1)
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		slog.Error("render", "template", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", p.policy.String())
	if h.Get("Cache-Control") == "" {
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

// fail writes the error page with the given status. When the settings cannot be
// read, it falls back to plain text.
func (s *site) fail(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	s.failLink(w, r, status, heading, message, "", "")
}

// failLink is [site.fail] with a link under the message.
func (s *site) failLink(w http.ResponseWriter, r *http.Request, status int, heading, message, href, label string) {
	p, err := s.begin(w, r, heading, "")
	if errors.Is(err, board.ErrNoSettings) {
		p = s.emptyPage(r, heading)
	} else if err != nil {
		slog.Error("error page", "err", err)
		http.Error(w, heading, status)
		return
	}
	p.Data = errorData{Status: status, Heading: heading, Message: message, Href: href, HrefLabel: label}
	s.render(w, status, "error.html", p)
}

// notFound answers 404 with the layout.
func (s *site) notFound(w http.ResponseWriter, r *http.Request) {
	s.fail(w, r, http.StatusNotFound, "Page not found", "There is no page at this address.")
}

// emptyPage returns the page frame for a database with no board in it: the
// name gobb, the guest zone UTC and the first scheme.
func (s *site) emptyPage(r *http.Request, title string) *page {
	scheme := board.Schemes[0]
	if q := r.URL.Query().Get("scheme"); board.ValidScheme(q) {
		scheme = q
	}
	return &page{
		Title:    title,
		Scheme:   scheme,
		Settings: board.Settings{BoardName: "gobb"},
		Viewer:   board.Viewer{Loc: time.UTC, Scheme: scheme},
		CSS:      s.path("static", "gobb.css") + "?v=" + s.assetVersion,
		JS:       s.path("static", "gobb.js") + "?v=" + s.assetVersion,
		Home:     s.path(),
		Nav:      []navLink{{Label: "Index", Href: s.path()}},
	}
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
