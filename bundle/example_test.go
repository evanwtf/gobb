package bundle_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// exampleDir is the committed example bundle, relative to this package.
const exampleDir = "../docs/example-bundle"

var update = flag.Bool("update", false, "rewrite docs/example-bundle from bundletest.Small")

// readTree returns the content of every regular file under dir, by slash path.
// It fails the test on anything else that is not a directory.
func readTree(t *testing.T, dir string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if !d.Type().IsRegular() {
			t.Errorf("%s is not a regular file", path)
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[filepath.ToSlash(rel)] = data
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// withoutCreatedAt decodes manifest.json and drops created_at, the one field
// that differs between two writes of one board.
func withoutCreatedAt(t *testing.T, data []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("manifest.json: %v", err)
	}
	if s, _ := m["created_at"].(string); s == "" {
		t.Fatal("manifest.json has no created_at")
	}
	delete(m, "created_at")
	return m
}

func TestExampleBundle(t *testing.T) {
	written := bundletest.Write(t, bundletest.Small())
	if *update {
		if err := os.RemoveAll(exampleDir); err != nil {
			t.Fatal(err)
		}
		for path, data := range readTree(t, written) {
			dst := filepath.Join(exampleDir, filepath.FromSlash(path))
			if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(dst, data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}

	want, got := readTree(t, written), readTree(t, exampleDir)
	for path := range want {
		if _, ok := got[path]; !ok {
			t.Errorf("%s is missing from %s; run go test ./bundle -run TestExampleBundle -update", path, exampleDir)
		}
	}
	for path, data := range got {
		w, ok := want[path]
		switch {
		case !ok:
			t.Errorf("%s is in %s, but Small() does not write it; run go test ./bundle -run TestExampleBundle -update", path, exampleDir)
		case path == "manifest.json":
			if !reflect.DeepEqual(withoutCreatedAt(t, w), withoutCreatedAt(t, data)) {
				t.Errorf("manifest.json differs from Small(), apart from created_at")
			}
		case !bytes.Equal(w, data):
			t.Errorf("%s differs from Small(); run go test ./bundle -run TestExampleBundle -update", path)
		}
	}

	r, err := bundle.Open(exampleDir)
	if err != nil {
		t.Fatalf("Open(example): %v", err)
	}
	if err := bundle.Check(r); err != nil {
		t.Fatalf("Check(example) = %v", err)
	}
}
