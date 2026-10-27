package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

// The admin area: every route under <base>/admin. One gate in front of the area
// lets in only members with the board-wide admin right; anyone else, a guest
// too, gets the themed 404 page, on GET and POST, so the area does not show that
// it exists. Every admin route is on one mux behind that gate, so a new route
// cannot skip it. Every admin page carries noindex and Cache-Control: no-store.

// The limit on the admin writes: each admin gets adminWritesPerWindow POSTs in
// adminWriteWindow. The count lives in memory and starts over with the server.
const (
	adminWritesPerWindow = 120
	adminWriteWindow     = 10 * time.Minute
	adminWriteMaxKeys    = 10_000
)

// passFor is how long a confirmed password lets an admin make the dangerous
// changes on one session.
const passFor = 10 * time.Minute

// adminPasses holds the sessions whose admin confirmed the password, with the
// end of each pass. It lives in memory: a restart asks for the password again.
type adminPasses struct {
	mu   sync.Mutex
	ends map[string]time.Time
}

// grant gives the session sessionID a pass until now + passFor, and drops the
// passes that have ended.
func (a *adminPasses) grant(sessionID string, now time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.ends == nil {
		a.ends = map[string]time.Time{}
	}
	for id, end := range a.ends {
		if !now.Before(end) {
			delete(a.ends, id)
		}
	}
	a.ends[sessionID] = now.Add(passFor)
}

// fresh reports whether the session sessionID holds a pass at now.
func (a *adminPasses) fresh(sessionID string, now time.Time) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	end, ok := a.ends[sessionID]
	return ok && now.Before(end)
}

// newAdminWrites returns the limiter of the admin writes.
func newAdminWrites(opt Options) *ratelimit.Limiter {
	n := adminWritesPerWindow
	if opt.AdminWrites > 0 {
		n = opt.AdminWrites
	}
	return ratelimit.New(n, adminWriteWindow, adminWriteMaxKeys).Named("admin_writes")
}

// adminRoutes is the mux of the admin area. Its methods are the only way to add
// a route: page for a GET, write for a POST through [site.protect] and the limit
// on admin writes, and dangerous for a POST that also needs a confirmed
// password.
type adminRoutes struct {
	*routes
	s *site
}

// page adds the GET route /admin<pattern>.
func (a adminRoutes) page(pattern string, h http.HandlerFunc) {
	a.HandleFunc("GET /admin"+pattern, h)
}

// write adds the POST route /admin<pattern>, with a body of at most limit bytes.
func (a adminRoutes) write(pattern string, limit int64, h http.HandlerFunc) {
	a.Handle("POST /admin"+pattern, a.s.protect(limit, a.s.adminLimited(h)))
}

// dangerous adds the POST route /admin<pattern> for a change that needs a
// confirmed password: a permission change, a change to an owner or an admin, a
// password reset, a ban. It is write with one more step after the token check:
// a session without a pass is sent to the confirm page, which sends the admin
// back to the form field "back", an admin page. The write does not run.
func (a adminRoutes) dangerous(pattern string, limit int64, h http.HandlerFunc) {
	a.write(pattern, limit, func(w http.ResponseWriter, r *http.Request) {
		if !a.s.passFresh(r) {
			http.Redirect(w, r, a.s.confirmHref(a.s.adminNext(r.PostForm.Get("back"))), http.StatusSeeOther)
			return
		}
		h(w, r)
	})
}

// adminArea returns the handler of every path under /admin, and records its
// patterns on the site.
func (s *site) adminArea() http.Handler {
	a := adminRoutes{routes: newRoutes(), s: s}
	a.page("/{$}", s.adminIndex)
	a.page("/log", s.adminLog)
	a.page("/confirm", s.adminConfirmPage)
	a.write("/confirm", maxLoginBody, s.adminConfirm)
	s.adminMemberRoutes(a)
	s.adminBansRoutes(a)
	s.adminForumRoutes(a)
	// A path of the area that no route above matches, and a method that none
	// takes, get the themed 404, not the plain text of the mux.
	a.HandleFunc("/admin/", s.notFound)
	a.HandleFunc("/admin", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			s.notFound(w, r)
			return
		}
		http.Redirect(w, r, s.path("admin")+"/", http.StatusMovedPermanently)
	})
	s.patterns = append(s.patterns, a.patterns...)
	return s.adminGate(a)
}

