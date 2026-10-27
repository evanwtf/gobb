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

	"github.com/evanwtf/gobb/internal/avatar"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/login"
)

// The content editors of the admin area: the static pages, the smilies and the
// ranks. This file holds what the three share: the routes, the answer to the
// error of a write, the confirm page of a delete and the handling of an image
// upload.

// maxPageBody is the cap of the body of the page editor: 100,000 bytes of text,
// each of which may percent-encode to 3 bytes, and the other fields.
const maxPageBody = 400 << 10

// maxImageBody is the cap of the body of an upload of a smiley or rank image: the
// largest file and 64 KiB for the other parts.
const maxImageBody = avatar.MaxBytes + 64<<10

// adminContentRoutes adds the routes of the content editors.
func (s *site) adminContentRoutes(a adminRoutes) {
	a.page("/pages", s.adminPages)
	a.page("/pages/new", s.adminPageNew)
	a.write("/pages", maxPageBody, s.adminPageCreate)
	a.page("/pages/{slug}", s.adminPageEdit)
	a.write("/pages/{slug}", maxPageBody, s.adminPageSave)
	a.page("/pages/{slug}/delete", s.adminPageDeleteConfirm)
	a.write("/pages/{slug}/delete", maxPostBody, s.adminPageDelete)

	a.page("/smilies", s.adminSmilies)
	a.upload("/smilies", maxImageBody, s.adminSmileyCreate)
	a.page("/smilies/{id}", s.adminSmileyEdit)
	a.upload("/smilies/{id}", maxImageBody, s.adminSmileySave)
	a.write("/smilies/{id}/move", maxPostBody, s.adminSmileyMove)
	a.page("/smilies/{id}/delete", s.adminSmileyDeleteConfirm)
	a.write("/smilies/{id}/delete", maxPostBody, s.adminSmileyDelete)

	a.page("/ranks", s.adminRanks)
	a.upload("/ranks", maxImageBody, s.adminRankCreate)
	a.page("/ranks/{id}", s.adminRankEdit)
	a.upload("/ranks/{id}", maxImageBody, s.adminRankSave)
	a.page("/ranks/{id}/delete", s.adminRankDeleteConfirm)
	a.write("/ranks/{id}/delete", maxPostBody, s.adminRankDelete)
}

// fieldErrs holds the message beside each field of a form, by the name of the
// field.
type fieldErrs map[string]string

// outcomeKind says how a content write ended.
type outcomeKind int

const (
	// outDone: the write ran.
	outDone outcomeKind = iota
	// outNoChange: the write found nothing to change.
	outNoChange
	// outMissing: the target is gone, or the viewer may not write. The answer is
	// the 404 page.
	outMissing
	// outInvalid: a field is not acceptable. The form shows msg beside field.
	outInvalid
	// outFailed: the database failed. The form shows msg on top.
	outFailed
)

// outcome is how a content write ended, for the answer.
type outcome struct {
	kind       outcomeKind
	field, msg string
	status     int
}

// outcomeOf sorts the error of a board write. An error that is none of the known
// ones is logged, and the form gets the fixed message.
func outcomeOf(r *http.Request, what string, err error) outcome {
	var in *board.InputError
	switch {
	case err == nil:
		return outcome{kind: outDone}
	case errors.Is(err, board.ErrNoChange):
		return outcome{kind: outNoChange}
	case errors.Is(err, board.ErrNotFound), errors.Is(err, board.ErrForbidden):
		return outcome{kind: outMissing}
	case errors.As(err, &in):
		return outcome{kind: outInvalid, field: in.Field, msg: in.Message, status: http.StatusBadRequest}
	}
	slog.Error("admin content write failed", "what", what, "path", r.URL.Path, "err", err)
	return outcome{kind: outFailed, msg: msgWriteFailed, status: failStatus(r, err)}
}

// formAnswer is what a form shows after a refusal: the message beside a field,
// or on top.
func (o outcome) formAnswer(errs fieldErrs, known ...string) (fieldErrs, string) {
	if errs == nil {
		errs = fieldErrs{}
	}
	if o.kind == outInvalid {
		for _, f := range known {
			if f == o.field {
				errs[f] = o.msg
				return errs, ""
			}
		}
	}
	return errs, o.msg
}

// adminDeleteData is the data of admin-delete.html: the confirm page of a
// delete.
type adminDeleteData struct {
	Crumbs  []crumb
	Heading string
	// Lines are the paragraphs that say what the delete does.
	Lines []string
	// Action is the address that the form posts to, and Cancel the link back.
	Action, Cancel string
}

// renderDelete writes the confirm page of a delete.
func (s *site) renderDelete(w http.ResponseWriter, r *http.Request, crumbs []crumb, d adminDeleteData) {
	p, err := s.adminBegin(w, r, d.Heading)
	if err != nil {
		s.serverError(w, r, err)
		return
	}
	d.Crumbs = crumbs
	p.Data = d
	s.render(w, http.StatusOK, "admin-delete.html", p)
}

// imageUpload is the result of the file field "image" of a form.
type imageUpload struct {
	// Path is the new file under the data directory, or empty when the admin
	// sent no file.
	Path string
	// Msg is the message for the field, with the status of the answer, when the
	// upload was refused.
	Msg    string
	Status int
}

