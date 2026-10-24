package bundle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
)

// Reader reads a bundle directory. Open makes one. A Reader holds the manifest
// and the settings; the data files stay on disk until [Each] reads them.
type Reader struct {
	dir      string
	manifest Manifest
	settings Settings
}

// Open opens the bundle in dir. It reads and checks manifest.json and
// settings.json. Open refuses a top-level entry that is not in the layout, a
// missing manifest, settings file or data file, an unknown or
// missing key in the two JSON files, and a wrong format or version. It refuses
// a counts object that lacks a data file or names an extra one.
//
// Open does not read the data files. [Each] reads one.
func Open(dir string) (*Reader, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{"manifest.json": false, "settings.json": false, "files": true}
	for _, f := range Files {
		want[f+".jsonl"] = false
	}
	seen := map[string]bool{}
	for _, e := range entries {
		isDir, ok := want[e.Name()]
		switch {
		case !ok:
			return nil, &Error{File: e.Name(), Msg: "is not part of the bundle layout"}
		case isDir && !e.IsDir():
			return nil, &Error{File: e.Name(), Msg: "must be a directory"}
		case !isDir && !e.Type().IsRegular():
			return nil, &Error{File: e.Name(), Msg: "must be a regular file"}
		}
		seen[e.Name()] = true
	}
	// files/ may be absent: it holds nothing.
	order := []string{"manifest.json", "settings.json"}
	for _, f := range Files {
		order = append(order, f+".jsonl")
	}
	for _, name := range order {
		if !seen[name] {
			return nil, &Error{File: name, Msg: "is missing"}
		}
	}

	r := &Reader{dir: dir}
	if err := checkFormatVersion(dir); err != nil {
		return nil, err
	}
	if r.manifest, err = readJSON[Manifest](dir, "manifest.json"); err != nil {
		return nil, err
	}
	if errs := r.manifest.check(); len(errs) > 0 {
		errs[0].File = "manifest.json"
		return nil, errs[0]
	}
	if r.settings, err = readJSON[Settings](dir, "settings.json"); err != nil {
		return nil, err
	}
	if errs := r.settings.check(); len(errs) > 0 {
		errs[0].File = "settings.json"
		return nil, errs[0]
	}
	return r, nil
}

// checkFormatVersion reads "format" and "version" of manifest.json before
// anything else, and with no strict rules. A manifest from another version may
// have other keys, and the reader must say "wrong version" and not "unknown
// field". When either key is absent or has the wrong type, the strict read that
// follows reports it.
func checkFormatVersion(dir string) error {
	data, err := readSmall(dir, "manifest.json")
	if err != nil {
		return err
	}
	var probe struct {
		Format  *string `json:"format"`
		Version *int    `json:"version"`
	}
	_ = json.Unmarshal(data, &probe) // a value of the wrong type stays nil or partial
	var v vet
	switch {
	case probe.Format != nil && probe.Version != nil:
		v.formatVersion(*probe.Format, *probe.Version)
	case probe.Format != nil:
		v.formatVersion(*probe.Format, Version)
	case probe.Version != nil:
		v.formatVersion(Format, *probe.Version)
	}
	if len(v.errs) > 0 {
		v.errs[0].File = "manifest.json"
		return v.errs[0]
	}
	return nil
}

