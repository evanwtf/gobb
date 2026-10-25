package bundle

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// fileDirs lists the directories under files/ that a field can name.
var fileDirs = []string{"attachments", "avatars", "smilies", "ranks", "header"}

// Writer writes a bundle directory. [Create] makes one; [Add] and AddFile fill
// it; Close finishes it. A Writer is not safe for concurrent use.
type Writer struct {
	dir      string
	source   string
	settings Settings
	data     map[string]*dataFile
	paths    map[string]bool // files/ paths that AddFile has written
	closed   bool
}

// dataFile is one open data file.
type dataFile struct {
	f     *os.File
	w     *bufio.Writer
	count int
}

// Create starts a bundle in dir. The directory must not exist, or it must be
// empty. Create makes it, with its parents, and opens all 21 data files. source
// is the manifest's "source" label. Create refuses settings or a source that
// fail their checks. When Create fails, it removes what it made.
func Create(dir, source string, s Settings) (*Writer, error) {
	m := Manifest{Format: Format, Version: Version, Source: source, Counts: map[string]int{}}
	for _, f := range Files {
		m.Counts[f] = 0
	}
	if errs := m.check(); len(errs) > 0 {
		errs[0].File = "manifest.json"
		return nil, errs[0]
	}
	if errs := s.check(); len(errs) > 0 {
		errs[0].File = "settings.json"
		return nil, errs[0]
	}
	if err := requireEmptyDir(dir); err != nil {
		return nil, err
	}
	w := &Writer{
		dir: dir, source: source, settings: s,
		data: map[string]*dataFile{}, paths: map[string]bool{},
	}
	_, statErr := os.Stat(dir)
	madeDir := errors.Is(statErr, fs.ErrNotExist)
	if err := os.MkdirAll(filepath.Join(dir, "files"), 0o755); err != nil {
		cleanDest(dir, madeDir)
		return nil, err
	}
	for _, name := range Files {
		f, err := createDataFile(filepath.Join(dir, name+".jsonl"))
		if err != nil {
			w.abort()
			cleanDest(dir, madeDir)
			return nil, err
		}
		w.data[name] = &dataFile{f: f, w: bufio.NewWriterSize(f, 64<<10)}
	}
	return w, nil
}

// requireEmptyDir returns an error unless dir does not exist or is an empty
// directory.
func requireEmptyDir(dir string) error {
	ents, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(ents) > 0:
		return fmt.Errorf("bundle: %s is not empty", dir)
	}
	return nil
}

// abort closes the open data files after a failure in Create.
func (w *Writer) abort() {
	for _, d := range w.data {
		_ = d.f.Close()
	}
}

// Add appends row to its data file. It checks the row first and refuses a row
// that fails a check, or that would make a line over [MaxLineBytes]. A refused
// row leaves the file as it was. The *[Error] names the line the row would
// take. Add writes a nil list as [] and a nil match as {}.
func Add[T Row](w *Writer, row T) error {
	if w.closed {
		return errors.New("bundle: Add after Close")
	}
	name, err := rowFile[T]()
	if err != nil {
		return err
	}
	d, ok := w.data[name]
	if !ok {
		return fmt.Errorf("bundle: %T is not a bundle row", row)
	}
	file, ln := name+".jsonl", d.count+1
	if e := firstError(any(row)); e != nil {
		e.File, e.Line = file, ln
		return e
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(normalize(any(row))); err != nil {
		return err
	}
	if n := buf.Len() - 1; n > MaxLineBytes {
		return &Error{File: file, Line: ln, Msg: fmt.Sprintf("row is %d bytes as JSON; the limit is %d", n, MaxLineBytes)}
	}
	if _, err := d.w.Write(buf.Bytes()); err != nil {
		return err
	}
	d.count++
	return nil
}

// normalize replaces a nil list that the format requires with an empty list,
// and a nil match with an empty one, so that the line holds [] and {} and not
// null.
func normalize(row any) any {
	switch r := row.(type) {
	case Role:
		if r.Settings == nil {
			r.Settings = []RoleSetting{}
		}
		return r
	case Poll:
		if r.Options == nil {
			r.Options = []PollOption{}
		}
		return r
	case PM:
		if r.Recipients == nil {
			r.Recipients = []Recipient{}
		}
		return r
	case LegacyRoute:
		if r.Match == nil {
			r.Match = map[string]string{}
		}
		return r
	}
	return row
}

// AddFile copies the file src to files/path in the bundle and returns the
// reference to put in a row. path is slash-separated. It must pass fs.ValidPath,
// hold no backslash, be at most 255 bytes, and start with one of attachments/,
// avatars/, smilies/, ranks/ or header/. AddFile refuses a path it has already
// written, and a src that is not a regular file (a symlink counts as not).
func (w *Writer) AddFile(src, path string) (FileRef, error) {
	if w.closed {
		return FileRef{}, errors.New("bundle: AddFile after Close")
	}
	first, _, _ := strings.Cut(path, "/")
	if !slices.Contains(fileDirs, first) {
		return FileRef{}, fmt.Errorf("bundle: %q must name a file under one of %s", clip(path), strings.Join(fileDirs, ", "))
	}
	var v vet // the rule for the path of a file reference
	v.filePath("path", path, first)
	if len(v.errs) > 0 {
		return FileRef{}, fmt.Errorf("bundle: path %q %s", clip(path), v.errs[0].Msg)
	}
	if w.paths[path] {
		return FileRef{}, fmt.Errorf("bundle: %q was added already", path)
	}
	// Lstat first: opening a FIFO with no writer blocks, and a symlink could
	// point anywhere.
	if st, err := os.Lstat(src); err != nil {
		return FileRef{}, err
	} else if !st.Mode().IsRegular() {
		return FileRef{}, fmt.Errorf("bundle: %s is not a regular file", filepath.Base(src))
	}
	in, err := os.Open(src)
	if err != nil {
		return FileRef{}, err
	}
	defer in.Close()
	dst := filepath.Join(w.dir, "files", filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return FileRef{}, err
	}
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return FileRef{}, err
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, h), in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		_ = os.Remove(dst)
		return FileRef{}, err
	}
	w.paths[path] = true
	return FileRef{Path: path, Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}

// Close finishes the bundle. It flushes and closes every data file, then writes
// settings.json and manifest.json, with the line counts and a UTC created_at.
// Close fails on a second call.
func (w *Writer) Close() error {
	if w.closed {
		return errors.New("bundle: Close called twice")
	}
	w.closed = true
	var errs []error
	m := Manifest{
		Format: Format, Version: Version, Source: w.source,
		CreatedAt: time.Now().UTC().Truncate(time.Second),
		Counts:    map[string]int{},
	}
	for _, name := range Files {
		d := w.data[name]
		errs = append(errs, d.w.Flush(), d.f.Close())
		m.Counts[name] = d.count
	}
	if err := errors.Join(errs...); err != nil {
		return err
	}
	if errs := m.check(); len(errs) > 0 {
		errs[0].File = "manifest.json"
		return errs[0]
	}
	if err := writeJSON(filepath.Join(w.dir, "settings.json"), w.settings); err != nil {
		return err
	}
	return writeJSON(filepath.Join(w.dir, "manifest.json"), m)
}

// writeJSON writes v to path as indented JSON with a final newline.
func writeJSON(path string, v any) error {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		return err
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

// createDataFile creates a new data file. It is a variable so that a test can
// make it fail.
var createDataFile = func(path string) (*os.File, error) {
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
}