// The messages of a refused image.
const (
	msgImageFormat = "Pick a PNG, JPEG or GIF."
	msgImagePixels = "That image is too large. Use one of at most 512 pixels a side."
	msgImageMemory = "That image needs too much memory to process. Use a smaller one."
)

// storeImage reads the file of the form field "image", checks it with
// [avatar.Icon] under the memory gate and writes a fresh PNG to a new file in dir
// under the data directory. The name is made here from 8 random bytes; the name
// of the upload is never used. An admin who sent no file gets an empty Path. The
// caller deletes the new file when its database write fails.
func (s *site) storeImage(w http.ResponseWriter, r *http.Request, dir, prefix string) imageUpload {
	file, _, err := r.FormFile("image")
	switch {
	case errors.Is(err, http.ErrMissingFile), errors.Is(err, http.ErrNotMultipart):
		return imageUpload{}
	case err != nil:
		return imageUpload{Msg: msgImageFormat, Status: http.StatusBadRequest}
	}
	defer func() { _ = file.Close() }()
	png, err := avatar.Icon(r.Context(), file, s.gate, s.login.GateWait)
	switch {
	case err == nil:
	case errors.Is(err, avatar.ErrTooLarge):
		return imageUpload{Msg: msgFileTooLarge, Status: http.StatusRequestEntityTooLarge}
	case errors.Is(err, avatar.ErrFormat):
		return imageUpload{Msg: msgImageFormat, Status: http.StatusBadRequest}
	case errors.Is(err, avatar.ErrDimensions):
		return imageUpload{Msg: msgImagePixels, Status: http.StatusBadRequest}
	case errors.Is(err, avatar.ErrTooMuchMemory):
		return imageUpload{Msg: msgImageMemory, Status: http.StatusBadRequest}
	case errors.Is(err, login.ErrBusy):
		w.Header().Set("Retry-After", strconv.Itoa(busyRetrySeconds))
		return imageUpload{Msg: msgBusy, Status: http.StatusServiceUnavailable}
	default:
		slog.Error("admin content: image", "err", err)
		return imageUpload{Msg: msgWriteFailed, Status: failStatus(r, err)}
	}
	name, err := s.writeContentFile(dir, prefix, png)
	if err != nil {
		slog.Error("admin content: cannot write an image file", "dir", dir, "err", err)
		return imageUpload{Msg: msgWriteFailed, Status: http.StatusInternalServerError}
	}
	return imageUpload{Path: name}
}

// writeContentFile writes png to a new file dir/{prefix}-{16 hex}.png under the
// data directory and returns its path, relative to the data directory and with
// slashes. The open goes through an os.Root, so no name can leave the directory,
// and uses O_EXCL, so no file is overwritten. The folder is made when it is
// missing.
func (s *site) writeContentFile(dir, prefix string, png []byte) (string, error) {
	root, err := os.OpenRoot(s.opt.DataDir)
	if err != nil {
		return "", fmt.Errorf("open the data directory: %w", err)
	}
	defer func() { _ = root.Close() }()
	if err := root.Mkdir(dir, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", fmt.Errorf("make the %s folder: %w", dir, err)
	}
	for range 5 {
		var rnd [8]byte
		if _, err := rand.Read(rnd[:]); err != nil {
			return "", fmt.Errorf("random name: %w", err)
		}
		name := fmt.Sprintf("%s/%s-%s.png", dir, prefix, hex.EncodeToString(rnd[:]))
		f, err := root.OpenFile(filepath.FromSlash(name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return "", fmt.Errorf("create the image file: %w", err)
		}
		// The mode of the new file does not depend on the umask of the process.
		merr := f.Chmod(0o644)
		_, werr := f.Write(png)
		cerr := f.Close()
		if err := errors.Join(merr, werr, cerr); err != nil {
			_ = root.Remove(filepath.FromSlash(name))
			return "", fmt.Errorf("write the image file: %w", err)
		}
		return name, nil
	}
	return "", errors.New("no free image file name")
}

// deleteContentFile deletes the file at path, a site path under the data
// directory dir, after the database no longer names it. It deletes only a path
// that [board.ValidContentPath] accepts. An empty path is no file, and a file
// that is already gone is fine. A failure is logged and goes no further: the
// change has committed.
func (s *site) deleteContentFile(dir, path string) {
	if path == "" {
		return
	}
	if !board.ValidContentPath(dir, path) {
		slog.Warn("admin content: the stored image path is not one that gobb deletes; the file is left alone", "dir", dir)
		return
	}
	root, err := os.OpenRoot(s.opt.DataDir)
	if err != nil {
		slog.Warn("admin content: cannot open the data directory to delete an image file", "err", err)
		return
	}
	defer func() { _ = root.Close() }()
	if err := root.Remove(filepath.FromSlash(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		slog.Warn("admin content: cannot delete an image file", "dir", dir, "err", err)
	}
}

// detached returns the context for a database write that follows a file write: it
// does not end with the request, because a row and a file that disagree are worse
// than a write that finishes after the client left.
func detached(r *http.Request) context.Context { return context.WithoutCancel(r.Context()) }
