package bundle

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// tarEntry is one entry of a test archive.
type tarEntry struct {
	hdr  tar.Header
	body string
}

// buildArchive writes a .tar.gz of entries and returns its path.
func buildArchive(t *testing.T, entries ...tarEntry) string {
	t.Helper()
	return writeArchive(t, archiveBytes(t, entries...))
}

// writeArchive writes data to a new file and returns its path.
func writeArchive(t *testing.T, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "a.tar.gz")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// archiveBytes returns a .tar.gz of entries. The gzip layer stores the data
// without compression, so a test can find a byte of a file and change it.
func archiveBytes(t *testing.T, entries ...tarEntry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz, err := gzip.NewWriterLevel(&buf, gzip.NoCompression)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		h := e.hdr
		if h.Typeflag == tar.TypeReg {
			h.Size = int64(len(e.body))
		}
		if h.Mode == 0 && h.Typeflag != tar.TypeXGlobalHeader {
			h.Mode = 0o644
		}
		if err := tw.WriteHeader(&h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := io.WriteString(tw, e.body); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func reg(name, body string) tarEntry {
	return tarEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeReg}, body: body}
}

func dirEntry(name string) tarEntry {
	return tarEntry{hdr: tar.Header{Name: name, Typeflag: tar.TypeDir, Mode: 0o755}}
}

// snapshot maps every path under dir to its content ("" for a directory).
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(dir, p)
		if d.IsDir() {
			out[filepath.ToSlash(rel)+"/"] = ""
			return nil
		}
		data, err := os.ReadFile(p)
		out[filepath.ToSlash(rel)] = string(data)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestPackExtractRoundTrip(t *testing.T) {
	src := filepath.Join(t.TempDir(), "src.bin")
	writeFile(t, src, strings.Repeat("png", 5000))
	dir := filepath.Join(t.TempDir(), "bundle")
	w, err := Create(dir, "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.AddFile(src, "avatars/7/a.png"); err != nil {
		t.Fatal(err)
	}
	if err := Add(w, validUser()); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}

	archive := filepath.Join(t.TempDir(), "out.tar.gz")
	if err := Pack(dir, archive); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := Extract(archive, dest); err != nil {
		t.Fatal(err)
	}
	want, got := snapshot(t, dir), snapshot(t, dest)
	if len(want) < 28 {
		t.Fatalf("the source bundle has %d paths", len(want))
	}
	if len(got) != len(want) {
		t.Errorf("extracted %d paths, want %d", len(got), len(want))
	}
	for p, c := range want {
		if g, ok := got[p]; !ok || g != c {
			t.Errorf("%s: missing or different after the round trip", p)
		}
	}
	if _, err := Open(dest); err != nil {
		t.Errorf("Open of the extracted bundle: %v", err)
	}

	t.Run("entry names are relative and clean", func(t *testing.T) {
		f, err := os.Open(archive)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		var names []string
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			names = append(names, h.Name)
			if strings.HasPrefix(h.Name, "/") || strings.HasPrefix(h.Name, "./") {
				t.Errorf("entry %q is not relative", h.Name)
			}
			if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir {
				t.Errorf("entry %q has type %c", h.Name, h.Typeflag)
			}
		}
		for _, n := range []string{"manifest.json", "files/avatars/7/a.png", "files/"} {
			if !slices.Contains(names, n) {
				t.Errorf("no entry %q in %v", n, names)
			}
		}
		if !slices.IsSorted(names) {
			t.Errorf("entries are not in order: %v", names)
		}
	})

	t.Run("pack is repeatable", func(t *testing.T) {
		a2 := filepath.Join(t.TempDir(), "again.tar.gz")
		if err := Pack(dir, a2); err != nil {
			t.Fatal(err)
		}
		b1, _ := os.ReadFile(archive)
		b2, _ := os.ReadFile(a2)
		if !bytes.Equal(b1, b2) {
			t.Error("two packs of one directory differ")
		}
	})
}

