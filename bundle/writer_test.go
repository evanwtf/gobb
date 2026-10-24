package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCreate(t *testing.T) {
	t.Run("refuses a directory with files", func(t *testing.T) {
		dir := t.TempDir()
		writeFile(t, filepath.Join(dir, "x"), "x")
		if _, err := Create(dir, "example", validSettings()); err == nil {
			t.Error("Create in a non-empty directory succeeded")
		}
	})
	t.Run("refuses a file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "f")
		writeFile(t, p, "x")
		if _, err := Create(p, "example", validSettings()); err == nil {
			t.Error("Create over a file succeeded")
		}
	})
	t.Run("takes an empty directory", func(t *testing.T) {
		dir := t.TempDir()
		w, err := Create(dir, "example", validSettings())
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dir); err != nil {
			t.Error(err)
		}
	})
	t.Run("makes the parent directories", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "a", "b")
		w, err := Create(dir, "example", validSettings())
		if err != nil {
			t.Fatal(err)
		}
		if err := w.Close(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("refuses bad settings", func(t *testing.T) {
		s := validSettings()
		s.Timezone = "Mars/Base"
		dir := filepath.Join(t.TempDir(), "b")
		_, err := Create(dir, "example", s)
		var be *Error
		if !errors.As(err, &be) || be.File != "settings.json" || be.Field != "timezone" {
			t.Errorf("err = %v", err)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Error("Create left a directory behind")
		}
	})
	t.Run("refuses a bad source", func(t *testing.T) {
		_, err := Create(filepath.Join(t.TempDir(), "b"), strings.Repeat("s", 256), validSettings())
		var be *Error
		if !errors.As(err, &be) || be.File != "manifest.json" || be.Field != "source" {
			t.Errorf("err = %v", err)
		}
	})
}

