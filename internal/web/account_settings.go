package web

import (
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
)

// The texts of the settings answers.
const (
	msgTooManySaves   = "Too many changes. Wait a few minutes and try again."
	msgTooManyUploads = "Too many uploads. Wait an hour and try again."
	msgAvatarFormat   = "Pick a PNG, JPEG or GIF."
	msgAvatarPixels   = "That image is too large. Use one of at most 8,000 pixels a side."
	msgBirthdayForm   = "Pick a day, a month and a year from the lists."
	msgNoRight        = "You cannot make this change."
)

// maxAvatarBody is the cap of the body of an upload: the largest file and 64 KiB
// for the other parts, so that a multipart body stays in memory.
const maxAvatarBody = avatar.MaxBytes + 64<<10

// saveLimits holds the limiters of the settings.
type saveLimits struct {
	saves, uploads *ratelimit.Limiter
}

func newSaveLimits() saveLimits {
	return saveLimits{
		saves:   ratelimit.New(savesPerWindow, saveWindow, saveLimitMaxKeys),
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
		saveMessage(w, http.StatusNotFound, msgSignatureGone)
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

// settingsStart starts a POST handler of the settings. It answers and reports
// false for a guest, for a member who lacks option ("" asks for none), for a
// member over the limit lim, and for a page that cannot be built. Otherwise it
// returns the page and the data. A request from gobb.js gets JSON; any other
// gets the page.
func (s *site) settingsStart(w http.ResponseWriter, r *http.Request, option, action string, lim *ratelimit.Limiter, tooMany string) (*page, accountData, bool) {
	p, err := s.begin(w, r, "Account", "account")
	if err != nil {
		if saveRequested(r) {
			slog.Error("account: page", "path", r.URL.Path, "err", err)
			saveMessage(w, failStatus(r, err), msgWriteFailed)
		} else {
			s.serverError(w, r, err)
		}
		return nil, accountData{}, false
	}
	if p.Viewer.UserID == 0 || (option != "" && !p.Viewer.Perms.Has(option, 0)) {
		if saveRequested(r) {
			saveMessage(w, http.StatusForbidden, deniedText(p, action))
		} else {
			s.deniedTo(w, p, action)
		}
		return nil, accountData{}, false
	}
	d, err := s.accountData(r, p)
	if err != nil {
		switch {
		case saveRequested(r):
			saveAnswer(w, r, err, "")
		case errors.Is(err, board.ErrNotFound):
			s.notFound(w, r)
		default:
			s.serverError(w, r, err)
		}
		return nil, accountData{}, false
	}
	// The try is counted before the write, and never given back: a refusal costs
	// as much as a success.
	if ok, wait := lim.Take(strconv.FormatInt(p.Viewer.UserID, 10), s.opt.Now()); !ok {
		w.Header().Set("Retry-After", strconv.Itoa(retrySeconds(wait)))
		if saveRequested(r) {
			saveMessage(w, http.StatusTooManyRequests, tooMany)
		} else {
			d.Err.Notice = tooMany
			s.renderAccount(w, http.StatusTooManyRequests, p, d)
		}
		return nil, accountData{}, false
	}
	return p, d, true
}

// settingResult ends a settings POST for the result err of the write. For a
// request from gobb.js it answers 204 or JSON. For any other it redirects to the
// account page with the notice done, or answers the refusal with the page; field
// puts the message of an input error beside its field.
func (s *site) settingResult(w http.ResponseWriter, r *http.Request, p *page, d accountData, err error, jsonField, done, action string,
	field func(*accountData, *board.InputError)) {
	if saveAnswer(w, r, err, jsonField) {
		return
	}
	notice := func(d *accountData, m string) { d.Err.Notice = m }
	if s.writeRefused(w, r, p, d, err, field, notice, action) {
		return
	}
	http.Redirect(w, r, s.path("account")+"?done="+done, http.StatusSeeOther)
}

// setTimezone serves POST /account/timezone.
func (s *site) setTimezone(w http.ResponseWriter, r *http.Request) {
	const action = "change your time zone"
	p, d, ok := s.settingsStart(w, r, "", action, s.saves.saves, msgTooManySaves)
	if !ok {
		return
	}
	err := board.SetTimezone(r.Context(), s.pool, p.Viewer, r.PostForm.Get("timezone"))
	s.settingResult(w, r, p, d, err, "timezone", "timezone", action,
		func(d *accountData, in *board.InputError) { d.Err.Zone = in.Message })
}

// setDateFormat serves POST /account/dateformat.
func (s *site) setDateFormat(w http.ResponseWriter, r *http.Request) {
	const action = "change your date format"
	p, d, ok := s.settingsStart(w, r, "", action, s.saves.saves, msgTooManySaves)
	if !ok {
		return
	}
	err := board.SetDateFormat(r.Context(), s.pool, p.Viewer, r.PostForm.Get("dateformat"))
	s.settingResult(w, r, p, d, err, "dateformat", "dateformat", action,
		func(d *accountData, in *board.InputError) { d.Err.Format = in.Message })
}

// setAbout serves POST /account/about. It changes only the fields that the form
// holds: a text box that is absent stays as it is. The birthday changes only when
// bday, bmonth and byear are all present.
func (s *site) setAbout(w http.ResponseWriter, r *http.Request) {
	const action = "change your profile"
	p, d, ok := s.settingsStart(w, r, "account.profile", action, s.saves.saves, msgTooManySaves)
	if !ok {
		return
	}
	form := r.PostForm
	var c board.AboutChange
	for name, dst := range map[string]struct {
		change **string
		echo   *string
	}{
		"location":   {&c.Location, &d.Location},
		"occupation": {&c.Occupation, &d.Occupation},
		"interests":  {&c.Interests, &d.Interests},
		"website":    {&c.Website, &d.Website},
	} {
		if form.Has(name) {
			v := form.Get(name)
			*dst.change = &v
			*dst.echo = v
		}
	}
	err := board.SetAbout(r.Context(), s.pool, p.Viewer, c)
	if err == nil && form.Has("bday") && form.Has("bmonth") && form.Has("byear") {
		var b board.Birthday
		b, err = parseBirthday(form.Get("bday"), form.Get("bmonth"), form.Get("byear"))
		if err == nil {
			d.BirthDay, d.BirthMonth, d.BirthYear = b.Day, b.Month, b.Year
			err = board.SetBirthday(r.Context(), s.pool, p.Viewer, b, s.opt.Now().UTC())
		}
	}
	s.settingResult(w, r, p, d, err, "", "about", action, func(d *accountData, in *board.InputError) {
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
// numbers only.
func parseBirthday(day, month, year string) (board.Birthday, error) {
	var out board.Birthday
	for _, f := range []struct {
		in  string
		out *int
	}{{day, &out.Day}, {month, &out.Month}, {year, &out.Year}} {
		if f.in == "" {
			continue
		}
		n, err := strconv.Atoi(f.in)
		if err != nil || n < 0 {
			return board.Birthday{}, &board.InputError{Field: "birthday", Message: msgBirthdayForm}
		}
		*f.out = n
	}
	return out, nil
}

// removeAvatar serves POST /account/avatar/remove.
func (s *site) removeAvatar(w http.ResponseWriter, r *http.Request) {
	const action = "change your avatar"
	p, d, ok := s.settingsStart(w, r, "account.avatar", action, s.saves.saves, msgTooManySaves)
	if !ok {
		return
	}
	old, err := board.RemoveAvatar(r.Context(), s.pool, p.Viewer)
	if err == nil {
		s.deleteAvatarFile(p.Viewer.UserID, old)
	}
	s.settingResult(w, r, p, d, err, "avatar", "avatar-removed", action,
		func(d *accountData, in *board.InputError) { d.Err.Avatar = in.Message })
}

// uploadAvatar serves POST /account/avatar. The handler owns the files:
//
//  1. avatar.Make checks the upload and makes a new 128 x 128 PNG.
//  2. The PNG goes to avatars/u{id}-{16 random hex}.png under the data directory,
//     through an os.Root, created with O_EXCL.
//  3. board.SetAvatar stores the path. When it fails, the new file is deleted.
//  4. When it succeeds, the file it replaced is deleted, if its path is one that
//     gobb may delete.
//
// A request from gobb.js gets 200 and {"src": the site path of the new file}.
func (s *site) uploadAvatar(w http.ResponseWriter, r *http.Request) {
	const action = "change your avatar"
	p, d, ok := s.settingsStart(w, r, "account.avatar", action, s.saves.uploads, msgTooManyUploads)
	if !ok {
		return
	}
	// refuse answers a problem with the upload: JSON with the field, or the page
	// with the message beside the file box.
	refuse := func(status int, msg string) {
		switch {
		case saveRequested(r) && status == http.StatusBadRequest:
			writeJSON(w, status, map[string]string{"field": "avatar", "message": msg})
		case saveRequested(r):
			saveMessage(w, status, msg)
		default:
			d.Err.Avatar = msg
			s.renderAccount(w, status, p, d)
		}
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
	old, err := board.SetAvatar(r.Context(), s.pool, p.Viewer, name)
	if err != nil {
		s.deleteAvatarFile(p.Viewer.UserID, name)
		s.settingResult(w, r, p, d, err, "avatar", "", action,
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
		_, werr := f.Write(png)
		cerr := f.Close()
		if err := errors.Join(werr, cerr); err != nil {
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
	p, _, ok := s.settingsStart(w, r, "account.signature", action, s.saves.saves, msgTooManySaves)
	if !ok {
		return
	}
	err := board.SetSignature(r.Context(), s.pool, p.Viewer, r.PostForm.Get("signature"), s.opt.Now())
	saveAnswer(w, r, err, "signature")
}
