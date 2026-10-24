package web

import (
	"errors"
	"net/http"
	"net/netip"
	"net/url"
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
	// otherwise.
	token string
	sess  session.Session
	// dead is true when the request held a session cookie that names no live
	// session. The page then removes the cookie.
	dead bool
	// guestSecret is the value of the gobb_csrf cookie, or empty.
	guestSecret string
}

// secret returns the secret that the csrf token of the visitor is made from: the
// session token of a member, and the gobb_csrf cookie of a guest.
func (id identity) secret() string {
	if id.token != "" {
		return id.token
	}
	return id.guestSecret
}

// identify reads the cookies of r and looks the session up.
func (s *site) identify(r *http.Request) (identity, error) {
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
		id.dead = true
	case err != nil:
		return id, err
	default:
		id.token, id.sess = c.Value, sess
	}
	return id, nil
}

// clientIP returns the address of the client of r. When it cannot be read, it
// returns the unspecified address, so that the session still has one.
func (s *site) clientIP(r *http.Request) netip.Addr {
	if ip := ClientIP(r, s.opt.TrustedProxies); ip.IsValid() {
		return ip
	}
	return netip.IPv4Unspecified()
}

// sitePath returns raw when it is a site path under the base path, and empty
// otherwise. A site path starts with the base path, is not scheme-relative
// ("//host"), has no backslash or control character (a browser can read "/\" as
// "//"), and has no scheme or host.
func (s *site) sitePath(raw string) string {
	if raw == "" || !utf8.ValidString(raw) || !strings.HasPrefix(raw, s.base) {
		return ""
	}
	for _, r := range raw {
		if r < 0x20 || r == 0x7f || r == '\\' {
			return ""
		}
	}
	if strings.HasPrefix(raw, "//") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "" || u.Host != "" || u.Opaque != "" {
		return ""
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
	s.renderLogin(w, p, http.StatusOK, loginData{Next: next})
}

// renderLogin draws the sign-in page. The page is never cached: it holds a csrf
// token, and an answer that a proxy kept would hold the token of someone else.
func (s *site) renderLogin(w http.ResponseWriter, p *page, status int, d loginData) {
	p.Return = s.path("login")
	if d.Next != s.path() {
		p.Return += "?next=" + url.QueryEscape(d.Next)
	}
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
	userID, err := s.login.Check(r.Context(), d.Name, form.Get("password"), ip, now)
	var banned *login.BannedError
	switch {
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

	token, sess, err := session.Create(r.Context(), s.pool, userID, d.Remember, ip, r.UserAgent(), now)
	if err != nil {
		s.serverError(w, r, err)
		return
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
	http.SetCookie(w, s.cookie(sessionCookie, token, maxAge))
	http.Redirect(w, r, d.Next, http.StatusSeeOther)
}

// logout serves POST /logout.
func (s *site) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(sessionCookie); err == nil && c.Value != "" {
		if err := session.Delete(r.Context(), s.pool, c.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}
	http.SetCookie(w, s.cookie(sessionCookie, "", -1))
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
	id, err := s.identify(r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	saved := false
	if id.token != "" {
		tag, err := s.pool.Exec(r.Context(), `update users set color_scheme = $2 where id = $1 and state = 'active'`, id.sess.UserID, scheme)
		if err != nil {
			s.serverError(w, r, err)
			return
		}
		saved = tag.RowsAffected() == 1
	}
	if !saved {
		http.SetCookie(w, s.cookie(schemeCookie, scheme, rememberSeconds))
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