// adminGate lets a request through only when its visitor is a signed-in member
// with the board-wide admin right. Anyone else gets the 404 page that any
// missing path gets. It reads the session and the rights, not the body, so a
// POST is refused before anything reads it.
func (s *site) adminGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := s.identify(w, r)
		if err != nil {
			// A failure answers as a missing page does when the database fails: the
			// 404 from the settings in memory. A 500 here would show that the area
			// exists.
			slog.Warn("admin gate: cannot read the session", "err", err)
			s.notFound(w, r)
			return
		}
		// The identity goes into the request, so begin and protect do not look the
		// session up again.
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, id))
		if id.token == "" {
			s.notFound(w, r)
			return
		}
		settings, err := board.LoadSettings(r.Context(), s.pool)
		var v board.Viewer
		if err == nil {
			v, err = board.Member(r.Context(), s.pool, id.sess.UserID, settings)
		}
		switch {
		case errors.Is(err, board.ErrNotFound) || (err == nil && !v.Perms.IsAdmin()):
			s.notFound(w, r)
			return
		case err != nil:
			slog.Warn("admin gate: cannot read the rights", "err", err)
			s.notFound(w, r)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// adminLimited counts an admin write against the limit of the admin, and
// answers 429 when the limit is reached. It runs after protect, so only a POST
// with a good token counts.
func (s *site) adminLimited(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, _ := r.Context().Value(identityKey{}).(identity)
		if ok, wait := s.adminWrites.Take(strconv.FormatInt(id.sess.UserID, 10), s.opt.Now()); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
			s.fail(w, r, http.StatusTooManyRequests, "Too many changes",
				"You made many changes in a short time. Wait a few minutes and try again.")
			return
		}
		h(w, r)
	}
}

// adminBegin is [site.begin] for an admin page: the nav marks the admin link,
// and the page carries noindex.
func (s *site) adminBegin(w http.ResponseWriter, r *http.Request, title string) (*page, error) {
	p, err := s.begin(w, r, title, "admin")
	if err != nil {
		return nil, err
	}
	p.Noindex = true
	return p, nil
}

// adminCrumbs returns the trail Index > Admin.
func (s *site) adminCrumbs() []crumb {
	return []crumb{{Label: "Index", Href: s.path()}, {Label: "Admin", Href: s.path("admin") + "/"}}
}

// adminIndexData is the data of admin.html.
type adminIndexData struct {
	Crumbs []crumb
	// Sections are the pages of the area, each with a line that says what it is
	// for.
	Sections []adminSection
}

// adminSection is one link of the admin index.
type adminSection struct {
	Label, Href, About string
}

// adminIndex serves GET /admin/.
func (s *site) adminIndex(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Admin")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	p.Data = adminIndexData{
		Crumbs: s.adminCrumbs()[:1],
		Sections: []adminSection{
			{Label: "Members", Href: s.path("admin", "members"), About: "Find a member; activate, deactivate, reset a password, set a rank."},
			{Label: "Log", Href: s.path("admin", "log"), About: "Every moderator and admin action, newest first."},
			{Label: "Bans", Href: s.path("admin", "bans"), About: "Ban a member, an email address or an IP range, and lift a ban."},
			{Label: "Forums", Href: s.path("admin", "forums"), About: "Create, edit, nest, reorder and lock the categories, forums and links."},
		},
	}
	s.render(w, http.StatusOK, "admin.html", p)
}

// adminLogData is the data of admin-log.html.
type adminLogData struct {
	Crumbs  []crumb
	Entries []adminLogRow
	Pager   *pager
	// The values of the filter form, as sent, and the choices of its lists.
	Action, Actor, TargetType, TargetID string
	NoActor                             bool
	Actions, Types                      []string
	// Filtered is true when any filter is set. Notice explains a filter that
	// matched no member.
	Filtered bool
	Notice   string
}

// adminLogRow is one row of the log page.
type adminLogRow struct {
	board.LogEntry
	// ActorText names an actor that is not a member: the command line, a reset
	// link, or a member who is gone.
	ActorText string
	// TargetHref is the link to the target, or empty.
	TargetHref string
}