func TestPackRefuses(t *testing.T) {
	t.Run("existing archive", func(t *testing.T) {
		dir := newBundle(t)
		p := filepath.Join(t.TempDir(), "a.tar.gz")
		writeFile(t, p, "keep")
		if err := Pack(dir, p); err == nil {
			t.Error("Pack over a file succeeded")
		}
		if b, _ := os.ReadFile(p); string(b) != "keep" {
			t.Error("Pack changed an existing file")
		}
	})
	t.Run("symlink in the tree", func(t *testing.T) {
		dir := newBundle(t)
		if err := os.Symlink("manifest.json", filepath.Join(dir, "files", "l")); err != nil {
			t.Skip("no symlinks:", err)
		}
		p := filepath.Join(t.TempDir(), "a.tar.gz")
		if err := Pack(dir, p); err == nil {
			t.Error("Pack of a tree with a symlink succeeded")
		}
		if _, err := os.Stat(p); err == nil {
			t.Error("a failed Pack left an archive")
		}
	})
	t.Run("missing directory", func(t *testing.T) {
		if err := Pack(filepath.Join(t.TempDir(), "none"), filepath.Join(t.TempDir(), "a.tar.gz")); err == nil {
			t.Error("Pack of a missing directory succeeded")
		}
	})
	t.Run("archive inside the directory", func(t *testing.T) {
		dir := newBundle(t)
		p := filepath.Join(dir, "files", "a.tar.gz")
		if err := Pack(dir, p); err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		gz, _ := gzip.NewReader(f)
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err != nil {
				break
			}
			if strings.HasSuffix(h.Name, "a.tar.gz") {
				t.Errorf("the archive holds itself: %s", h.Name)
			}
		}
	})
}

func TestExtractRefusesUnsafeEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []tarEntry
		want    string // part of the error
	}{
		{"parent path", []tarEntry{reg("../evil", "x")}, "is not a clean relative path"},
		{"nested parent path", []tarEntry{reg("a/../../evil", "x")}, "is not a clean relative path"},
		{"dot-slash then parent path", []tarEntry{reg("./../evil", "x")}, "is not a clean relative path"},
		{"absolute path", []tarEntry{reg("/abs", "x")}, "is an absolute path"},
		{"absolute path after dot-slash", []tarEntry{reg(".//abs", "x")}, "is an absolute path"},
		{"double dot-slash", []tarEntry{reg("././x", "x")}, "is not a clean relative path"},
		{"empty element", []tarEntry{reg("a//b", "x")}, "is not a clean relative path"},
		{"backslash", []tarEntry{reg(`a\b`, "x")}, "holds a backslash"},
		{"dot entry", []tarEntry{reg(".", "x")}, "is not a clean relative path"},
		{"parent directory", []tarEntry{dirEntry("../d/")}, "is not a clean relative path"},
		{"symlink", []tarEntry{{hdr: tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"}}}, `is of type "2"`},
		{"symlink out", []tarEntry{{hdr: tar.Header{Name: "l", Typeflag: tar.TypeSymlink, Linkname: ".."}}, reg("l/evil", "x")}, `is of type "2"`},
		{"hard link", []tarEntry{reg("a", "x"), {hdr: tar.Header{Name: "h", Typeflag: tar.TypeLink, Linkname: "a"}}}, `is of type "1"`},
		{"character device", []tarEntry{{hdr: tar.Header{Name: "c", Typeflag: tar.TypeChar, Devmajor: 1, Devminor: 3}}}, `is of type "3"`},
		{"block device", []tarEntry{{hdr: tar.Header{Name: "b", Typeflag: tar.TypeBlock}}}, `is of type "4"`},
		{"fifo", []tarEntry{{hdr: tar.Header{Name: "f", Typeflag: tar.TypeFifo}}}, `is of type "6"`},
		{"duplicate file", []tarEntry{reg("a", "1"), reg("a", "2")}, "file exists"},
		{"file under a file", []tarEntry{reg("a", "1"), reg("a/b", "2")}, "not a directory"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			archive := buildArchive(t, tt.entries...)
			parent := t.TempDir()
			dest := filepath.Join(parent, "dest")
			err := Extract(archive, dest)
			if err == nil {
				t.Fatal("Extract succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			// Nothing appears outside dest, and a failed Extract leaves no dest.
			ents, err := os.ReadDir(parent)
			if err != nil {
				t.Fatal(err)
			}
			for _, e := range ents {
				t.Errorf("left behind: %s", e.Name())
			}
			if _, err := os.Stat(filepath.Join(filepath.Dir(parent), "evil")); err == nil {
				t.Error("a file escaped to the parent of the parent")
			}
		})
	}
}

func TestExtractCleansUpAnExistingDest(t *testing.T) {
	archive := buildArchive(t, reg("ok", "1"), reg("../evil", "x"))
	dest := t.TempDir()
	if err := Extract(archive, dest); err == nil {
		t.Fatal("Extract succeeded")
	}
	ents, err := os.ReadDir(dest)
	if err != nil {
		t.Fatalf("dest was removed: %v", err)
	}
	if len(ents) != 0 {
		t.Errorf("dest holds %d entries after a failed Extract", len(ents))
	}
}

func TestExtractAcceptsDotSlashEntries(t *testing.T) {
	archive := buildArchive(t,
		dirEntry("./"),
		reg("./manifest.json", "{}"),
		dirEntry("./files/"),
		dirEntry("./files/ranks/"),
		reg("./files/ranks/1.png", "png"),
		reg("settings.json", "s"), // an entry without the prefix is fine too
	)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := Extract(archive, dest); err != nil {
		t.Fatal(err)
	}
	got := snapshot(t, dest)
	want := map[string]string{
		"./":                "",
		"manifest.json":     "{}",
		"files/":            "",
		"files/ranks/":      "",
		"files/ranks/1.png": "png",
		"settings.json":     "s",
	}
	for p, c := range want {
		if g, ok := got[p]; !ok || g != c {
			t.Errorf("%s: got %q (present %v), want %q", p, g, ok, c)
		}
	}
	if len(got) != len(want) {
		t.Errorf("extracted %v", got)
	}
}

// TestExtractThenOpenRefusesAppleDouble pins that Extract stays strict about
// names: an AppleDouble file at the top level extracts, and Open refuses it.
func TestExtractThenOpenRefusesAppleDouble(t *testing.T) {
	var entries []tarEntry
	dir := newBundle(t)
	for p, c := range snapshot(t, dir) {
		if strings.HasSuffix(p, "/") {
			if p != "./" {
				entries = append(entries, dirEntry(p))
			}
			continue
		}
		entries = append(entries, reg(p, c))
	}
	entries = append(entries, reg("._users.jsonl", "junk"))
	dest := filepath.Join(t.TempDir(), "dest")
	if err := Extract(buildArchive(t, entries...), dest); err != nil {
		t.Fatal(err)
	}
	_, err := Open(dest)
	if err == nil || !strings.Contains(err.Error(), "._users.jsonl: is not part of the bundle layout") {
		t.Errorf("Open err = %v", err)
	}
}

func TestExtractDest(t *testing.T) {
	archive := buildArchive(t, reg("a", "1"))
	mustFail := func(t *testing.T, err error, want string) {
		t.Helper()
		if err == nil {
			t.Fatal("Extract succeeded")
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %q, want it to contain %q", err, want)
		}
	}
	t.Run("refuses a non-empty directory", func(t *testing.T) {
		dest := t.TempDir()
		writeFile(t, filepath.Join(dest, "x"), "keep")
		mustFail(t, Extract(archive, dest), "is not empty")
		if b, _ := os.ReadFile(filepath.Join(dest, "x")); string(b) != "keep" {
			t.Error("Extract changed an existing file")
		}
	})
	t.Run("refuses a file", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "f")
		writeFile(t, dest, "x")
		mustFail(t, Extract(archive, dest), "not a directory")
	})
	t.Run("takes an empty directory", func(t *testing.T) {
		dest := t.TempDir()
		if err := Extract(archive, dest); err != nil {
			t.Fatal(err)
		}
		if b, _ := os.ReadFile(filepath.Join(dest, "a")); string(b) != "1" {
			t.Error("file missing")
		}
	})
	t.Run("makes parent directories", func(t *testing.T) {
		dest := filepath.Join(t.TempDir(), "x", "y")
		if err := Extract(archive, dest); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("missing archive", func(t *testing.T) {
		mustFail(t, Extract(filepath.Join(t.TempDir(), "none"), filepath.Join(t.TempDir(), "d")), "no such file")
	})
	t.Run("not gzip", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "a")
		writeFile(t, p, "plain text, not gzip")
		mustFail(t, Extract(p, filepath.Join(t.TempDir(), "d")), "is not a gzip file")
	})
	t.Run("gzip but not tar", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		_, _ = gz.Write(bytes.Repeat([]byte("not a tar header "), 100))
		_ = gz.Close()
		p := filepath.Join(t.TempDir(), "a")
		if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		mustFail(t, Extract(p, filepath.Join(t.TempDir(), "d")), "reading the archive")
	})
	t.Run("truncated archive", func(t *testing.T) {
		data := archiveBytes(t, reg("a", strings.Repeat("x", 100000)))
		p := writeArchive(t, data[:len(data)/2])
		mustFail(t, Extract(p, filepath.Join(t.TempDir(), "d")), "unexpected EOF")
	})
}

