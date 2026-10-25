package web

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/evanwtf/gobb/internal/avatar"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/ratelimit"
)

// The settings of the account page: the time zone, the date format, "About you"
// and the avatar. Each one is a POST through site.protect (the upload through
// site.protectUpload). A browser without script posts the form and gets a 303 to
// the account page, or the page again with 400 and the message beside the field.
// gobb.js sends the same POST with the header X-Gobb-Save: 1 and gets 204, or
// JSON (see [saveAnswer]).

// The limits on the settings. A save is a POST to any of the settings routes
// (the signature included); an upload is a POST to /account/avatar. The counts
// are per member, live in memory and start over with the server, like the other
// limits of the site.
const (
	saveWindow       = 10 * time.Minute
	savesPerWindow   = 120
	uploadWindow     = time.Hour
	uploadsPerWindow = 10
	saveLimitMaxKeys = 10_000
	// maxUploadsInFlight is how many avatar uploads the server takes at one time.
	// The body of each is held in memory (up to about 1 MiB) while it is read.
	maxUploadsInFlight = 4
)

// The texts of the settings answers.
const (
	msgTooManySaves   = "Too many changes. Wait a few minutes and try again."
	msgTooManyUploads = "Too many uploads. Wait an hour and try again."
	msgAvatarFormat   = "Pick a PNG, JPEG or GIF."
	msgAvatarPixels   = "That image is too large. Use one of at most 8,000 pixels a side."
	msgBirthdayForm   = "Pick a day, a month and a year from the lists."
	msgNoRight        = "You cannot make this change."
	msgNotSaved       = "Not saved. Reload the page and try again."
)

// maxAvatarBody is the cap of the body of an upload: the largest file and 64 KiB
// for the other parts, so that a multipart body stays in memory.
const maxAvatarBody = avatar.MaxBytes + 64<<10

// saveLimits holds the limiters of the settings.
type saveLimits struct {
	saves, uploads *ratelimit.Limiter
}

func newSaveLimits(opt Options) saveLimits {
	saves := savesPerWindow
	if opt.MemberSaves > 0 {
		saves = opt.MemberSaves
	}
	return saveLimits{
		saves:   ratelimit.New(saves, saveWindow, saveLimitMaxKeys),
		uploads: ratelimit.New(uploadsPerWindow, uploadWindow, saveLimitMaxKeys),
	}
}

// saveAnswer answers a request from gobb.js (the header X-Gobb-Save: 1) for the
// result err of a settings write, and reports whether it did. A request without
// the header gets nothing here: the caller answers it with a page.
//
//   - err nil: 204.
//   - A refusal of the input: 400 with {"field": ..., "message": ...}. The field
//     is the argument field when set, else the field of the error.
//   - A missing right or a ban: 403. A member who is gone: 404. A full gate: 503.
//   - Any other failure: 500 with a fixed text; the error goes to the log.
//
// Each failure carries {"message": ...}, and the answer is application/json.
func saveAnswer(w http.ResponseWriter, r *http.Request, err error, field string) bool {
	if !saveRequested(r) {
		return false
	}
	var in *board.InputError
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &in):
		if field == "" {
			field = in.Field
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"field": field, "message": in.Message})
	case errors.Is(err, board.ErrBanned):
		saveMessage(w, http.StatusForbidden, msgBanned)
	case errors.Is(err, board.ErrForbidden):
		saveMessage(w, http.StatusForbidden, msgNoRight)
	case errors.Is(err, board.ErrNotFound):
		saveMessage(w, http.StatusNotFound, msgNotSaved)
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		saveMessage(w, http.StatusServiceUnavailable, msgBusy)
	default:
		slog.Error("account setting failed", "path", r.URL.Path, "err", err)
		saveMessage(w, failStatus(r, err), msgWriteFailed)
	}
	return true
}

// deniedText is the text of the 403 for a viewer who may not do action.
func deniedText(p *page, action string) string {
	if p.Viewer.UserID == 0 {
		return "Sign in to " + action + "."
	}
	return "You do not have the right to " + action + "."
}