func TestAddRefusesBadRow(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	w, err := Create(dir, "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	if err := Add(w, validUser()); err != nil {
		t.Fatal(err)
	}
	bad := validUser()
	bad.State = "banned"
	err = Add(w, bad)
	var be *Error
	if !errors.As(err, &be) {
		t.Fatalf("err = %v, want *Error", err)
	}
	if be.File != "users.jsonl" || be.Line != 2 || be.Field != "state" {
		t.Errorf("err = %#v", be)
	}
	// The failed call did not take line 2.
	if err := Add(w, validUser()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "users.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(string(data), "\n"); got != 2 {
		t.Errorf("users.jsonl has %d lines, want 2", got)
	}
	if strings.Contains(string(data), "banned") {
		t.Error("the bad row reached the file")
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Manifest().Counts["users"] != 2 {
		t.Errorf("count = %d, want 2", r.Manifest().Counts["users"])
	}
}

func TestAddRefusesPointerRows(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	w, err := Create(dir, "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	role := validRole()
	err = Add(w, &role)
	if err == nil || !strings.Contains(err.Error(), "must be a struct") {
		t.Errorf("Add(&role) = %v", err)
	}
	err = Add(w, (*User)(nil))
	if err == nil || !strings.Contains(err.Error(), "must be a struct") {
		t.Errorf("Add((*User)(nil)) = %v", err)
	}
	if err := Add[Row](w, validUser()); err == nil {
		t.Error("Add with the Row interface as T succeeded")
	}
	if err := Add(w, validUser()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "roles.jsonl"))
	if len(data) != 0 {
		t.Errorf("roles.jsonl = %q; a refused row wrote to it", data)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.Manifest().Counts["roles"] != 0 || r.Manifest().Counts["users"] != 1 {
		t.Errorf("counts = %v", r.Manifest().Counts)
	}
}

func TestCreateCleansUpAfterAFailure(t *testing.T) {
	fail := func(t *testing.T) {
		old := createDataFile
		n := 0
		createDataFile = func(path string) (*os.File, error) {
			n++
			if n == 5 {
				return nil, errors.New("disk full")
			}
			return old(path)
		}
		t.Cleanup(func() { createDataFile = old })
	}
	t.Run("a directory that Create made", func(t *testing.T) {
		fail(t)
		parent := t.TempDir()
		dir := filepath.Join(parent, "b")
		if _, err := Create(dir, "example", validSettings()); err == nil || !strings.Contains(err.Error(), "disk full") {
			t.Fatalf("err = %v", err)
		}
		if _, err := os.Stat(dir); err == nil {
			t.Error("Create left the directory")
		}
	})
	t.Run("an empty directory that existed", func(t *testing.T) {
		fail(t)
		dir := t.TempDir()
		if _, err := Create(dir, "example", validSettings()); err == nil {
			t.Fatal("Create succeeded")
		}
		ents, err := os.ReadDir(dir)
		if err != nil {
			t.Fatalf("Create removed the directory: %v", err)
		}
		if len(ents) != 0 {
			t.Errorf("Create left %d entries", len(ents))
		}
	})
}

func TestAddRefusesOversizeRow(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "b"), "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	// Every field is within its own limit; the row as a whole is not. A
	// message has any number of recipients, and each takes about 60 bytes.
	pm := func(n int) PM {
		p := validPM()
		p.Recipients = nil
		for i := range n {
			p.Recipients = append(p.Recipients, Recipient{UserID: int64(i + 1), Kind: "to"})
		}
		return p
	}
	if err := Add(w, pm(1000)); err != nil {
		t.Fatalf("a row under the limit: %v", err)
	}
	err = Add(w, pm(30000))
	var be *Error
	if !errors.As(err, &be) || be.File != "pms.jsonl" || be.Line != 2 || !strings.Contains(be.Msg, "limit") {
		t.Errorf("err = %v", err)
	}
	if err := Add(w, pm(1)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterNormalizesRequiredCollections(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b")
	w, err := Create(dir, "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	role := validRole()
	role.Settings = nil
	route := validRoute()
	route.Match = nil
	if err := Add(w, role); err != nil {
		t.Fatal(err)
	}
	if err := Add(w, route); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	for file, want := range map[string]string{
		"roles.jsonl":         `"settings":[]`,
		"legacy_routes.jsonl": `"match":{}`,
	} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), want) {
			t.Errorf("%s = %s; want it to hold %s", file, data, want)
		}
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Each(r, func(int, Role) error { return nil }); err != nil {
		t.Errorf("roles: %v", err)
	}
	if err := Each(r, func(int, LegacyRoute) error { return nil }); err != nil {
		t.Errorf("legacy_routes: %v", err)
	}
}

func TestWriterAddFile(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.bin")
	content := strings.Repeat("0123456789", 1000)
	writeFile(t, src, content)
	sum := sha256.Sum256([]byte(content))

	dir := filepath.Join(t.TempDir(), "b")
	w, err := Create(dir, "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	fr, err := w.AddFile(src, "attachments/12/a.png")
	if err != nil {
		t.Fatal(err)
	}
	if fr.Path != "attachments/12/a.png" || fr.Size != int64(len(content)) || fr.SHA256 != hex.EncodeToString(sum[:]) {
		t.Errorf("ref = %+v", fr)
	}
	got, err := os.ReadFile(filepath.Join(dir, "files", "attachments", "12", "a.png"))
	if err != nil || string(got) != content {
		t.Errorf("copy differs: %v", err)
	}
	for _, f := range []FileRef{fr} {
		if errs := (Attachment{ID: 1, UploaderID: ptr(int64(1)), Name: "a", File: f, Mime: "image/png", CreatedAt: testTime}).check(); len(errs) != 0 {
			t.Errorf("the ref fails its check: %v", errs[0])
		}
	}

	bad := []struct{ name, path, want string }{
		{"parent element", "attachments/../x", "not a clean slash-separated path"},
		{"absolute", "/attachments/x", "must name a file under one of"},
		{"unknown directory", "other/x.png", "must name a file under one of"},
		{"directory only", "attachments", "must name a file under attachments/"},
		{"trailing slash", "attachments/", "not a clean slash-separated path"},
		{"empty element", "attachments//x", "not a clean slash-separated path"},
		{"backslash", `attachments/a\x`, "holds a backslash"},
		{"dot", ".", "must name a file under one of"},
		{"too long", "attachments/" + strings.Repeat("a", 250), "the limit is 255"},
		{"NUL", "attachments/a\x00b", "holds U+0000"},
		{"already added", "attachments/12/a.png", "was added already"},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := w.AddFile(src, tt.path)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want it to contain %q", err, tt.want)
			}
		})
	}
	t.Run("missing source", func(t *testing.T) {
		if _, err := w.AddFile(filepath.Join(t.TempDir(), "none"), "avatars/n.png"); err == nil {
			t.Error("AddFile succeeded")
		}
		if _, err := os.Stat(filepath.Join(dir, "files", "avatars", "n.png")); err == nil {
			t.Error("a failed AddFile left a file")
		}
	})
	t.Run("source is a directory", func(t *testing.T) {
		if _, err := w.AddFile(t.TempDir(), "avatars/d.png"); err == nil {
			t.Error("AddFile succeeded")
		}
	})
	for _, d := range []string{"attachments", "avatars", "smilies", "ranks", "header"} {
		if _, err := w.AddFile(src, d+"/ok.png"); err != nil {
			t.Errorf("%s: %v", d, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestWriterClosed(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "b"), "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err == nil {
		t.Error("second Close succeeded")
	}
	if err := Add(w, validUser()); err == nil {
		t.Error("Add after Close succeeded")
	}
	src := filepath.Join(t.TempDir(), "s")
	writeFile(t, src, "x")
	if _, err := w.AddFile(src, "ranks/a.png"); err == nil {
		t.Error("AddFile after Close succeeded")
	}
}