// TestExtractVerifiesGzip pins that Extract reads the gzip stream to its end:
// tar stops at its own end blocks, so the CRC and the trailer need a separate
// read.
func TestExtractVerifiesGzip(t *testing.T) {
	const marker = "FLIP-THIS-BYTE-0123456789"
	good := archiveBytes(t, reg("a", marker), reg("b", "second"))
	if err := Extract(writeArchive(t, good), filepath.Join(t.TempDir(), "d")); err != nil {
		t.Fatalf("the good archive: %v", err)
	}
	flipped := bytes.Clone(good)
	flipped[bytes.Index(flipped, []byte(marker))+3] ^= 0x01
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{"flipped byte in a file", flipped, "checksum"},
		{"missing trailer", good[:len(good)-8], "unexpected EOF"},
		{"short trailer", good[:len(good)-3], "unexpected EOF"},
		{"junk after the stream", append(bytes.Clone(good), "junk"...), "follows the end of the gzip stream"},
		{"a second gzip member", append(bytes.Clone(good), good...), "follows the end of the gzip stream"},
		{"trailing zero bytes", append(bytes.Clone(good), 0, 0, 0, 0), "follows the end of the gzip stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			dest := filepath.Join(parent, "d")
			err := Extract(writeArchive(t, tt.data), dest)
			if err == nil {
				t.Fatal("Extract succeeded")
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error = %q, want it to contain %q", err, tt.want)
			}
			if _, err := os.Stat(dest); err == nil {
				t.Error("a failed Extract left dest")
			}
		})
	}
}

func TestExtractSkipsGlobalPaxHeader(t *testing.T) {
	archive := buildArchive(t,
		tarEntry{hdr: tar.Header{
			Typeflag:   tar.TypeXGlobalHeader,
			PAXRecords: map[string]string{"comment": "0123456789abcdef"},
		}},
		reg("a", "1"),
	)
	dest := filepath.Join(t.TempDir(), "d")
	if err := Extract(archive, dest); err != nil {
		t.Fatal(err)
	}
	got := snapshot(t, dest)
	if len(got) != 2 || got["a"] != "1" {
		t.Errorf("extracted %v; want only a", got)
	}
}

func TestExtractStopsAtTheEntryLimit(t *testing.T) {
	old := maxExtractEntries
	maxExtractEntries = 5
	t.Cleanup(func() { maxExtractEntries = old })
	var es []tarEntry
	for i := range 6 {
		es = append(es, reg("f"+string(rune('a'+i)), ""))
	}
	err := Extract(buildArchive(t, es...), filepath.Join(t.TempDir(), "d"))
	if err == nil || !strings.Contains(err.Error(), "more than 5 entries") {
		t.Errorf("err = %v", err)
	}
	if err := Extract(buildArchive(t, es[:5]...), filepath.Join(t.TempDir(), "d")); err != nil {
		t.Errorf("5 entries: %v", err)
	}
}

func TestExtractStopsAtTheSizeLimit(t *testing.T) {
	old := maxExtractBytes
	maxExtractBytes = 10_000
	t.Cleanup(func() { maxExtractBytes = old })

	t.Run("one big file", func(t *testing.T) {
		archive := buildArchive(t, reg("big", strings.Repeat("x", 20_000)))
		dest := filepath.Join(t.TempDir(), "d")
		err := Extract(archive, dest)
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("err = %v", err)
		}
		if _, err := os.Stat(dest); err == nil {
			t.Error("a failed Extract left dest")
		}
	})
	t.Run("many small files", func(t *testing.T) {
		var es []tarEntry
		for i := range 30 {
			es = append(es, reg("f"+string(rune('a'+i%26))+string(rune('a'+i/26)), strings.Repeat("x", 500)))
		}
		err := Extract(buildArchive(t, es...), filepath.Join(t.TempDir(), "d"))
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("many empty entries", func(t *testing.T) {
		// Each header takes 512 bytes of the stream, so entries with no data
		// count against the limit as well.
		var es []tarEntry
		for i := range 40 {
			es = append(es, dirEntry("d"+string(rune('a'+i%26))+string(rune('a'+i/26))+"/"))
		}
		err := Extract(buildArchive(t, es...), filepath.Join(t.TempDir(), "d"))
		if err == nil || !strings.Contains(err.Error(), "limit") {
			t.Errorf("err = %v", err)
		}
	})
	t.Run("under the limit", func(t *testing.T) {
		archive := buildArchive(t, reg("ok", strings.Repeat("x", 2_000)))
		if err := Extract(archive, filepath.Join(t.TempDir(), "d")); err != nil {
			t.Error(err)
		}
	})
}

func TestExtractLimits(t *testing.T) {
	if maxExtractBytes != 8<<30 {
		t.Errorf("maxExtractBytes = %d, want 8 GiB", maxExtractBytes)
	}
	if maxExtractEntries != 1_000_000 {
		t.Errorf("maxExtractEntries = %d, want 1,000,000", maxExtractEntries)
	}
}