// tooMany answers a request that is over its limit: 429 with Retry-After, as JSON
// for gobb.js and as the account page with a notice for any other. echo, when
// set, puts the values that the member sent into the page, as the other refusals
// of a form do.
func (s *site) tooMany(w http.ResponseWriter, r *http.Request, wait time.Duration, msg string, echo func(*accountData)) {
	w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
	if saveRequested(r) {
		saveMessage(w, http.StatusTooManyRequests, msg)
		return
	}
	p, err := s.begin(w, r, "Account", "account")
	if err == nil {
		if afterBegin != nil {
			afterBegin()
		}
		var d accountData
		if d, err = s.accountData(r, p); err == nil {
			if echo != nil {
				echo(&d)
			}
			d.Err.Notice = msg
			s.renderAccount(w, http.StatusTooManyRequests, p, d)
			return
		}
	}
	s.pageFailed(w, r, err)
}

// pageFailed answers the failure of the data of the account page, for a request
// that wants a page. A member who is gone (inactive since the request began) gets
// 404 and the neutral text, as a save from gobb.js does; any other failure is a
// server error.
func (s *site) pageFailed(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, board.ErrNotFound) {
		s.fail(w, r, http.StatusNotFound, "Not saved", msgNotSaved)
		return
	}
	s.serverError(w, r, err)
}

// takeSave counts a try of a member against lim, before any read of the
// database, and answers 429 when the member is over it. It reports whether the
// request may go on. The try is never given back: a refusal costs as much as a
// success. A visitor with no session is not counted here; the checks of the
// handler refuse the visitor.
func (s *site) takeSave(w http.ResponseWriter, r *http.Request, lim *ratelimit.Limiter, msg string, echo func(*accountData)) bool {
	id, err := s.identify(w, r)
	if err != nil {
		s.identifyFailed(w, r, err)
		return false
	}
	if id.token == "" {
		return true
	}
	if ok, wait := lim.Take(strconv.FormatInt(id.sess.UserID, 10), s.opt.Now()); !ok {
		s.tooMany(w, r, wait, msg, echo)
		return false
	}
	return true
}

// settingsStart starts a POST handler of the settings. The limit lim comes first,
// then the rights: it answers and reports false for a member over the limit, for
// a guest, for a member who lacks option ("" asks for none), and for a page that
// cannot be built. Otherwise it returns the page. A request from gobb.js gets
// JSON; any other gets the page, and echo puts the values that the member sent
// into the 429 page. The data of the account page is read only when
// the answer is a page ([site.settingResult]).
func (s *site) settingsStart(w http.ResponseWriter, r *http.Request, option, action string, lim *ratelimit.Limiter, msg string, echo func(*accountData)) (*page, bool) {
	if !s.takeSave(w, r, lim, msg, echo) {
		return nil, false
	}
	p, err := s.begin(w, r, "Account", "account")
	if err != nil {
		if saveRequested(r) {
			slog.Error("account: page", "path", r.URL.Path, "err", err)
			saveMessage(w, failStatus(r, err), msgWriteFailed)
		} else {
			s.serverError(w, r, err)
		}
		return nil, false
	}
	if afterBegin != nil {
		afterBegin()
	}
	if p.Viewer.UserID == 0 || (option != "" && !p.Viewer.Perms.Has(option, 0)) {
		if saveRequested(r) {
			saveMessage(w, http.StatusForbidden, deniedText(p, action))
		} else {
			s.deniedTo(w, p, action)
		}
		return nil, false
	}
	return p, true
}

// settingResult ends a settings POST for the results errs of its writes. For a
// request from gobb.js it answers 204 or JSON for the first error. For any other
// it redirects to the account page with the notice done, or answers the refusals
// with the page: every input error puts its message beside its field (field), and
// echo puts the values that the member sent back into the form.
func (s *site) settingResult(w http.ResponseWriter, r *http.Request, p *page, errs []error, jsonField, done, action string,
	echo func(*accountData), field func(*accountData, *board.InputError)) {
	var first error
	for _, err := range errs {
		if err != nil {
			first = err
			break
		}
	}
	if saveAnswer(w, r, first, jsonField) {
		return
	}
	if first == nil {
		http.Redirect(w, r, s.path("account")+"?done="+done, http.StatusSeeOther)
		return
	}
	d, err := s.accountData(r, p)
	if errors.Is(err, board.ErrNotFound) {
		s.notFound(w, r)
		return
	}
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	if echo != nil {
		echo(&d)
	}
	notice := func(d *accountData, m string) { d.Err.Notice = m }
	// Every input message goes beside its field first. A failure of another write
	// then answers with the same page, so the member sees both.
	var failed error
	for _, err := range errs {
		var in *board.InputError
		switch {
		case err == nil:
		case errors.As(err, &in):
			field(&d, in)
		case failed == nil:
			failed = err
		}
	}
	if failed != nil {
		s.writeRefused(w, r, p, d, failed, field, notice, action)
		return
	}
	s.renderAccount(w, http.StatusBadRequest, p, d)
}

