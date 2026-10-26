package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/session"
)

// The names of the cookies.
const (
	sessionCookie = "gobb_session"
	csrfCookie    = "gobb_csrf"
	schemeCookie  = "gobb_scheme"
)

// rememberSeconds is the lifetime of the cookie of a "remember me" session and
// of the scheme cookie of a guest.
const rememberSeconds = int(session.RememberFor / time.Second)

// cookie returns a cookie with the attributes that all gobb cookies share:
// HttpOnly, SameSite=Lax, the base path, and Secure unless the options say that
// the site runs on plain HTTP. A maxAge of 0 makes a session cookie, and a
// negative one removes the cookie.
func (s *site) cookie(name, value string, maxAge int) *http.Cookie {
	return &http.Cookie{
		Name: name, Value: value, Path: s.base, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteLaxMode, Secure: s.opt.CookieSecure,
	}
}

// identity is what the cookies of a request say about the visitor.
type identity struct {
	// token is the session token when it names a live session, and empty
	// otherwise. A live session is one that has not expired and whose user is
	// active (session.Lookup).
	token string
	sess  session.Session
	// guestSecret is the value of the gobb_csrf cookie, or empty.
	guestSecret string
	// ended is true when the request carried a session cookie that names no
	// live session: the member's session ended, and the member is a guest now.
	ended bool
}

// identityKey is the context key under which protect keeps the identity of a
// POST, so that the handler does not look the session up again.
type identityKey struct{}

// secret returns the secret that the csrf token of the visitor is made from: the
// session token of a member, and the gobb_csrf cookie of a guest.
func (id identity) secret() string {
	if id.token != "" {
		return id.token
	}
	return id.guestSecret
}

// identify reads the cookies of r and looks the session up. It is the one place
// that decides whether a visitor is a member, for every route. A session cookie
// that names no live session (unknown, expired, deleted, or of a user who is not
// active) is dead: identify deletes its row if any, sends a cookie that removes
// it, and returns the identity of a guest, with the guest's csrf secret.
//
// A request that already went through identify (a POST in protect) gets the
// same identity back without a second lookup.
func (s *site) identify(w http.ResponseWriter, r *http.Request) (identity, error) {
	if id, ok := r.Context().Value(identityKey{}).(identity); ok {
		return id, nil
	}
	var id identity
	if c, err := r.Cookie(csrfCookie); err == nil && len(c.Value) <= 128 {
		id.guestSecret = c.Value
	}
	c, err := r.Cookie(sessionCookie)
	if err != nil || c.Value == "" {
		return id, nil
	}
	sess, err := session.Lookup(r.Context(), s.pool, c.Value, s.opt.Now())
	switch {
	case errors.Is(err, session.ErrNone):
		s.dropSession(w, r, c.Value)
		id.ended = true
	case err != nil:
		return id, err
	default:
		id.token, id.sess = c.Value, sess
	}
	return id, nil
}

// endSession ends the live session of id and returns the identity of a guest.
func (s *site) endSession(w http.ResponseWriter, r *http.Request, id identity) identity {
	s.dropSession(w, r, id.token)
	id.token, id.sess = "", session.Session{}
	return id
}

// dropSession deletes the row of the session that has token and removes the
// cookie. A failure to delete is logged: the row is dead anyway.
func (s *site) dropSession(w http.ResponseWriter, r *http.Request, token string) {
	if err := session.Delete(r.Context(), s.pool, token); err != nil {
		slog.Warn("cannot delete a dead session", "err", err)
	}
	s.setCookie(w, s.cookie(sessionCookie, "", -1))
}

// setCookie sets c and drops any earlier Set-Cookie line for the same name, so
// that a response never holds two lines for one cookie. A dead session's clear
// and the new session's cookie, for example, must not both go out.
func (s *site) setCookie(w http.ResponseWriter, c *http.Cookie) {
	h := w.Header()
	var kept []string
	for _, line := range h.Values("Set-Cookie") {
		if !strings.HasPrefix(line, c.Name+"=") {
			kept = append(kept, line)
		}
	}
	h.Del("Set-Cookie")
	for _, line := range kept {
		h.Add("Set-Cookie", line)
	}
	http.SetCookie(w, c)
}

// clientIP returns the address of the client of r. When it cannot be read, it
// returns the unspecified address, so that the session still has one.
func (s *site) clientIP(r *http.Request) netip.Addr {
	if ip := ClientIP(r, s.opt.TrustedProxies); ip.IsValid() {
		return ip
	}
	return netip.IPv4Unspecified()
}