// readSmall reads the small JSON file name in dir.
func readSmall(dir, name string) ([]byte, error) {
	f, err := os.Open(filepath.Join(dir, name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, MaxLineBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaxLineBytes {
		return nil, &Error{File: name, Msg: "is longer than " + strconv.Itoa(MaxLineBytes) + " bytes"}
	}
	return data, nil
}

// readJSON reads the small JSON file name in dir and decodes it with the strict
// rules of the format.
func readJSON[T any](dir, name string) (T, error) {
	var zero T
	data, err := readSmall(dir, name)
	if err != nil {
		return zero, err
	}
	v, e := decodeStrict[T](data)
	if e != nil {
		e.File = name
		return zero, e
	}
	return v, nil
}

// Dir returns the directory of the bundle.
func (r *Reader) Dir() string { return r.dir }

// Manifest returns a copy of the manifest.
func (r *Reader) Manifest() Manifest {
	m := r.manifest
	m.Counts = maps.Clone(m.Counts)
	return m
}

// Settings returns a copy of the settings.
func (r *Reader) Settings() Settings {
	s := r.settings
	if s.HeaderImage != nil {
		h := *s.HeaderImage
		s.HeaderImage = &h
	}
	return s
}

// Each streams the rows of one data file and calls fn for each. T picks the
// file: Each[User] reads users.jsonl. T must be a row struct type, not a
// pointer. line counts from 1.
//
// Each reads one line at a time and keeps no row after fn returns, so memory
// does not grow with the file. For each line it refuses:
//
//   - a line over [MaxLineBytes], a CRLF line ending, invalid UTF-8, or an empty
//     line;
//   - data after the JSON value, a key that is unknown, repeated or missing, a
//     null where the field is not a pointer, and a value of the wrong type;
//   - U+0000 in any string, and an escape for half of a surrogate pair;
//   - a row that fails its field checks.
//
// Each fills File, Line and Field in the *[Error] it returns. When the file
// ends, Each compares the line count with the manifest. When fn returns an
// error, Each stops and returns that error unchanged.
func Each[T Row](r *Reader, fn func(line int, row T) error) error {
	name, err := rowFile[T]()
	if err != nil {
		return err
	}
	file := name + ".jsonl"
	f, err := os.Open(filepath.Join(r.dir, file))
	if err != nil {
		return err
	}
	defer f.Close()

	br := bufio.NewReaderSize(f, 64<<10)
	var buf []byte
	n := 0
	for {
		var tooLong bool
		buf, tooLong, err = readLine(br, buf[:0])
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		n++
		if tooLong {
			return &Error{File: file, Line: n, Msg: "line is longer than " + strconv.Itoa(MaxLineBytes) + " bytes"}
		}
		if len(buf) > 0 && buf[len(buf)-1] == '\r' {
			return &Error{File: file, Line: n, Msg: "CRLF line ending; use LF"}
		}
		if len(bytes.TrimSpace(buf)) == 0 {
			return &Error{File: file, Line: n, Msg: "empty line"}
		}
		row, e := decodeStrict[T](buf)
		if e == nil {
			e = firstError(any(row))
		}
		if e != nil {
			e.File, e.Line = file, n
			return e
		}
		if err := fn(n, row); err != nil {
			return err
		}
	}
	if want := r.manifest.Counts[name]; n != want {
		return &Error{File: file, Msg: strconv.Itoa(n) + " rows, manifest says " + strconv.Itoa(want)}
	}
	return nil
}

// rowFile returns the data file name of row type T. T must be a struct type:
// a pointer type has no usable zero value.
func rowFile[T Row]() (string, error) {
	rt := reflect.TypeFor[T]()
	if rt.Kind() != reflect.Struct {
		return "", fmt.Errorf("bundle: the row type must be a struct, not %s", rt)
	}
	return reflect.Zero(rt).Interface().(Row).DataFile(), nil
}

// firstError runs the field checks of row and returns the first fault.
func firstError(row any) *Error {
	c, ok := row.(checker)
	if !ok {
		return &Error{Msg: fmt.Sprintf("bundle: %T has no field check", row)}
	}
	if errs := c.check(); len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// readLine reads one line into buf and returns it without its newline. It
// returns io.EOF when no line is left. When the line is longer than
// MaxLineBytes, it stops reading and sets tooLong; the rest of the line stays
// unread, and the caller must stop.
func readLine(br *bufio.Reader, buf []byte) (line []byte, tooLong bool, err error) {
	for {
		frag, err := br.ReadSlice('\n')
		buf = append(buf, frag...)
		switch {
		case err == nil:
			buf = buf[:len(buf)-1]
			return buf, len(buf) > MaxLineBytes, nil
		case errors.Is(err, bufio.ErrBufferFull):
			if len(buf) > MaxLineBytes {
				return buf, true, nil
			}
		case errors.Is(err, io.EOF):
			if len(buf) == 0 {
				return nil, false, io.EOF
			}
			return buf, len(buf) > MaxLineBytes, nil
		default:
			return nil, false, err
		}
	}
}