// setTimezone serves POST /account/timezone.
func (s *site) setTimezone(w http.ResponseWriter, r *http.Request) {
	const action = "change your time zone"
	p, ok := s.settingsStart(w, r, "", action, s.saves.saves, msgTooManySaves, nil)
	if !ok {
		return
	}
	err := board.SetTimezone(r.Context(), s.pool, p.Viewer, r.PostForm.Get("timezone"))
	s.settingResult(w, r, p, []error{err}, "timezone", "timezone", action, nil,
		func(d *accountData, in *board.InputError) { d.Err.Zone = in.Message })
}

// setDateFormat serves POST /account/dateformat.
func (s *site) setDateFormat(w http.ResponseWriter, r *http.Request) {
	const action = "change your date format"
	p, ok := s.settingsStart(w, r, "", action, s.saves.saves, msgTooManySaves, nil)
	if !ok {
		return
	}
	err := board.SetDateFormat(r.Context(), s.pool, p.Viewer, r.PostForm.Get("dateformat"))
	s.settingResult(w, r, p, []error{err}, "dateformat", "dateformat", action, nil,
		func(d *accountData, in *board.InputError) { d.Err.Format = in.Message })
}

// aboutFields are the four text boxes of "About you".
var aboutFields = []string{"location", "occupation", "interests", "website"}

// setAbout serves POST /account/about. It changes only the fields that the form
// holds: a text box that is absent stays as it is. The birthday changes only when
// bday, bmonth and byear are all present. The text boxes and the birthday are
// two writes, and each runs whatever the other one answers, so a refusal of one
// never drops what the member sent for the other. Each refusal shows beside its
// own field, and the page shows every value that was sent.
func (s *site) setAbout(w http.ResponseWriter, r *http.Request) {
	const action = "change your profile"
	form := r.PostForm
	// The birthday is read first, so that every refusal, the 429 page included,
	// shows the values that the member sent.
	var b board.Birthday
	var birthdayErr error
	hasBirthday := form.Has("bday") && form.Has("bmonth") && form.Has("byear")
	if hasBirthday {
		b, birthdayErr = parseBirthday(form.Get("bday"), form.Get("bmonth"), form.Get("byear"))
	}
	echo := func(d *accountData) {
		for name, dst := range map[string]*string{"location": &d.Location, "occupation": &d.Occupation, "interests": &d.Interests, "website": &d.Website} {
			if form.Has(name) {
				*dst = form.Get(name)
			}
		}
		if hasBirthday {
			d.BirthDay, d.BirthMonth, d.BirthYear = b.Day, b.Month, b.Year
		}
	}
	p, ok := s.settingsStart(w, r, "account.profile", action, s.saves.saves, msgTooManySaves, echo)
	if !ok {
		return
	}
	var c board.AboutChange
	texts := map[string]**string{"location": &c.Location, "occupation": &c.Occupation, "interests": &c.Interests, "website": &c.Website}
	hasText := false
	for _, name := range aboutFields {
		if form.Has(name) {
			v := form.Get(name)
			*texts[name] = &v
			hasText = true
		}
	}
	var textErr error
	if hasText {
		textErr = board.SetAbout(r.Context(), s.pool, p.Viewer, c)
	}
	if hasBirthday && birthdayErr == nil {
		birthdayErr = board.SetBirthday(r.Context(), s.pool, p.Viewer, b, s.opt.Now().UTC())
	}
	s.settingResult(w, r, p, []error{textErr, birthdayErr}, "", "about", action, echo, func(d *accountData, in *board.InputError) {
		switch in.Field {
		case "location":
			d.Err.Location = in.Message
		case "occupation":
			d.Err.Occupation = in.Message
		case "interests":
			d.Err.Interests = in.Message
		case "website":
			d.Err.Website = in.Message
		default:
			d.Err.Birthday = in.Message
		}
	})
}