// sitePath returns raw, cleaned, when it is a site path under the base path, and
// empty otherwise. A site path starts with "/", is not scheme-relative
// ("//host"), has no backslash or control character even after percent-decoding
// (a browser can read "/\" as "//"), and has no scheme or host. Dot segments are
// removed first, written plain or as %2e, and the base path is checked on the
// result, so "/forum/../x" cannot leave "/forum/". A path with nothing to clean
// comes back unchanged.
func (s *site) sitePath(raw string) string {
	if raw == "" || !utf8.ValidString(raw) || raw[0] != '/' || strings.HasPrefix(raw, "//") {
		return ""
	}
	bad := func(text string) bool {
		for _, r := range text {
			if r < 0x20 || r == 0x7f || r == '\\' {
				return true
			}
		}
		return false
	}
	if bad(raw) {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" || bad(u.Path) {
		return ""
	}
	clean := path.Clean(u.Path)
	if strings.HasSuffix(u.Path, "/") && clean != "/" {
		clean += "/"
	}
	if !strings.HasPrefix(clean, s.base) {
		return ""
	}
	if clean != u.Path {
		u.Path, u.RawPath = clean, ""
		return u.String()
	}
	return raw
}

// safeNext returns the place to send a member after a sign-in: raw when it is a
// site path that is not one of the sign-in routes, and the index otherwise. The
// check keeps a sign-in from sending the member in a circle.
func (s *site) safeNext(raw string) string {
	p := s.sitePath(raw)
	if p == "" {
		return s.path()
	}
	u, _ := url.Parse(p)
	for _, route := range []string{"login", "logout", "scheme"} {
		if strings.TrimSuffix(u.Path, "/") == s.path(route) {
			return s.path()
		}
	}
	return p
}

// returnPath returns the site path of r with its query, less the ?scheme=
// preview. A form on the page sends it back as next.
func (s *site) returnPath(r *http.Request) string {
	p := strings.TrimSuffix(s.base, "/") + r.URL.EscapedPath()
	q := r.URL.Query()
	q.Del("scheme")
	if enc := q.Encode(); enc != "" {
		p += "?" + enc
	}
	if s.sitePath(p) == "" {
		return s.path()
	}
	return p
}

// schemeChoice is one button of the scheme picker.
type schemeChoice struct {
	Value, Label string
	Current      bool
}

// schemeLabels are the names of the schemes in the picker.
var schemeLabels = map[string]string{"slate": "Slate", "slate-deep": "Slate deep", "slate-light": "Slate light"}

// choices returns the picker buttons, with the scheme in use marked.
func choices(current string) []schemeChoice {
	out := make([]schemeChoice, 0, len(board.Schemes))
	for _, v := range board.Schemes {
		out = append(out, schemeChoice{Value: v, Label: schemeLabels[v], Current: v == current})
	}
	return out
}

// loginData is the data of login.html.
type loginData struct {
	Next     string
	Name     string
	Remember bool
	// Error is the headline of a failed sign-in, and Detail the line under it.
	Error, Detail string
	// Reset shows the notice that a reset changed the password. Forgot shows the
	// link to the reset form; renderLogin sets it when email is on.
	Reset, Forgot bool
}

// loginPage serves GET /login. A member who opens it goes on to next at once.
func (s *site) loginPage(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "Sign in", "login")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	next := s.safeNext(r.URL.Query().Get("next"))
	if p.Viewer.UserID != 0 {
		http.Redirect(w, r, next, http.StatusSeeOther)
		return
	}
	s.renderLogin(w, p, http.StatusOK, loginData{Next: next, Reset: r.URL.Query().Get("reset") == "1"})
}

// renderLogin draws the sign-in page. The page is never cached: it holds a csrf
// token, and an answer that a proxy kept would hold the token of someone else.
func (s *site) renderLogin(w http.ResponseWriter, p *page, status int, d loginData) {
	p.Return = s.path("login")
	if d.Next != s.path() {
		p.Return += "?next=" + url.QueryEscape(d.Next)
	}
	d.Forgot = s.opt.Mail != nil
	d.Reset = d.Reset && d.Forgot
	p.Data = d
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, status, "login.html", p)
}

