package web

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/ratelimit"
	"github.com/evanwtf/gobb/internal/session"
	"github.com/evanwtf/gobb/internal/tz"
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
	"password":       "Your password is changed.",
	"email":          "Your email address is changed.",
	"signature":      "Your signature is saved.",
	"timezone":       "Your time zone is saved.",
	"dateformat":     "Your date format is saved.",
	"about":          "Your changes are saved.",
	"avatar":         "Your avatar is saved.",
	"avatar-removed": "Your avatar is removed.",
}

// The texts of the account forms.
const (
	msgWrongCurrent   = "That is not your current password."
	msgConfirmDiffers = "The two new passwords differ."
	msgBanned         = "You cannot make this change because you are banned."
	msgInactive       = "This account is not active."
	msgBusy           = "The server is busy. Try again in a few seconds."
	msgSignatureFail  = "Something went wrong, and your signature was not saved. Your text is below. Try again in a moment."
	msgSignatureGone  = "Your account is no longer available. Copy your text before you leave this page."
	msgWriteFailed    = "Something went wrong, and nothing was changed. Try again in a moment."
	msgTooManyTries   = "Too many tries. Wait an hour and try again."
)

// The limit on the changes that need the current password: each member gets
// triesPerHour tries an hour on each of the email and the password form. A try
// is a POST that reaches the check of the current password, so refusals and
// successes both count. The count lives in memory and starts over with the
// server, like the sign-in limits on names.
const (
	triesPerHour = 5
	triesWindow  = time.Hour
	triesMaxKeys = 100_000
)

// newTries returns the limiter of the account changes.
func newTries() *ratelimit.Limiter { return ratelimit.New(triesPerHour, triesWindow, triesMaxKeys) }

// beforeHold, when set, runs in a password change after the current password is
// verified and before the gate is taken for the new hash. A test uses it to fill
// the gate at that point.
var beforeHold func()

// beforeNewSession, when set, runs after a password change commits and before the
// new session of the browser is inserted. A test uses it to write a ban there.
var beforeNewSession func()

// beforeEmailChange, when set, runs in an email change after the current password
// is verified and before the address is written. A test uses it to change the
// password of the member at that point.
var beforeEmailChange func()

// afterBegin, when set, runs in the signature form after the page of the member
// is built and before the stored values are read. A test uses it to remove the
// member at that point.
var afterBegin func()

// passwordErr, emailErr and signatureErr are the messages of the three forms.
// Notice is for the whole form; the others sit beside their field.
type passwordErr struct{ Notice, Current, Next, Confirm string }
type emailErr struct{ Notice, Email, Current string }
type signatureErr struct{ Text string }

// settingsErr holds the messages that sit beside the fields of the settings
// forms. Notice is for the whole page.
type settingsErr struct {
	Notice                                                 string
	Zone, Format, Location, Occupation, Interests, Website string
	Birthday, Avatar                                       string
}

// accountData is the data of account.html.
//
// The embedded AccountPage holds the stored settings. On a failed POST of "About
// you", the values that the member sent replace them. The time zone and the date
// format keep the stored value.
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
	// Err holds the messages of the settings forms.
	Err settingsErr
	// Samples are the three date formats made with the clock of the viewer.
	Samples dateSamples
	// NowText is the time now in the zone of the viewer, in the standard format,
	// and NowCity is the city of that zone.
	NowText, NowCity string
	// LatestYear is the latest year that the birthday accepts.
	LatestYear int
	// Common and Zones are the choices of the time zone list.
	Common []tz.Choice
	Zones  []string
}

// dateSamples are the samples that the date format choices show.
type dateSamples struct {
	Standard string
	Relative []string
	ISO      string
}

// monthChoice is a month of the birthday list.
type monthChoice struct {
	Number int
	Name   string
}

// birthdayDays and birthdayMonths are the day and month lists of the form.
var (
	birthdayDays = func() []int {
		d := make([]int, 31)
		for i := range d {
			d[i] = i + 1
		}
		return d
	}()
	birthdayMonths = func() []monthChoice {
		out := make([]monthChoice, 12)
		for i := range out {
			out[i] = monthChoice{i + 1, time.Month(i + 1).String()}
		}
		return out
	}()
)

// Days and Months are the lists of the birthday form.
func (accountData) Days() []int           { return birthdayDays }
func (accountData) Months() []monthChoice { return birthdayMonths }

// Years returns the years of the birthday list, newest first: from LatestYear
// down to 1900. A stored year above LatestYear (an import) is added on top, so
// the list shows it.
func (d accountData) Years() []int {
	var out []int
	if d.BirthYear > d.LatestYear {
		out = append(out, d.BirthYear)
	}
	for y := d.LatestYear; y >= account.MinBirthYear; y-- {
		out = append(out, y)
	}
	return out
}