// parseBirthday reads the three lists of the birthday form. An empty value is 0.
// A value that is not a number is an input error on the birthday: the lists send
// numbers only. The numbers that were read are returned even then.
func parseBirthday(day, month, year string) (board.Birthday, error) {
	var out board.Birthday
	var bad bool
	for _, f := range []struct {
		in  string
		out *int
	}{{day, &out.Day}, {month, &out.Month}, {year, &out.Year}} {
		if f.in == "" {
			continue
		}
		n, err := strconv.Atoi(f.in)
		if err != nil || n < 0 {
			bad = true
			continue
		}
		*f.out = n
	}
	if bad {
		return out, &board.InputError{Field: "birthday", Message: msgBirthdayForm}
	}
	return out, nil
}

// beforeSetAvatar, when set, runs in an avatar upload after the new file is
// written and before the database write. A test uses it to break the write, or to
// cancel the request at that point.
var beforeSetAvatar func()

// removeAvatar serves POST /account/avatar/remove.
func (s *site) removeAvatar(w http.ResponseWriter, r *http.Request) {
	const action = "change your avatar"
	p, ok := s.settingsStart(w, r, "account.avatar", action, s.saves.saves, msgTooManySaves, nil)
	if !ok {
		return
	}
	// The write does not follow a cancelled request: a row and a file that
	// disagree are worse than a write that finishes after the client left.
	old, err := board.RemoveAvatar(context.WithoutCancel(r.Context()), s.pool, p.Viewer)
	if err == nil {
		s.deleteAvatarFile(p.Viewer.UserID, old)
	}
	s.settingResult(w, r, p, []error{err}, "avatar", "avatar-removed", action, nil,
		func(d *accountData, in *board.InputError) { d.Err.Avatar = in.Message })
}

// uploadAvatar serves POST /account/avatar. The guard has checked that a member
// is signed in and has a free slot. The handler owns the files:
//
//  1. avatar.Make checks the upload and makes a new 128 x 128 PNG.
//  2. The PNG goes to avatars/u{id}-{16 random hex}.png under the data directory,
//     through an os.Root, created with O_EXCL.
//  3. board.SetAvatar stores the path. When it fails, the new file is deleted.
//  4. When it succeeds, the file it replaced is deleted, if its path is one that
//     gobb may delete and no other member names it (board.SetAvatar returns "" then).
//
// A request from gobb.js gets 200 and {"src": the site path of the new file}.
func (s *site) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	const action = "change your avatar"
	p, ok := s.settingsStart(w, r, "account.avatar", action, s.saves.uploads, msgTooManyUploads, nil)
	if !ok {
		return
	}
	// refuse answers a problem with the upload: JSON with the field, or the page
	// with the message beside the file box.
	refuse := func(status int, msg string) {
		if saveRequested(r) {
			if status == http.StatusBadRequest {
				writeJSON(w, status, map[string]string{"field": "avatar", "message": msg})
			} else {
				saveMessage(w, status, msg)
			}
			return
		}
		d, err := s.accountData(r, p)
		if err != nil {
			s.pageFailed(w, r, err)
			return
		}
		d.Err.Avatar = msg
		s.renderAccount(w, status, p, d)
	}
	file, _, err := r.FormFile("avatar")
	if err != nil {
		refuse(http.StatusBadRequest, msgAvatarFormat)
		return
	}
	defer func() { _ = file.Close() }()
	png, err := avatar.Make(r.Context(), file, s.gate, s.login.GateWait)
	switch {
	case err == nil:
	case errors.Is(err, avatar.ErrTooLarge):
		refuse(http.StatusRequestEntityTooLarge, msgFileTooLarge)
		return
	case errors.Is(err, avatar.ErrFormat):
		refuse(http.StatusBadRequest, msgAvatarFormat)
		return
	case errors.Is(err, avatar.ErrDimensions):
		refuse(http.StatusBadRequest, msgAvatarPixels)
		return
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		refuse(http.StatusServiceUnavailable, msgBusy)
		return
	default:
		slog.Error("account: avatar", "user", p.Viewer.UserID, "err", err)
		refuse(failStatus(r, err), msgWriteFailed)
		return
	}
	name, err := s.writeAvatarFile(p.Viewer.UserID, png)
	if err != nil {
		slog.Error("account: cannot write an avatar file", "user", p.Viewer.UserID, "err", err)
		refuse(http.StatusInternalServerError, msgWriteFailed)
		return
	}
	if beforeSetAvatar != nil {
		beforeSetAvatar()
	}
	// The write does not follow a cancelled request (see removeAvatar).
	old, err := board.SetAvatar(context.WithoutCancel(r.Context()), s.pool, p.Viewer, name)
	if err != nil {
		s.deleteAvatarFile(p.Viewer.UserID, name)
		s.settingResult(w, r, p, []error{err}, "avatar", "", action, nil,
			func(d *accountData, in *board.InputError) { d.Err.Avatar = in.Message })
		return
	}
	s.deleteAvatarFile(p.Viewer.UserID, old)
	if saveRequested(r) {
		writeJSON(w, http.StatusOK, map[string]string{"src": s.path("files", name)})
		return
	}
	http.Redirect(w, r, s.path("account")+"?done=avatar", http.StatusSeeOther)
}

