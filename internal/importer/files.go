package importer

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/evanwtf/gobb/bundle"
)

// dataDirs lists the five directories under the data directory that the
// importer owns. The first element of every stored file path is one of them.
var dataDirs = []string{"attachments", "avatars", "smilies", "ranks", "header"}

// newStagingDir makes DataDir/.import-<16 lowercase hex> and returns its path.
func newStagingDir(dataDir string) (string, error) {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("importer: random staging name: %w", err)
	}
	dir := filepath.Join(dataDir, ".import-"+hex.EncodeToString(b[:]))
	if err := os.Mkdir(dir, 0o755); err != nil {
		return "", fmt.Errorf("importer: make the staging directory: %w", err)
	}
	return dir, nil
}

// wantedFiles collects every file reference of the bundle, keyed by path. A
// path that several rows name appears once: Check has proved that the rows agree.
// The map holds one small entry for each distinct file, never a file's content.
func wantedFiles(r *bundle.Reader) (map[string]bundle.FileRef, error) {
	want := map[string]bundle.FileRef{}
	add := func(f *bundle.FileRef) {
		if f != nil {
			want[f.Path] = *f
		}
	}
	add(r.Settings().HeaderImage)
	if err := bundle.Each(r, func(_ int, x bundle.Rank) error { add(x.Image); return nil }); err != nil {
		return nil, err
	}
	if err := bundle.Each(r, func(_ int, x bundle.User) error { add(x.Avatar); return nil }); err != nil {
		return nil, err
	}
	if err := bundle.Each(r, func(_ int, x bundle.Attachment) error { add(&x.File); return nil }); err != nil {
		return nil, err
	}
	if err := bundle.Each(r, func(_ int, x bundle.Smiley) error { add(&x.Image); return nil }); err != nil {
		return nil, err
	}
	return want, nil
}

// stageFiles puts every file of the bundle in staging/files and returns how
// many it put there. With rename set, the bundle sits in the staging directory
// already, and the files move. Otherwise the files are copied, and each copy is
// hashed while it is written. A file whose size or SHA-256 differs from the
// bundle's stops the import.
func stageFiles(r *bundle.Reader, staging string, rename bool) (int, error) {
	want, err := wantedFiles(r)
	if err != nil {
		return 0, err
	}
	n := 0
	for p, ref := range want {
		src := filepath.Join(r.Dir(), "files", filepath.FromSlash(p))
		dst := filepath.Join(staging, "files", filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return n, fmt.Errorf("importer: stage %s: %w", p, err)
		}
		if rename {
			err = os.Rename(src, dst)
		} else {
			err = copyVerified(src, dst, ref)
		}
		if err != nil {
			return n, fmt.Errorf("importer: stage files/%s: %w", p, err)
		}
		n++
	}
	return n, nil
}

// copyVerified copies the regular file src to the new file dst and compares the
// size and SHA-256 of the bytes it copied with want.
func copyVerified(src, dst string, want bundle.FileRef) (err error) {
	st, err := os.Lstat(src)
	if err != nil {
		return err
	}
	if !st.Mode().IsRegular() {
		return errors.New("is not a regular file")
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := out.Close(); err == nil {
			err = cerr
		}
	}()
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(out, h), in)
	if err != nil {
		return err
	}
	if size != want.Size {
		return fmt.Errorf("has %d bytes, the bundle says %d", size, want.Size)
	}
	if got := hex.EncodeToString(h.Sum(nil)); got != want.SHA256 {
		return fmt.Errorf("has sha256 %s, the bundle says %s", got, want.SHA256)
	}
	return nil
}

// checkDataDirs refuses a data directory that holds an entry.
func checkDataDirs(dataDir string) error {
	for _, d := range dataDirs {
		entries, err := os.ReadDir(filepath.Join(dataDir, d))
		switch {
		case errors.Is(err, fs.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf("importer: read data directory %s: %w", d, err)
		case len(entries) > 0:
			return fmt.Errorf("importer: data directory %s is not empty; import into an empty data directory or use --replace", d)
		}
	}
	return nil
}

// replacedName is the layout of the time in the name of the directory that
// receives the old data directories.
const replacedName = "20060102T150405Z"

// installFiles moves the staged data directories into dataDir and creates the
// empty ones with mode 0755. With replace set, it moves the existing data
// directories to dataDir/.replaced-<UTC time>/ first.
func installFiles(dataDir, staging string, replace bool, now time.Time) error {
	if replace {
		old := filepath.Join(dataDir, ".replaced-"+now.UTC().Format(replacedName))
		for _, d := range dataDirs {
			from := filepath.Join(dataDir, d)
			if _, err := os.Lstat(from); errors.Is(err, fs.ErrNotExist) {
				continue
			} else if err != nil {
				return err
			}
			if err := os.MkdirAll(old, 0o755); err != nil {
				return err
			}
			if err := os.Rename(from, filepath.Join(old, d)); err != nil {
				return err
			}
		}
	}
	for _, d := range dataDirs {
		to := filepath.Join(dataDir, d)
		from := filepath.Join(staging, "files", d)
		if _, err := os.Lstat(from); err == nil {
			// checkDataDirs proved that an existing directory is empty, so
			// Remove drops only an empty directory.
			if err := os.Remove(to); err != nil && !errors.Is(err, fs.ErrNotExist) {
				return err
			}
			if err := os.Rename(from, to); err != nil {
				return err
			}
		} else if errors.Is(err, fs.ErrNotExist) {
			if err := os.Mkdir(to, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
				return err
			}
		} else {
			return err
		}
		if err := os.Chmod(to, 0o755); err != nil {
			return err
		}
	}
	return nil
}

// isArchive reports whether src names a gzip-compressed tar archive.
func isArchive(src string) bool {
	s := strings.ToLower(path.Base(filepath.ToSlash(src)))
	return strings.HasSuffix(s, ".tar.gz") || strings.HasSuffix(s, ".tgz")
}
