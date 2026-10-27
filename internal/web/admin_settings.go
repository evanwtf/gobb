package web

import (
	"context"
	"errors"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/avatar"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/mail"
	"github.com/evanwtf/gobb/internal/tz"
	"github.com/evanwtf/gobb/internal/version"
)

// The settings page of the admin area, /admin/settings: the board name and
// description, the time zone, the default color scheme, the header image, and a
// test of the email. The sign-up mode and the email notifications show read only.
// Each form is a POST that goes through the admin area, so it needs an admin, a
// csrf token and a place under the admin write limit, and each change writes a
// mod_log row through board.AdminWrite. A save that fails the checks gives the
// page again with 400 and the admin's values; any other answer is 303 to the page
// with a notice.

// maxSettingsBody is the cap of the body of the settings form. A description of
// 500 characters of 3 bytes each is 4,500 bytes once it is percent-encoded.
const maxSettingsBody = 16 << 10

// The texts of the settings page.
const (
	msgHeaderFormat = "Pick a PNG, JPEG or WebP."
	msgMailWait     = "Wait a minute before you send another test email."
	msgMailAddress  = "Your account has no email address that gobb can send to. Set one on your account page."
	msgNoHeader     = "The board has no header image."
)

// sendTestTimeout is how long the test email may take. The request deadline
// is shorter, so the send runs on its own deadline.
const sendTestTimeout = 25 * time.Second

// adminSettingsRoutes adds the routes of the settings page to a. The test email
// route exists only when email is on and the site has a sender for it.
func (s *site) adminSettingsRoutes(a adminRoutes) {
	a.page("/settings", s.adminSettings)
	a.write("/settings", maxSettingsBody, s.adminSettingsSave)
	a.upload("/settings/header", maxAvatarBody, s.adminHeaderUpload)
	a.write("/settings/header/remove", maxPostBody, s.adminHeaderRemove)
	if s.mailTestOn() {
		a.write("/settings/mail-test", maxPostBody, s.adminMailTest)
	}
}

// mailTestOn reports whether the site can send a test email: email is on, and
// the site has a sender.
func (s *site) mailTestOn() bool { return s.opt.Mail != nil && s.opt.Mail.Sender != nil }

// settingsErrors are the messages of the settings page: one for each field, and
// the text beside the header image and the email test.
type settingsErrors struct {
	Name, Description, Zone, Scheme, Header, Mail string
}

// adminSettingsData is the data of admin-settings.html.
type adminSettingsData struct {
	Crumbs []crumb
	// Name, Description, Timezone and Scheme are the values of the form: the
	// stored ones, or the ones that the admin sent.
	Name, Description, Timezone, Scheme string
	// Notice is the text of the answer to the last change. NoticeFailed marks one
	// that is an error.
	Notice       string
	NoticeFailed bool
	Err          settingsErrors
	// Common and Zones are the choices of the time zone list.
	Common []tz.Choice
	Zones  []string
	// Schemes are the choices of the color scheme, with the selected one marked.
	Schemes []schemeChoice
	// MaxName and MaxDescription are the longest values, in characters.
	MaxName, MaxDescription int
	// HasHeader is true when the board has a header image. The page shows it from
	// the layout's own HeaderSrc.
	HasHeader bool
	// Registration and EmailNotifications are the read only settings, as text.
	Registration, EmailNotifications string
	// MailOn is true when the page offers the test email.
	MailOn bool
}

// ZoneListed reports whether the time zone of the form is one that the list offers.
func (d adminSettingsData) ZoneListed() bool { return tz.Known(d.Timezone) }

// InCommon reports whether zone is one of the common choices.
func (d adminSettingsData) InCommon(zone string) bool {
	for _, c := range d.Common {
		if c.Name == zone {
			return true
		}
	}
	return false
}

// SelectedInAll reports whether the entry zone of the full list is the selected
// one. A zone that is also a common choice is selected there, once, not here.
func (d adminSettingsData) SelectedInAll(zone string) bool {
	return zone == d.Timezone && !d.InCommon(zone)
}

// registrationText words the stored sign-up mode. A value that gobb does not know
// shows as it is stored.
func registrationText(mode string) string {
	switch mode {
	case "open":
		return "Open: anyone may sign up."
	case "approval":
		return "By approval: an admin approves each new member."
	case "closed":
		return "Closed: nobody may sign up."
	}
	return mode
}