// adminLog serves GET /admin/log.
func (s *site) adminLog(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	page, err := board.ParsePage(q.Get("page"))
	if err != nil {
		s.badPage(w, r)
		return
	}
	p, err := s.adminBegin(w, r, "Log")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := r.Context()
	d := adminLogData{Crumbs: s.adminCrumbs(), Action: q.Get("action"), Actor: strings.TrimSpace(q.Get("actor")),
		TargetType: q.Get("type"), TargetID: strings.TrimSpace(q.Get("target")), NoActor: q.Get("noactor") == "1"}
	f := board.LogFilter{Action: d.Action, TargetType: d.TargetType}
	d.Filtered = d.Action != "" || d.Actor != "" || d.TargetType != "" || d.TargetID != "" || d.NoActor
	none := false
	switch {
	case d.NoActor:
		f.Actor = board.LogNoActor
	case d.Actor != "":
		id, _, err := board.UserByName(ctx, s.pool, d.Actor)
		switch {
		case errors.Is(err, board.ErrNotFound):
			d.Notice, none = "No member has that name.", true
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		f.Actor = id
	}
	if d.TargetID != "" {
		id, err := strconv.ParseInt(d.TargetID, 10, 64)
		if err != nil || id < 1 {
			d.Notice, none = "The target ID must be a whole number above 0.", true
		}
		f.TargetID = id
	}
	if d.Actions, d.Types, err = board.LogChoices(ctx, s.pool); err != nil {
		s.serverError(w, r, err)
		return
	}
	if !none {
		lp, err := board.ModLog(ctx, s.pool, f, page)
		switch {
		case errors.Is(err, board.ErrNotFound):
			s.notFound(w, r)
			return
		case err != nil:
			s.serverError(w, r, err)
			return
		}
		for _, e := range lp.Entries {
			d.Entries = append(d.Entries, s.logEntryRow(e))
		}
		d.Pager = newPagerWith(func(n int) string { return s.logHref(q, n) }, lp.Page, lp.Pages)
	}
	p.Data = d
	s.render(w, http.StatusOK, "admin-log.html", p)
}

// logEntryRow adds what the log page shows to an entry: the name of an actor that
// is not a member, and the link to the target.
func (s *site) logEntryRow(e board.LogEntry) adminLogRow {
	row := adminLogRow{LogEntry: e}
	if e.Actor.ID == 0 {
		switch e.Via {
		case "cli":
			row.ActorText = "Command line"
		case "email":
			row.ActorText = "Reset link"
		default:
			row.ActorText = "A member who is gone"
		}
	}
	id := strconv.FormatInt(e.TargetID, 10)
	switch e.TargetType {
	case "user":
		row.TargetHref = s.path("u", id)
	case "topic":
		row.TargetHref = s.path("t", id)
	case "post":
		row.TargetHref = s.path("p", id)
	case "forum":
		row.TargetHref = s.path("f", id)
	}
	return row
}

// logHref returns the link to page n of the log with the filters of q.
func (s *site) logHref(q url.Values, n int) string {
	v := url.Values{}
	for _, k := range []string{"action", "actor", "noactor", "type", "target"} {
		if x := q.Get(k); x != "" {
			v.Set(k, x)
		}
	}
	if n > 1 {
		v.Set("page", strconv.Itoa(n))
	}
	href := s.path("admin", "log")
	if len(v) > 0 {
		href += "?" + v.Encode()
	}
	return href
}

// adminConfirmData is the data of admin-confirm.html.
type adminConfirmData struct {
	Crumbs []crumb
	// Next is the admin page that the form goes back to.
	Next string
	// Error is the message of a refused password, beside the field when Field is
	// true and on the whole form otherwise.
	Error string
	Field bool
}

// adminNext returns the value of next when it is a path of the admin area, and
// the admin index otherwise. The form sends the admin back there, so it must not
// lead off the area or off the site.
func (s *site) adminNext(next string) string {
	area := s.path("admin") + "/"
	u, err := url.Parse(next)
	if err != nil || u.Scheme != "" || u.Host != "" || u.User != nil || !strings.HasPrefix(u.Path, area) ||
		!isCleanPath(u.Path) || strings.ContainsAny(next, "\\\r\n") {
		return area
	}
	return next
}

// confirmHref returns the link to the confirm page that sends the admin back to
// next, an admin path.
func (s *site) confirmHref(next string) string {
	return s.path("admin", "confirm") + "?" + url.Values{"next": {next}}.Encode()
}

// passFresh reports whether the session of r holds a confirmed password.
func (s *site) passFresh(r *http.Request) bool {
	id, _ := r.Context().Value(identityKey{}).(identity)
	return id.token != "" && s.passes.fresh(id.sess.ID, s.opt.Now())
}

// adminConfirmPage serves GET /admin/confirm: the form that asks the admin for
// the password before a dangerous change.
func (s *site) adminConfirmPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Confirm your password")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	s.renderConfirm(w, http.StatusOK, p, adminConfirmData{Next: s.adminNext(r.URL.Query().Get("next"))})
}

// adminConfirm serves POST /admin/confirm. A right password gives the session a
// pass for 10 minutes and sends the admin back to next. The check is the one of
// the account page, with its own limit of tries.
func (s *site) adminConfirm(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Confirm your password")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := adminConfirmData{Next: s.adminNext(r.PostForm.Get("next"))}
	// The tries share the key of the password form of the account page, so a
	// stolen session gets no more guesses here than there.
	_, f, err := s.checkOwnPassword(r, p, "password", r.PostForm.Get("password"))
	switch {
	case err != nil:
		s.serverError(w, r, err)
		return
	case f != nil:
		if f.retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(f.retry))
		}
		d.Error, d.Field = f.msg, f.field
		s.renderConfirm(w, f.status, p, d)
		return
	}
	id, _ := r.Context().Value(identityKey{}).(identity)
	s.passes.grant(id.sess.ID, s.opt.Now())
	http.Redirect(w, r, d.Next, http.StatusSeeOther)
}

// renderConfirm writes the confirm page with d.
func (s *site) renderConfirm(w http.ResponseWriter, status int, p *page, d adminConfirmData) {
	d.Crumbs = s.adminCrumbs()
	p.Data = d
	s.render(w, status, "admin-confirm.html", p)
}
