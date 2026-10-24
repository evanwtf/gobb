package web

import (
	"bytes"
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"html/template"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
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
}

// pageFiles are the page templates. Each one defines "content" and runs inside
// layout.html.
var pageFiles = []string{"index.html", "forum.html", "topic.html", "error.html"}

// New returns the handler of the whole server. It serves /healthz at the host
// root, and the site under opt.BasePath. Other paths get 404. New parses the
// templates once, so a broken template fails here and not on a request.
func New(_ context.Context, pool *pgxpool.Pool, opt Options) (http.Handler, error) {
	s := &site{pool: pool, opt: opt}
	if s.opt.Now == nil {
		s.opt.Now = time.Now
	}
	s.base = normalizeBase(opt.BasePath)
	s.assetVersion = assetVersion()
	if err := s.parseTemplates(); err != nil {
		return nil, err
	}

	inner := http.NewServeMux()
	inner.HandleFunc("GET /{$}", s.index)
	inner.HandleFunc("GET /f/{id}", s.forum)
	inner.HandleFunc("GET /t/{id}", s.topic)
	inner.HandleFunc("GET /p/{id}", s.post)
	inner.HandleFunc("GET /a/{id}", s.attachment)
	inner.HandleFunc("GET /static/{name}", s.static)
	inner.HandleFunc("GET /files/{dir}/{name...}", s.files)
	// Without this, the mux would redirect /files/avatars to /files/avatars/.
	inner.HandleFunc("GET /files/{dir}", s.notFound)
	inner.HandleFunc("/", s.notFound)

	root := http.NewServeMux()
	root.Handle("/healthz", Health(pool))
	if s.base == "/" {
		root.Handle("/", inner)
	} else {
		root.Handle(s.base, http.StripPrefix(strings.TrimSuffix(s.base, "/"), inner))
	}
	return s.guard(root), nil
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
		p := r.URL.Path
		if strings.HasSuffix(p, "/") && len(p) > 1 {
			p = p[:len(p)-1]
		}
		if p != "" && p != path.Clean(p) {
			s.notFound(w, r)
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), requestTimeout)
		defer cancel()
		next.ServeHTTP(w, r.WithContext(ctx))
	})
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
	// Data holds the data of the page template.
	Data any
}

// navLink is one link of the nav bar.
type navLink struct {
	Label, Href string
	Current     bool
}

// begin loads what every page needs: the settings and the viewer. It also picks
// the color scheme: a valid ?scheme= value for this one page, otherwise the
// scheme of the viewer.
func (s *site) begin(r *http.Request, title string, nav string) (*page, error) {
	ctx := r.Context()
	settings, err := board.LoadSettings(ctx, s.pool)
	if err != nil {
		return nil, err
	}
	s.noDataLogged.Store(false)
	v, err := board.Guest(ctx, s.pool, settings)
	if err != nil {
		return nil, err
	}
	scheme := v.Scheme
	if q := r.URL.Query().Get("scheme"); board.ValidScheme(q) {
		scheme = q
	}
	p := &page{
		Title:    title,
		Scheme:   scheme,
		Settings: settings,
		Viewer:   v,
		CSS:      s.path("static", "gobb.css") + "?v=" + s.assetVersion,
		JS:       s.path("static", "gobb.js") + "?v=" + s.assetVersion,
		Home:     s.path(),
		Nav:      []navLink{{Label: "Index", Href: s.path(), Current: nav == "index"}},
	}
	if settings.HeaderImage != "" {
		p.HeaderSrc = s.path("files", settings.HeaderImage)
	}
	return p, nil
}

// render executes the page template into a buffer, so that a template error
// gives a clean 500 and not half a page, then writes it with status.
func (s *site) render(w http.ResponseWriter, status int, name string, p *page) {
	t, ok := s.pages[name]
	if !ok {
		slog.Error("render: no such template", "name", name)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	var buf bytes.Buffer
	if err := t.ExecuteTemplate(&buf, "layout", p); err != nil {
		slog.Error("render", "template", name, "err", err)
		http.Error(w, "internal server error", http.StatusInternalServerError)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(status)
	_, _ = w.Write(buf.Bytes())
}

// errorData is the data of error.html.
type errorData struct {
	Status  int
	Heading string
	Message string
}

// fail writes the error page with the given status. When the settings cannot be
// read, it falls back to plain text.
func (s *site) fail(w http.ResponseWriter, r *http.Request, status int, heading, message string) {
	p, err := s.begin(r, heading, "")
	if errors.Is(err, board.ErrNoSettings) {
		p = s.emptyPage(r, heading)
	} else if err != nil {
		slog.Error("error page", "err", err)
		http.Error(w, heading, status)
		return
	}
	p.Data = errorData{Status: status, Heading: heading, Message: message}
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