// writeAvatarFile writes png to a new file avatars/u{id}-{16 hex}.png under the
// data directory and returns its path, relative to the data directory and with
// slashes. The open goes through an os.Root, so no name can leave the directory,
// and uses O_EXCL, so no file is overwritten. The folder is made when it is
// missing.
func (s *site) writeAvatarFile(userID int64, png []byte) (string, error) {
	root, err := os.OpenRoot(s.opt.DataDir)
	if err != nil {
		return "", fmt.Errorf("open the data directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir("avatars", 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("make the avatars folder: %w", err)
	}
	for range 5 {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return "", fmt.Errorf("random name: %w", err)
		}
		name := fmt.Sprintf("avatars/u%d-%s.png", userID, hex.EncodeToString(rnd[:]))
		f, err := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create the avatar file: %w", err)
		}
		// The mode of the new file does not depend on the umask of the process.
		merr := f.Chmod(0o644)
		_, werr := f.Write(png)
		cerr := f.Close()
		if err := errors.Join(merr, werr, cerr); err != nil {
			_ = root.Remove(filepath.FromSlash(name))
			return "", fmt.Errorf("write the avatar file: %w", err)
		}
		return name, nil
	}
	return "", errors.New("no free avatar file name")
}

// deleteAvatarFile deletes the file at path, a site path under the data
// directory, after a change of the avatar of member userID. It deletes only a
// path that [board.ValidAvatarPath] accepts; any other value is left alone and
// logged by member ID only. An empty path is no file. A file that is already gone
// is fine. A failure is logged and goes no further: the change has committed.
func (s *site) deleteAvatarFile(userID int64, path string) {
	if path == "" {
		return
	}
	if !board.ValidAvatarPath(path) {
		slog.Warn("account: the stored avatar path is not one that gobb deletes; the file is left alone", "user", userID)
		return
	}
	root, err := os.OpenRoot(s.opt.DataDir)
	if err != nil {
		slog.Warn("account: cannot open the data directory to delete an avatar file", "user", userID, "err", err)
		return
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(filepath.FromSlash(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("account: cannot delete an avatar file", "user", userID, "err", err)
	}
}

// saveSignature is the signature POST of gobb.js: the same write as
// [site.changeSignature], with a JSON answer.
func (s *site) saveSignature(w http.ResponseWriter, r *http.Request) {
	const action = "change your signature"
	p, ok := s.settingsStart(w, r, "account.signature", action, s.saves.saves, msgTooManySaves, nil)
	if !ok {
		return
	}
	err := board.SetSignature(r.Context(), s.pool, p.Viewer, r.PostForm.Get("signature"), s.opt.Now())
	saveAnswer(w, r, err, "signature")
}