// loginSubmit serves POST /login.
func (s *site) loginSubmit(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "Sign in", "login")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	form := r.PostForm
	d := loginData{
		Next:     s.safeNext(form.Get("next")),
		Name:     form.Get("name"),
		Remember: form.Get("remember") != "",
	}
	ip := s.clientIP(r)
	now := s.opt.Now()
	// The session starts inside the check, as its last step, and only while the
	// account still holds the password epoch that the check read and no user ban
	// holds. A password change, a reset or a ban that lands between the check and
	// the insert makes the sign-in a wrong password, counted and under the floor.
	var (
		token string
		sess  session.Session
	)
	userID, err := s.login.CheckWith(r.Context(), d.Name, form.Get("password"), ip, now,
		func(ctx context.Context, id, epoch int64) error {
			if beforeSession != nil {
				beforeSession()
			}
			var err error
			token, sess, err = session.CreateChecked(ctx, s.pool, id, epoch, d.Remember, ip, r.UserAgent(), now)
			if errors.Is(err, session.ErrStale) {
				return login.ErrStale
			}
			return err
		})
	var banned *login.BannedError
	var limited *login.LimitedError
	switch {
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(limited.RetryAfter)))
		d.Error = limitMessage(limited)
		s.renderLogin(w, p, http.StatusTooManyRequests, d)
		return
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		d.Error = "The server is busy. Try again in a few seconds."
		s.renderLogin(w, p, http.StatusServiceUnavailable, d)
		return
	case errors.Is(err, login.ErrWrong):
		d.Error = "The name or password is wrong."
		s.renderLogin(w, p, http.StatusOK, d)
		return
	case errors.Is(err, login.ErrAmbiguous):
		d.Error = "This email address belongs to more than one account. Sign in with your username."
		s.renderLogin(w, p, http.StatusOK, d)
		return
	case errors.Is(err, login.ErrInactive):
		d.Error = "This account is not active."
		s.renderLogin(w, p, http.StatusForbidden, d)
		return
	case errors.As(err, &banned):
		d.Error = "You cannot sign in because you are banned."
		if banned.Reason != "" {
			d.Detail = "Reason: " + banned.Reason
		}
		s.renderLogin(w, p, http.StatusForbidden, d)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}

	if err := board.StartVisit(r.Context(), s.pool, userID, now); err != nil {
		slog.Warn("cannot start a visit", "err", err)
	}
	// A sign-in on top of another one ends the old session.
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := session.Delete(r.Context(), s.pool, c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	maxAge := 0
	if sess.Remember {
		maxAge = rememberSeconds
	}
	s.setCookie(w, s.cookie(sessionCookie, token, maxAge))
	http.Redirect(w, r, d.Next, http.StatusSeeOther)
}

// beforeSession, when set, runs in a sign-in after the password is verified and
// before the session is inserted. A test uses it to hold a sign-in at that point.
var beforeSession func()

// busyRetrySeconds is the Retry-After of a sign-in that found the gate full.
const busyRetrySeconds = 5

// retrySeconds returns d in whole seconds, rounded up, and at least 1, for the
// Retry-After header.
func retrySeconds(d time.Duration) int {
	return max(1, int((d+time.Second-1)/time.Second))
}

// waitText returns d in whole minutes, rounded up, and at least one: "1 minute"
// or "N minutes". It never says "0 minutes".
func waitText(d time.Duration) string {
	n := max(1, int((d+time.Minute-1)/time.Minute))
	if n == 1 {
		return "1 minute"
	}
	return strconv.Itoa(n) + " minutes"
}

// toIndex answers 303 to the board index. It is the answer of "Sign out" for a
// visitor with no live session: there is nothing to sign out of, and nothing
// for the csrf token to protect.
func (s *site) toIndex(w http.ResponseWriter, r *http.Request) {
	http.Redirect(w, r, s.path(), http.StatusSeeOther)
}

// logout serves POST /logout.
func (s *site) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := session.Delete(r.Context(), s.pool, c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	s.setCookie(w, s.cookie(sessionCookie, "", -1))
	http.Redirect(w, r, s.path(), http.StatusSeeOther)
}

// setScheme serves POST /scheme. A member's choice goes into the member's row;
// a guest's goes into a cookie for a year.
func (s *site) setScheme(w http.ResponseWriter, r *http.Request) {
	scheme := r.PostForm.Get("scheme")
	if !board.ValidScheme(scheme) {
		s.fail(w, r, http.StatusBadRequest, "Bad request", "That color scheme does not exist.")
		return
	}
	id, err := s.identify(w, r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	saved := false
	if id.token != "" {
		if saved, err = board.SetScheme(r.Context(), s.pool, id.sess.UserID, scheme); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	if !saved {
		s.setCookie(w, s.cookie(schemeCookie, scheme, rememberSeconds))
	}
	next := s.sitePath(r.PostForm.Get("next"))
	if next == "" {
		next = s.path()
	}
	http.Redirect(w, r, next, http.StatusSeeOther)
}

// loginHref returns the link to the sign-in page for a guest on the page at
// ret. The index needs no next.
func (s *site) loginHref(ret string) string {
	href := s.path("login")
	if ret != s.path() {
		href += "?next=" + url.QueryEscape(ret)
	}
	return href
}
