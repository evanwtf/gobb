package web

import (
	"errors"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/session"
)

// The account page and the three changes it makes. Each change is a POST through
// site.protect (or protectPosting for the signature). Every password field is
// empty on every answer, and no code here logs or stores a raw password.

// The cap of the body of the password and email forms. A form holds up to three
// passwords of 1,024 bytes (a current one can be up to 4,096 bytes) and an
// address, and a 3-byte character percent-encodes to 9 bytes.
const maxAccountBody = 48 << 10

// doneNotices are the three notices that /account?done=... can show. Any other
// value shows nothing.
var doneNotices = map[string]string{
	"password":  "Your password is changed.",
	"email":     "Your email address is changed.",
	"signature": "Your signature is saved.",
}

// The texts of the account forms.
const (
	msgWrongCurrent   = "That is not your current password."
	msgConfirmDiffers = "The two new passwords differ."
	msgBanned         = "You cannot make this change because you are banned."
	msgInactive       = "This account is not active."
	msgBusy           = "The server is busy. Try again in a few seconds."
	msgSignatureFail  = "Something went wrong, and your signature was not saved. Your text is below. Try again in a moment."
	msgPasswordFail   = "Something went wrong, and nothing was changed. Try again in a moment."
)

// passwordErr, emailErr and signatureErr are the messages of the three forms.
// Notice is for the whole form; the others sit beside their field.
type passwordErr struct{ Notice, Current, Next, Confirm string }
type emailErr struct{ Notice, Email, Current string }
type signatureErr struct{ Notice, Text string }

// accountData is the data of account.html.
type accountData struct {
	board.AccountPage
	// Done is the fixed notice of a change that succeeded, or empty.
	Done string
	// Email and Signature are the values of the fields: the stored ones, or on a
	// failed POST of that form, the text that the member sent. Passwords have no
	// field in this struct, so no answer can hold one.
	Email, Signature string
	Pass             passwordErr
	Mail             emailErr
	Sig              signatureErr
}

// account serves GET /account: a guest goes to sign in and comes back here; a
// member sees the three forms.
func (s *site) account(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "Account", "")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.Viewer.UserID == 0 {
		http.Redirect(w, r, s.loginHref(s.path("account")), http.StatusFound)
		return
	}
	d, err := s.accountData(r, p)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Done = doneNotices[r.URL.Query().Get("done")]
	s.renderAccount(w, http.StatusOK, p, d)
}

// accountData loads the stored values of the account page.
func (s *site) accountData(r *http.Request, p *page) (accountData, error) {
	ap, err := board.Account(r.Context(), s.pool, p.Viewer)
	if err != nil {
		return accountData{}, err
	}
	return accountData{AccountPage: ap, Email: ap.Email, Signature: ap.Signature}, nil
}

// renderAccount writes the account page. The forms of the layout return to the
// account page, not to the address of a POST.
func (s *site) renderAccount(w http.ResponseWriter, status int, p *page, d accountData) {
	p.Return = s.path("account")
	p.Title = "Account"
	p.Data = d
	s.render(w, status, "account.html", p)
}

// accountMember starts a POST handler of the account page. It returns the page
// and the data, or answers and reports false. A guest, and a member who lacks
// option, get 403 before any check of a password or any hash.
func (s *site) accountMember(w http.ResponseWriter, r *http.Request, option, action string) (*page, accountData, bool) {
	p, err := s.begin(w, r, "Account", "")
	if err != nil {
		s.serverError(w, r, err)
		return nil, accountData{}, false
	}
	if p.Viewer.UserID == 0 || !p.Viewer.Perms.Has(option, 0) {
		s.deniedTo(w, p, action)
		return nil, accountData{}, false
	}
	d, err := s.accountData(r, p)
	if err != nil {
		s.serverError(w, r, err)
		return nil, accountData{}, false
	}
	return p, d, true
}

// limitMessage is the text of a sign-in limit. /login and the account page say
// the same words.
func limitMessage(l *login.LimitedError) string {
	return "Too many sign-in attempts. Try again in " + waitText(l.RetryAfter) + "."
}

