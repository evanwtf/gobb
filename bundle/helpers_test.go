package bundle

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// newBundle writes an empty, valid bundle and returns its directory.
func newBundle(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "b")
	w, err := Create(dir, "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return dir
}

// editJSON reads the JSON object in dir/name, applies f to it and writes it
// back.
func editJSON(t *testing.T, dir, name string, f func(m map[string]any)) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	f(m)
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), out, 0o644); err != nil {
		t.Fatal(err)
	}
}

// dataBundle returns an opened bundle whose data file name holds content, with
// count as the manifest count for it.
func dataBundle(t *testing.T, name, content string, count int) *Reader {
	t.Helper()
	dir := newBundle(t)
	if err := os.WriteFile(filepath.Join(dir, name+".jsonl"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	editJSON(t, dir, "manifest.json", func(m map[string]any) {
		m["counts"].(map[string]any)[name] = count
	})
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// line marshals row, applies f to its JSON object and returns one line, with
// no newline.
func line(t *testing.T, row any, f func(m map[string]any)) string {
	t.Helper()
	data, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatal(err)
	}
	if f != nil {
		f(m)
	}
	out, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// eachErr runs Each over a file of type T and returns the error text. It
// fails the test when the error is not an *Error.
func eachErr[T Row](t *testing.T, r *Reader) string {
	t.Helper()
	err := Each(r, func(int, T) error { return nil })
	if err == nil {
		t.Fatal("Each returned no error")
	}
	if _, ok := err.(*Error); !ok {
		t.Fatalf("error is %T, want *Error: %v", err, err)
	}
	return err.Error()
}

// userRows returns n valid user lines joined with newlines, with a trailing
// newline.
func userRows(t *testing.T, n int) string {
	t.Helper()
	var b strings.Builder
	for i := range n {
		u := validUser()
		u.ID = int64(i + 1)
		u.Username = "user" + strconv.Itoa(i+1)
		b.WriteString(line(t, u, nil))
		b.WriteByte('\n')
	}
	return b.String()
}