// settingsData returns the data of the page for the stored settings of p.
func (s *site) settingsData(p *page) adminSettingsData {
	set := p.Settings
	d := adminSettingsData{
		Crumbs: s.adminCrumbs(),
		Name:   set.BoardName, Description: set.BoardDescription, Timezone: set.Timezone, Scheme: set.DefaultScheme,
		Common: tz.Common, Zones: tz.Zones,
		MaxName: board.MaxBoardName, MaxDescription: board.MaxBoardDescription,
		HasHeader:    set.HeaderImage != "",
		Registration: registrationText(set.Registration),
		MailOn:       s.mailTestOn(),
	}
	if set.EmailNotifications {
		d.EmailNotifications = "On"
	} else {
		d.EmailNotifications = "Off"
	}
	d.Schemes = schemeChoices(d.Scheme)
	return d
}

// schemeChoices returns the color schemes, with current marked.
func schemeChoices(current string) []schemeChoice {
	out := make([]schemeChoice, 0, len(board.Schemes))
	for _, v := range board.Schemes {
		out = append(out, schemeChoice{Value: v, Label: schemeLabels[v], Current: v == current})
	}
	return out
}

// echoSettings puts the values that the admin sent into d. A zone that the list
// does not offer, and a scheme that gobb does not have, stay out: the select and
// the buttons can only show a real choice.
func echoSettings(d *adminSettingsData, form url.Values) {
	d.Name, d.Description = form.Get("board_name"), form.Get("board_description")
	if zone := form.Get("timezone"); tz.Known(zone) {
		d.Timezone = zone
	}
	if scheme := form.Get("scheme"); board.ValidScheme(scheme) {
		d.Scheme = scheme
		d.Schemes = schemeChoices(scheme)
	}
}

// settingsNotices are the texts that the query ?done= selects after a change.
// The value is never printed, so the page shows no text of the request.
var settingsNotices = map[string]struct {
	text   string
	failed bool
}{
	"saved":          {text: "Settings saved."},
	"nochange":       {text: "Nothing changed: the form held the settings that the board has."},
	"header":         {text: "Header image saved."},
	"header-removed": {text: "Header image removed."},
	"header-none":    {text: msgNoHeader},
	"mail-sent":      {text: "Test email sent to the address of your account."},
}

// mailReasonRE matches the summaries of a failed send that mail.Summary makes,
// such as "smtp 550 5.1.1" and "timeout". The query ?reason= is shown only when
// it matches.
var mailReasonRE = regexp.MustCompile(`^(smtp [45][0-9]{2}( [245]\.[0-9]{1,3}\.[0-9]{1,3})?|timeout|tls|no starttls|network)$`)

// noticeFor returns the notice that the query q selects, and whether it is an
// error.
func noticeFor(q url.Values) (string, bool) {
	done := q.Get("done")
	if done == "mail-failed" {
		text := "The test email was not sent"
		if reason := q.Get("reason"); mailReasonRE.MatchString(reason) {
			text += " (" + reason + ")"
		}
		return text + ". Check the email settings of the server.", true
	}
	n, ok := settingsNotices[done]
	return n.text, ok && n.failed
}

// renderSettings writes the settings page with d.
func (s *site) renderSettings(w http.ResponseWriter, status int, p *page, d adminSettingsData) {
	p.Data = d
	s.render(w, status, "admin-settings.html", p)
}

// adminSettings serves GET /admin/settings.
func (s *site) adminSettings(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Settings")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d := s.settingsData(p)
	d.Notice, d.NoticeFailed = noticeFor(r.URL.Query())
	s.renderSettings(w, http.StatusOK, p, d)
}

// settingsHref returns the link to the settings page with the notice done, and
// the extra query values in more.
func (s *site) settingsHref(done string, more ...string) string {
	v := url.Values{"done": {done}}
	for i := 0; i+1 < len(more); i += 2 {
		v.Set(more[i], more[i+1])
	}
	return s.path("admin", "settings") + "?" + v.Encode()
}

// inputErrors returns the [*board.InputError]s that err holds: err itself, or the
// parts of an error made by errors.Join. It returns nil when err is neither.
func inputErrors(err error) []*board.InputError {
	if in, ok := err.(*board.InputError); ok {
		return []*board.InputError{in}
	}
	var out []*board.InputError
	if j, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range j.Unwrap() {
			out = append(out, inputErrors(e)...)
		}
	}
	return out
}