// verifyCurrent checks the current password of the member through the sign-in
// check, with the member's own username and the address of the request, so the
// limits, the gate and the floor of /login apply. A wrong password counts as a
// failed sign-in. It reports whether the password is right; when it is not, it
// has answered. The notice and field functions put the message on the form that
// was sent.
func (s *site) verifyCurrent(w http.ResponseWriter, r *http.Request, p *page, d accountData, pw string,
	wrong func(*accountData, string), notice func(*accountData, string)) bool {
	id, err := s.login.Check(r.Context(), p.Viewer.Name, pw, s.clientIP(r), s.opt.Now())
	var banned *login.BannedError
	var limited *login.LimitedError
	switch {
	case err == nil && id == p.Viewer.UserID:
		return true
	case errors.As(err, &limited):
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(limited.RetryAfter)))
		notice(&d, limitMessage(limited))
		s.renderAccount(w, http.StatusTooManyRequests, p, d)
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		notice(&d, msgBusy)
		s.renderAccount(w, http.StatusServiceUnavailable, p, d)
	case err == nil, errors.Is(err, login.ErrWrong), errors.Is(err, login.ErrAmbiguous):
		// err == nil with another ID cannot happen (the username is unique); it is
		// refused like a wrong password.
		wrong(&d, msgWrongCurrent)
		s.renderAccount(w, http.StatusBadRequest, p, d)
	case errors.Is(err, login.ErrInactive):
		notice(&d, msgInactive)
		s.renderAccount(w, http.StatusForbidden, p, d)
	case errors.As(err, &banned):
		notice(&d, msgBanned)
		s.renderAccount(w, http.StatusForbidden, p, d)
	default:
		s.serverError(w, r, err)
	}
	return false
}

// hold takes the gate for the hash of a new password. It reports whether it got
// the share; when not, it has answered 503.
func (s *site) hold(w http.ResponseWriter, r *http.Request, p *page, d accountData, notice func(*accountData, string)) (func(), bool) {
	release, err := s.login.Hold(r.Context())
	switch {
	case err == nil:
		return release, true
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		notice(&d, msgBusy)
		s.renderAccount(w, http.StatusServiceUnavailable, p, d)
	default:
		s.serverError(w, r, err)
	}
	return nil, false
}

// writeRefused answers the error of a board account write and reports whether it
// did. An input error is 400 beside its field; a missing right is 403; a member
// who is gone is 404; anything else is a failure of the database, with a fixed
// notice.
func (s *site) writeRefused(w http.ResponseWriter, r *http.Request, p *page, d accountData, err error,
	field func(*accountData, *board.InputError), notice func(*accountData, string), action string) bool {
	var in *board.InputError
	switch {
	case err == nil:
		return false
	case errors.As(err, &in):
		field(&d, in)
		s.renderAccount(w, http.StatusBadRequest, p, d)
	case errors.Is(err, board.ErrForbidden):
		s.deniedTo(w, p, action)
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
	default:
		slog.Error("account write failed", "path", r.URL.Path, "err", err)
		notice(&d, msgPasswordFail)
		s.renderAccount(w, failStatus(r, err), p, d)
	}
	return true
}

// changePassword serves POST /account/password.
func (s *site) changePassword(w http.ResponseWriter, r *http.Request) {
	const action = "change your password"
	p, d, ok := s.accountMember(w, r, "account.password", action)
	if !ok {
		return
	}
	form := r.PostForm
	current, next := form.Get("current"), form.Get("next")
	passNotice := func(d *accountData, m string) { d.Pass.Notice = m }
	passField := func(d *accountData, in *board.InputError) { d.Pass.Next = in.Message }
	// The cheap checks come first: a bad new password costs no hash.
	if err := board.CheckNewPassword(r.Context(), s.pool, p.Viewer, next); s.writeRefused(w, r, p, d, err, passField, passNotice, action) {
		return
	}
	if form.Get("confirm") != next {
		d.Pass.Confirm = msgConfirmDiffers
		s.renderAccount(w, http.StatusBadRequest, p, d)
		return
	}
	if !s.verifyCurrent(w, r, p, d, current, func(d *accountData, m string) { d.Pass.Current = m }, passNotice) {
		return
	}
	release, ok := s.hold(w, r, p, d, passNotice)
	if !ok {
		return
	}
	err := board.ChangePassword(r.Context(), s.pool, p.Viewer, next, s.opt.Now())
	release()
	if s.writeRefused(w, r, p, d, err, passField, passNotice, action) {
		return
	}
	s.startNewSession(w, r, p)
}

