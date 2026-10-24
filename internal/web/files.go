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

// errNotServed means that /files/ would answer 404 for a file. The reason is
// not told.
var errNotServed = errors.New("web: file is not served")

// openImage opens name below the data directory dir for /files/. It succeeds for
// a regular file, inside an allowed directory, that holds an image of an allowed
// type. Any other file gives [errNotServed]; a read error gives that error. The
// caller closes the file, which is positioned at its start.
func (s *site) openImage(dir, name string) (*os.File, fs.FileInfo, string, error) {
	if !fileDirs[dir] || name == "" {
		return nil, nil, "", errNotServed
	}
	for _, elem := range strings.Split(name, "/") {
		if elem == "" || strings.HasPrefix(elem, ".") || strings.ContainsAny(elem, `\`) || strings.ContainsRune(elem, 0) {
			return nil, nil, "", errNotServed
		}
	}
	// The root keeps the open inside the directory: a ".." element or a symlink
	// that leads out of it fails.
	root, err := os.OpenRoot(filepath.Join(s.opt.DataDir, dir))
	if err != nil {
		return nil, nil, "", errNotServed
	}
	defer func() { _ = root.Close() }()
	// A symlink is not a regular file, even when it points inside the directory.
	// Lstat does not follow the last element.
	local := filepath.FromSlash(name)
	if li, err := root.Lstat(local); err != nil || !li.Mode().IsRegular() {
		return nil, nil, "", errNotServed
	}
	f, err := root.Open(local)
	if err != nil {
		return nil, nil, "", errNotServed
	}
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		_ = f.Close()
		return nil, nil, "", errNotServed
	}
	head := make([]byte, 512)
	n, err := io.ReadFull(f, head)
	if err != nil && !errors.Is(err, io.ErrUnexpectedEOF) && !errors.Is(err, io.EOF) {
		_ = f.Close()
		return nil, nil, "", err
	}
	ctype := detectImage(head[:n])
	if ctype == "" {
		_ = f.Close()
		return nil, nil, "", errNotServed
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, nil, "", err
	}
	return f, info, ctype, nil
}

// files serves GET /files/{dir}/{name...}. It serves a regular file of an
// allowed directory when the content is an image of an allowed type. Anything
// else gets 404, and the answer does not say why.
func (s *site) files(w http.ResponseWriter, r *http.Request) {
	f, info, ctype, err := s.openImage(r.PathValue("dir"), r.PathValue("name"))
	switch {
	case errors.Is(err, errNotServed):
		s.notFound(w, r)
		return
	case err != nil:
		s.serverError(w, r, err)
		return
	}
	defer func() { _ = f.Close() }()
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=86400")
	s.serveContent(w, r, info.ModTime(), f)
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

// staticTypes maps each file under /static/ to its Content-Type. No other name
// is served.
var staticTypes = map[string]string{
	"gobb.css": "text/css; charset=utf-8",
	"gobb.js":  "text/javascript; charset=utf-8",
	"icon.svg": "image/svg+xml",
}

// favicon serves GET /favicon.ico at the host root: the site icon, which the
// layout also links from /static/. A browser asks for this path by itself, on
// every site, so the answer reads no settings and no session, and does no
// database work.
func (s *site) favicon(w http.ResponseWriter, r *http.Request) {
	data, err := fs.ReadFile(staticFS, "static/icon.svg")
	if err != nil {
		s.writeFailure(w, http.StatusNotFound)
		return
	}
	h := w.Header()
	h.Set("Content-Type", staticTypes["icon.svg"])
	h.Set("X-Content-Type-Options", "nosniff")
	h.Set("Cache-Control", "public, max-age=86400")
	s.serveContent(w, r, time.Time{}, bytes.NewReader(data))
}

// static serves GET /static/{name}: the stylesheet and the script.
func (s *site) static(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	ctype, ok := staticTypes[name]
	if !ok {
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
	s.serveContent(w, r, time.Time{}, bytes.NewReader(data))
}

// serveContent is http.ServeContent with the error page of the site for the one
// plain-text answer that ServeContent writes: 416, for a Range header that no
// byte of the content fits. The 416 keeps its Content-Range header, but not the
// headers of the file (its disposition, its cache lifetime and its validators),
// because an error is not the file.
func (s *site) serveContent(w http.ResponseWriter, r *http.Request, modtime time.Time, content io.ReadSeeker) {
	g := &rangeGuard{ResponseWriter: w}
	http.ServeContent(g, r, "", modtime, content)
	if !g.refused {
		return
	}
	h := w.Header()
	for _, k := range []string{"Content-Disposition", "Cache-Control", "Last-Modified", "Etag", "Content-Security-Policy", "Content-Length", "Accept-Ranges"} {
		h.Del(k)
	}
	h.Set("Cache-Control", "no-store")
	s.fail(w, r, http.StatusRequestedRangeNotSatisfiable, "Range not satisfiable",
		"The part of the file that your browser asked for is outside the file.")
}

// rangeGuard holds back the 416 that http.ServeContent writes, so that the
// caller can answer it in the layout.
type rangeGuard struct {
	http.ResponseWriter
	refused bool
}

func (g *rangeGuard) WriteHeader(code int) {
	if code == http.StatusRequestedRangeNotSatisfiable {
		g.refused = true
		return
	}
	g.ResponseWriter.WriteHeader(code)
}

func (g *rangeGuard) Write(b []byte) (int, error) {
	if g.refused {
		return len(b), nil
	}
	return g.ResponseWriter.Write(b)
}

// ReadFrom keeps the fast path of the underlying writer for a file that is
// sent whole.
func (g *rangeGuard) ReadFrom(src io.Reader) (int64, error) {
	if g.refused {
		return io.Copy(io.Discard, src)
	}
	if rf, ok := g.ResponseWriter.(io.ReaderFrom); ok {
		return rf.ReadFrom(src)
	}
	return io.Copy(struct{ io.Writer }{g.ResponseWriter}, src)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (g *rangeGuard) Unwrap() http.ResponseWriter { return g.ResponseWriter }
