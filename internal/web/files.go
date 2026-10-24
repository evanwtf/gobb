package web

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// fileDirs are the data directories that /files/ serves. Attachments are not
// among them: they go through /a/{id}, because download rights apply to them.
var fileDirs = map[string]bool{"avatars": true, "smilies": true, "ranks": true, "header": true}

// imageTypes are the content types that /files/ serves.
var imageTypes = map[string]bool{
	"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true,
}

// files serves GET /files/{dir}/{name...}. It serves a regular file of an
// allowed directory when the content is an image of an allowed type. Anything
// else gets 404, and the answer does not say why.
func (s *site) files(w http.ResponseWriter, r *http.Request) {
	dir, name := r.PathValue("dir"), r.PathValue("name")
	if !fileDirs[dir] || name == "" {
		s.notFound(w, r)
		return
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == "" || strings.HasPrefix(elem, ".") || strings.ContainsAny(elem, `\`) || strings.ContainsRune(elem, 0) {
			s.notFound(w, r)
			return
		}
	}
	// The root keeps the open inside the directory: a ".." element or a symlink
	// that leads out of it fails.
	root, err := os.OpenRoot(filepath.Join(s.opt.DataDir, dir))
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer func() { _ = root.Close() }()
	// A symlink is not a regular file, even when it points inside the directory.
	// Lstat does not follow the last element.
	local := filepath.FromSlash(name)
	if li, err := root.Lstat(local); err != nil || !li.Mode().IsRegular() {
		s.notFound(w, r)
		return
	}
	f, err := root.Open(local)
	if err != nil {
		s.notFound(w, r)
		return
	}
	defer func() { _ = f.Close() }()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		s.notFound(w, r)
		return
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		s.serverError(w, r, err)
		return
	}
	ctype := detectImage(head[:n])
	if ctype == "" {
		s.notFound(w, r)
		return
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		s.serverError(w, r, err)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=86400")
	http.ServeContent(w, r, "", info.ModTime(), f)
}

// detectImage returns the content type of head when it is a type that /files/
// serves, and "" otherwise.
func detectImage(head []byte) string {
	ctype := http.DetectContentType(head)
	if imageTypes[ctype] {
		return ctype
	}
	return ""
}

// static serves GET /static/{name}: the stylesheet and the script.
func (s *site) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	var ctype string
	switch name {
	case "gobb.css":
		ctype = "text/css; charset=utf-8"
	case "gobb.js":
		ctype = "text/javascript; charset=utf-8"
	default:
		s.notFound(w, r)
		return
	}
	data, err := fs.ReadFile(staticFS, "static/"+name)
	if err != nil {
		s.notFound(w, r)
		return
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	// A request that carries the current version names these exact bytes, so a
	// browser may keep them for a year.
	if r.URL.Query().Get("v") == s.assetVersion {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "public, max-age=86400")
	}
	http.ServeContent(w, r, "", time.Time{}, bytes.NewReader(data))
}