// startNewSession ends the password change: every session of the member is gone
// (ChangePassword deleted them with the new hash), so one new session starts
// for this browser, with the "remember me" choice of the old one. The csrf token
// follows the session, so the page after the redirect carries a new token.
func (s *site) startNewSession(w http.ResponseWriter, r *http.Request, p *page) {
	id, err := s.identify(w, r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := s.opt.Now()
	token, sess, err := session.Create(r.Context(), s.pool, p.Viewer.UserID, id.sess.Remember, s.clientIP(r), r.UserAgent(), now)
	if err != nil {
		// The password is changed and the old sessions are gone. Send the member
		// to sign in with the new one.
		slog.Error("account: cannot start a session after a password change", "user", p.Viewer.UserID, "err", err)
		s.setCookie(w, s.cookie(sessionCookie, "", -1))
		http.Redirect(w, r, s.loginHref(s.path("account")), http.StatusSeeOther)
		return
	}
	maxAge := 0
	if sess.Remember {
		maxAge = rememberSeconds
	}
	s.setCookie(w, s.cookie(sessionCookie, token, maxAge))
	http.Redirect(w, r, s.path("account")+"?done=password", http.StatusSeeOther)
}

// changeEmail serves POST /account/email.
func (s *site) changeEmail(w http.ResponseWriter, r *http.Request) {
	const action = "change your email address"
	p, d, ok := s.accountMember(w, r, "account.email", action)
	if !ok {
		return
	}
	form := r.PostForm
	d.Email = form.Get("email")
	mailNotice := func(d *accountData, m string) { d.Mail.Notice = m }
	mailField := func(d *accountData, in *board.InputError) { d.Mail.Email = in.Message }
	if _, _, err := board.CleanEmailInput(d.Email); s.writeRefused(w, r, p, d, err, mailField, mailNotice, action) {
		return
	}
	if !s.verifyCurrent(w, r, p, d, form.Get("current"), func(d *accountData, m string) { d.Mail.Current = m }, mailNotice) {
		return
	}
	err := board.ChangeEmail(r.Context(), s.pool, p.Viewer, d.Email, s.opt.Now())
	if s.writeRefused(w, r, p, d, err, mailField, mailNotice, action) {
		return
	}
	http.Redirect(w, r, s.path("account")+"?done=email", http.StatusSeeOther)
}

// changeSignature serves POST /account/signature. The text comes back in the box
// whatever happens after the csrf check.
func (s *site) changeSignature(w http.ResponseWriter, r *http.Request) {
	const action = "change your signature"
	text := r.PostForm.Get("signature")
	p, err := s.begin(w, r, "Account", "")
	if err != nil {
		slog.Error("account: page", "path", r.URL.Path, "err", err)
		s.bareForm(w, r, nil, failStatus(r, err), msgSignatureFail, text)
		return
	}
	if p.Viewer.UserID == 0 || !p.Viewer.Perms.Has("account.signature", 0) {
		s.deniedTo(w, p, action)
		return
	}
	d, err := s.accountData(r, p)
	if err != nil {
		slog.Error("account: data", "path", r.URL.Path, "err", err)
		s.bareForm(w, r, p, failStatus(r, err), msgSignatureFail, text)
		return
	}
	d.Signature = text
	err = board.SetSignature(r.Context(), s.pool, p.Viewer, text, s.opt.Now())
	var in *board.InputError
	switch {
	case err == nil:
		http.Redirect(w, r, s.path("account")+"?done=signature", http.StatusSeeOther)
	case errors.As(err, &in):
		d.Sig.Text = in.Message
		s.renderAccount(w, http.StatusBadRequest, p, d)
	case errors.Is(err, board.ErrForbidden):
		s.deniedTo(w, p, action)
	default:
		slog.Error("account: signature failed", "err", err)
		s.bareForm(w, r, p, failStatus(r, err), msgSignatureFail, text)
	}
}