// adminSettingsSave serves POST /admin/settings: the four fields of the main
// form. A refusal of the checks gives the page again with 400, the message beside
// each field that failed, and the values that the admin sent; nothing changes. A
// save that changes nothing, and a save that changes something, answer 303.
//
// After the save, the static 500 and 503 pages and the frame of the error pages
// follow the new settings. The write does not follow a cancelled request: the
// settings and the pages that hold them must not disagree.
func (s *site) adminSettingsSave(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Settings")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	form := r.PostForm
	c := board.SettingsChange{Name: form.Get("board_name"), Description: form.Get("board_description"),
		Timezone: form.Get("timezone"), Scheme: form.Get("scheme")}
	ctx := context.WithoutCancel(r.Context())
	_, err = board.UpdateSettings(ctx, s.pool, p.Viewer, c, s.opt.Now())
	switch {
	case err == nil:
	case errors.Is(err, board.ErrNoChange):
		http.Redirect(w, r, s.settingsHref("nochange"), http.StatusSeeOther)
		return
	case errors.Is(err, board.ErrForbidden):
		s.fail(w, r, http.StatusForbidden, "Not allowed", "You do not have the right to change the board settings.")
		return
	case len(inputErrors(err)) > 0:
		d := s.settingsData(p)
		echoSettings(&d, form)
		for _, in := range inputErrors(err) {
			switch in.Field {
			case "name":
				d.Err.Name = in.Message
			case "description":
				d.Err.Description = in.Message
			case "timezone":
				d.Err.Zone = in.Message
			case "scheme":
				d.Err.Scheme = in.Message
			}
		}
		s.renderSettings(w, http.StatusBadRequest, p, d)
		return
	default:
		s.serverError(w, r, err)
		return
	}
	s.followSettings(ctx)
	http.Redirect(w, r, s.settingsHref("saved"), http.StatusSeeOther)
}

// followSettings makes the error pages follow the settings that were just saved.
// The save has committed, so a failure here is logged and goes no further: the
// pages catch up at the next request that reads the settings, except the static
// pages, which the next save builds again.
func (s *site) followSettings(ctx context.Context) {
	if err := s.refreshFrame(ctx); err != nil {
		slog.Error("admin: cannot refresh the error pages after a save of the settings", "err", err)
	}
}

// beforeSetHeader, when set, runs in a header upload after the new file is
// written and before the database write. A test uses it to break the write.
var beforeSetHeader func()

// adminHeaderUpload serves POST /admin/settings/header. The guard has checked the
// token and taken the slots. The handler owns the files:
//
//  1. avatar.CheckHeader checks the upload (PNG, JPEG or WebP, at most 1 MB, at
//     most 8,000 pixels a side) and decodes it under the memory gate.
//  2. The file, as it was sent, goes to header/h-{16 random hex}.{png,jpg,webp}
//     under the data directory, through an os.Root, created with O_EXCL.
//  3. board.SetHeaderImage stores the path and writes the log row. When it fails,
//     the new file is deleted.
//  4. When it succeeds, the file it replaced is deleted, if its path is one that
//     gobb may delete.
func (s *site) adminHeaderUpload(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Settings")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	refuse := func(status int, msg string) {
		d := s.settingsData(p)
		d.Err.Header = msg
		s.renderSettings(w, status, p, d)
	}
	file, _, err := r.FormFile("header")
	if err != nil {
		refuse(http.StatusBadRequest, msgHeaderFormat)
		return
	}
	defer func() { _ = file.Close() }()
	img, err := avatar.CheckHeader(r.Context(), file, s.gate, s.login.GateWait)
	switch {
	case err == nil:
	case errors.Is(err, avatar.ErrTooLarge):
		refuse(http.StatusRequestEntityTooLarge, msgFileTooLarge)
		return
	case errors.Is(err, avatar.ErrFormat):
		refuse(http.StatusBadRequest, msgHeaderFormat)
		return
	case errors.Is(err, avatar.ErrDimensions):
		refuse(http.StatusBadRequest, msgAvatarPixels)
		return
	case errors.Is(err, avatar.ErrTooMuchMemory):
		refuse(http.StatusBadRequest, msgAvatarMemory)
		return
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		refuse(http.StatusServiceUnavailable, msgBusy)
		return
	default:
		slog.Error("admin: header image", "user", p.Viewer.UserID, "err", err)
		refuse(failStatus(r, err), msgWriteFailed)
		return
	}
	name, err := s.writeDataFile("header", "h-", img.Ext, img.Data)
	if err != nil {
		slog.Error("admin: cannot write a header image file", "user", p.Viewer.UserID, "err", err)
		refuse(http.StatusInternalServerError, msgWriteFailed)
		return
	}
	if beforeSetHeader != nil {
		beforeSetHeader()
	}
	// The write does not follow a cancelled request (see adminSettingsSave).
	ctx := context.WithoutCancel(r.Context())
	old, err := board.SetHeaderImage(ctx, s.pool, p.Viewer, name, s.opt.Now())
	if err != nil {
		s.deleteHeaderFile(name)
		if errors.Is(err, board.ErrForbidden) {
			s.fail(w, r, http.StatusForbidden, "Not allowed", "You do not have the right to change the board settings.")
			return
		}
		s.serverError(w, r, err)
		return
	}
	s.deleteHeaderFile(old)
	s.followSettings(ctx)
	http.Redirect(w, r, s.settingsHref("header"), http.StatusSeeOther)
}