// ZoneListed reports whether the stored zone is one that the list offers.
func (d accountData) ZoneListed() bool { return tz.Known(d.Timezone) }

// InCommon reports whether zone is one of the common choices.
func (d accountData) InCommon(zone string) bool {
	for _, c := range d.Common {
		if c.Name == zone {
			return true
		}
	}
	return false
}

// SelectedInAll reports whether the entry zone of the full list is the selected
// one. A zone that is also a common choice is selected there, once, not here.
func (d accountData) SelectedInAll(zone string) bool {
	return zone == d.Timezone && !d.InCommon(zone)
}

// account serves GET /account: a guest goes to sign in and comes back here; a
// member sees the three forms.
func (s *site) account(w http.ResponseWriter, r *http.Request) {
	p, err := s.begin(w, r, "Account", "account")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if p.Viewer.UserID == 0 {
		// The redirect is not a page, so the robots meta element has no place on
		// it; the header says the same.
		w.Header().Set("X-Robots-Tag", "noindex")
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
	now := s.opt.Now()
	loc := p.Viewer.Loc
	if loc == nil {
		loc = time.UTC
	}
	d := accountData{AccountPage: ap, Email: ap.Email, Signature: ap.Signature, Common: tz.Common, Zones: tz.Zones}
	d.LatestYear = now.UTC().Year() - account.MinAge
	d.NowText = now.In(loc).Format(timeLayout)
	d.NowCity = zoneCity(loc.String())
	d.Samples = dateSamples{
		Standard: now.In(loc).Format(timeLayout),
		Relative: []string{
			relText(now.Add(-98*time.Minute), now, loc, timeLayout),
			relText(now.Add(-39*time.Hour), now, loc, timeLayout),
			relText(now.In(loc).AddDate(0, -14, 0), now, loc, timeLayout),
		},
		ISO: now.In(loc).Format(isoTimeLayout),
	}
	return d, nil
}

// zoneCity names the city of a zone: the last element of its name, with spaces
// for underscores ("America/New_York" is "New York"). gobb.js makes the same text.
func zoneCity(zone string) string {
	city := zone[strings.LastIndex(zone, "/")+1:]
	return strings.ReplaceAll(city, "_", " ")
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
	p, err := s.begin(w, r, "Account", "account")
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
//
// On success it returns the password epoch that the check read with the hash it
// verified. The write that follows hands it back, and the write refuses when the
// password changed in between (board.ErrPasswordChanged).
func (s *site) verifyCurrent(w http.ResponseWriter, r *http.Request, p *page, d accountData, route, pw string,
	wrong func(*accountData, string), notice func(*accountData, string)) (epoch int64, ok bool) {
	// The try is counted before the check, so parallel tries cannot pass the limit,
	// and it is never given back: a refusal counts as much as a success.
	now := s.opt.Now()
	if ok, wait := s.tries.Take(route+":"+strconv.FormatInt(p.Viewer.UserID, 10), now); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
		notice(&d, msgTooManyTries)
		s.renderAccount(w, http.StatusTooManyRequests, p, d)
		return 0, false
	}
	// The step after the check only keeps the epoch. It asks the database nothing.
	id, err := s.login.CheckWith(r.Context(), p.Viewer.Name, pw, s.clientIP(r), now,
		func(_ context.Context, _, e int64) error { epoch = e; return nil })
	var banned *login.BannedError
	var limited *login.LimitedError
	switch {
	case err == nil && id == p.Viewer.UserID:
		return epoch, true
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
	return 0, false
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
	case errors.Is(err, board.ErrBanned):
		notice(&d, msgBanned)
		s.renderAccount(w, http.StatusForbidden, p, d)
	case errors.Is(err, board.ErrForbidden):
		s.deniedTo(w, p, action)
	case errors.Is(err, board.ErrNotFound):
		s.notFound(w, r)
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		notice(&d, msgBusy)
		s.renderAccount(w, http.StatusServiceUnavailable, p, d)
	default:
		slog.Error("account write failed", "path", r.URL.Path, "err", err)
		notice(&d, msgWriteFailed)
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
	epoch, ok := s.verifyCurrent(w, r, p, d, "password", current, func(d *accountData, m string) { d.Pass.Current = m }, passNotice)
	if !ok {
		return
	}
	// The gate share covers the hash and nothing else (ChangePasswordWith).
	hold := func(ctx context.Context) (func(), error) {
		if beforeHold != nil {
			beforeHold()
		}
		return s.login.Hold(ctx)
	}
	newEpoch, err := board.ChangePasswordWith(r.Context(), s.pool, p.Viewer, next, epoch, hold, s.opt.Now())
	if errors.Is(err, board.ErrPasswordChanged) {
		// The password changed after the check read it. The answer is the one for a
		// wrong current password, and nothing was changed.
		d.Pass.Current = msgWrongCurrent
		s.renderAccount(w, http.StatusBadRequest, p, d)
		return
	}
	if s.writeRefused(w, r, p, d, err, passField, passNotice, action) {
		return
	}
	s.startNewSession(w, r, p, newEpoch)
}

// startNewSession ends the password change: every session of the member is gone
// (ChangePassword deleted them with the new hash), so one new session starts
// for this browser, with the "remember me" choice of the old one. The csrf token
// follows the session, so the page after the redirect carries a new token.
func (s *site) startNewSession(w http.ResponseWriter, r *http.Request, p *page, epoch int64) {
	id, err := s.identify(w, r)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	now := s.opt.Now()
	if beforeNewSession != nil {
		beforeNewSession()
	}
	// The insert is conditional like a sign-in: a ban or another change that
	// landed since the commit leaves the member signed out.
	token, sess, err := session.CreateChecked(r.Context(), s.pool, p.Viewer.UserID, epoch, id.sess.Remember, s.clientIP(r), r.UserAgent(), now)
	if err != nil {
		// The password is changed and the old sessions are gone. Send the member
		// to sign in with the new one.
		if !errors.Is(err, session.ErrStale) {
			slog.Error("account: cannot start a session after a password change", "user", p.Viewer.UserID, "err", err)
		}
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
	stored := d.Email
	d.Email = form.Get("email")
	mailNotice := func(d *accountData, m string) { d.Mail.Notice = m }
	mailField := func(d *accountData, in *board.InputError) { d.Mail.Email = in.Message }
	if _, _, err := board.CleanEmailInput(d.Email); s.writeRefused(w, r, p, d, err, mailField, mailNotice, action) {
		return
	}
	epoch, ok := s.verifyCurrent(w, r, p, d, "email", form.Get("current"), func(d *accountData, m string) { d.Mail.Current = m }, mailNotice)
	if !ok {
		return
	}
	if beforeEmailChange != nil {
		beforeEmailChange()
	}
	err := board.ChangeEmailWith(r.Context(), s.pool, p.Viewer, d.Email, epoch, s.opt.Now())
	if errors.Is(err, board.ErrPasswordChanged) {
		d.Mail.Current = msgWrongCurrent
		s.renderAccount(w, http.StatusBadRequest, p, d)
		return
	}
	if s.writeRefused(w, r, p, d, err, mailField, mailNotice, action) {
		return
	}
	// The own address, unchanged, is a no-op: nothing changed, so no notice says so.
	if account.CleanEmail(d.Email) == account.CleanEmail(stored) {
		http.Redirect(w, r, s.path("account"), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, s.path("account")+"?done=email", http.StatusSeeOther)
}

// changeSignature serves POST /account/signature. The text comes back in the box
// whatever happens after the csrf check.
func (s *site) changeSignature(w http.ResponseWriter, r *http.Request) {
	const action = "change your signature"
	if saveRequested(r) {
		s.saveSignature(w, r)
		return
	}
	text := r.PostForm.Get("signature")
	p, err := s.begin(w, r, "Account", "account")
	if err != nil {
		slog.Error("account: page", "path", r.URL.Path, "err", err)
		s.bareForm(w, r, nil, failStatus(r, err), msgSignatureFail, text)
		return
	}
	if p.Viewer.UserID == 0 || !p.Viewer.Perms.Has("account.signature", 0) {
		s.deniedTo(w, p, action)
		return
	}
	if afterBegin != nil {
		afterBegin()
	}
	d, err := s.accountData(r, p)
	if errors.Is(err, board.ErrNotFound) {
		s.bareForm(w, r, p, http.StatusNotFound, msgSignatureGone, text)
		return
	}
	if err != nil {
		slog.Error("account: data", "path", r.URL.Path, "err", err)
		s.bareForm(w, r, p, failStatus(r, err), msgSignatureFail, text)
		return
	}
	d.Signature = text
	// The signature counts as a save, whether it comes from gobb.js or from a form.
	if ok, wait := s.saves.saves.Take(strconv.FormatInt(p.Viewer.UserID, 10), s.opt.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
		d.Err.Notice = msgTooManySaves
		s.renderAccount(w, http.StatusTooManyRequests, p, d)
		return
	}
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
	case errors.Is(err, board.ErrNotFound):
		// The member is gone or no longer active: hand the text back, as the
		// posting forms do for a target that is gone.
		s.bareForm(w, r, p, http.StatusNotFound, msgSignatureGone, text)
	default:
		slog.Error("account: signature failed", "err", err)
		s.bareForm(w, r, p, failStatus(r, err), msgSignatureFail, text)
	}
}