// adminHeaderRemove serves POST /admin/settings/header/remove. A board with no
// header image answers 303 with a note and changes nothing.
func (s *site) adminHeaderRemove(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Settings")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	ctx := context.WithoutCancel(r.Context())
	old, err := board.RemoveHeaderImage(ctx, s.pool, p.Viewer, s.opt.Now())
	switch {
	case err == nil:
	case errors.Is(err, board.ErrNoChange):
		http.Redirect(w, r, s.settingsHref("header-none"), http.StatusSeeOther)
		return
	case errors.Is(err, board.ErrForbidden):
		s.fail(w, r, http.StatusForbidden, "Not allowed", "You do not have the right to change the board settings.")
		return
	default:
		s.serverError(w, r, err)
		return
	}
	s.deleteHeaderFile(old)
	s.followSettings(ctx)
	http.Redirect(w, r, s.settingsHref("header-removed"), http.StatusSeeOther)
}

// deleteHeaderFile deletes the file at path, a site path under the data
// directory, after a change of the header image. It deletes only a path that
// [board.ValidHeaderPath] accepts; any other value is left alone. An empty path
// is no file. A file that is already gone is fine. A failure is logged and goes
// no further: the change has committed.
func (s *site) deleteHeaderFile(path string) {
	if path == "" {
		return
	}
	if !board.ValidHeaderPath(path) {
		slog.Warn("admin: the stored header image path is not one that gobb deletes; the file is left alone")
		return
	}
	root, err := os.OpenRoot(s.opt.DataDir)
	if err != nil {
		slog.Warn("admin: cannot open the data directory to delete a header image file", "err", err)
		return
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(filepath.FromSlash(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("admin: cannot delete a header image file", "err", err)
	}
}

// adminMailTest serves POST /admin/settings/mail-test: it sends one test email to
// the address of the admin's own account, through the SMTP settings and not
// through the queue, as `gobb mail test` does, so the admin sees at once whether
// email works. Each admin may send one a minute; a send counts whether it works
// or not. The send runs on its own deadline, so the answer is a 303 to the page
// with a notice for a send that worked and for one that failed. The notice for a
// failure holds the summary that [mail.Summary] makes and nothing from the
// server's reply. The test writes no mod_log row: it changes no data.
func (s *site) adminMailTest(w http.ResponseWriter, r *http.Request) {
	p, err := s.adminBegin(w, r, "Settings")
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	refuse := func(status int, msg string) {
		d := s.settingsData(p)
		d.Err.Mail = msg
		s.renderSettings(w, status, p, d)
	}
	if ok, wait := s.mailTests.Take(strconv.FormatInt(p.Viewer.UserID, 10), s.opt.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
		refuse(http.StatusTooManyRequests, msgMailWait)
		return
	}
	acct, err := board.Account(r.Context(), s.pool, p.Viewer)
	if err != nil {
		s.pageFailed(w, r, err)
		return
	}
	m := mail.Message{
		To:      acct.Email,
		Subject: "Test email from " + foldControls(p.Settings.BoardName),
		Body:    "This is a test email from gobb " + version.Version + ". Email works.\n",
	}
	if mail.Check(m) != nil {
		refuse(http.StatusBadRequest, msgMailAddress)
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), sendTestTimeout)
	defer cancel()
	if err := s.opt.Mail.Sender.Send(ctx, m); err != nil {
		// The summary and the stage only: nothing from the error text, which could
		// hold what the server sent.
		attrs := []any{"user", p.Viewer.UserID, "reason", mail.Summary(err)}
		var me *mail.Error
		if errors.As(err, &me) {
			attrs = append(attrs, "stage", me.Stage)
		}
		slog.Warn("admin: test email failed", attrs...)
		http.Redirect(w, r, s.settingsHref("mail-failed", "reason", mail.Summary(err)), http.StatusSeeOther)
		return
	}
	slog.Info("admin: test email sent", "user", p.Viewer.UserID)
	http.Redirect(w, r, s.settingsHref("mail-sent"), http.StatusSeeOther)
}
