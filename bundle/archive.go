package bundle

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// maxExtractBytes is the most that Extract reads from an archive, counted in
// bytes of the uncompressed tar stream. It is a variable so that a test can
// lower it.
var maxExtractBytes int64 = 8 << 30

// errTooBig is the error of a stream that passes maxExtractBytes.
var errTooBig = errors.New("the archive passes the limit on total size")

// Pack writes a gzip-compressed tar archive of the directory dir to the file
// archive. Entry names are relative to dir, in slash form, in lexical order,
// with no leading "./". Pack writes regular files and directories only, with
// fixed owner, time and mode fields, so that the same tree gives the same
// bytes. It refuses a symlink or any other kind of entry, and it refuses an
// archive path that exists. When Pack fails, it removes the partial archive.
func Pack(dir, archive string) (err error) {
	if st, err := os.Stat(dir); err != nil {
		return err
	} else if !st.IsDir() {
		return fmt.Errorf("bundle: %s is not a directory", dir)
	}
	out, err := os.OpenFile(archive, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	self, err := out.Stat()
	if err != nil {
		_ = out.Close()
		_ = os.Remove(archive)
		return err
	}
	defer func() {
		if err != nil {
			_ = os.Remove(archive)
		}
	}()
	gz := gzip.NewWriter(out)
	tw := tar.NewWriter(gz)
	err = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if os.SameFile(info, self) {
			return nil // the archive is inside dir
		}
		h := &tar.Header{Name: filepath.ToSlash(rel), ModTime: time.Unix(0, 0), Format: tar.FormatPAX}
		switch {
		case info.IsDir():
			h.Typeflag, h.Name, h.Mode = tar.TypeDir, h.Name+"/", 0o755
		case info.Mode().IsRegular():
			h.Typeflag, h.Mode, h.Size = tar.TypeReg, 0o644, info.Size()
		default:
			return fmt.Errorf("bundle: %s is not a regular file or a directory", h.Name)
		}
		if err := tw.WriteHeader(h); err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			return nil
		}
		return copyFile(tw, p, h.Size)
	})
	err = errors.Join(err, tw.Close(), gz.Close(), out.Close())
	return err
}

// copyFile copies the file at path, which holds size bytes, to w.
func copyFile(w io.Writer, path string, size int64) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(w, f)
	if err == nil && n != size {
		err = fmt.Errorf("bundle: %s changed while Pack read it", filepath.Base(path))
	}
	return err
}

// limitReader returns errTooBig once more than n bytes pass through it.
type limitReader struct {
	r io.Reader
	n int64
}

func (l *limitReader) Read(p []byte) (int, error) {
	if l.n < 0 {
		return 0, errTooBig
	}
	if int64(len(p)) > l.n+1 {
		p = p[:l.n+1]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	if l.n < 0 {
		return n, errTooBig
	}
	return n, err
}

// Extract unpacks the gzip-compressed tar archive into dest. dest must not
// exist, or it must be empty. Extract makes dest, with its parents.
//
// Extract writes regular files and directories only, with mode 0644 and 0755
// whatever the archive says. It refuses an absolute path, a path that
// fs.ValidPath rejects, a path with a backslash, a symlink, a hard link, a
// device, a FIFO, a repeated file, and any other kind of entry. It stops when
// the uncompressed stream passes 8 GiB.
//
// Extract strips one leading "./" from an entry name and skips the bare "./"
// entry, so an archive made with "tar -C dir -czf bundle.tar.gz ." works. It
// stays strict about the rest: Open refuses an AppleDouble "._" file at the top
// level later, as an unknown entry.
//
// When Extract fails, it removes what it wrote, and dest too if it made dest.
func Extract(archive, dest string) (err error) {
	_, statErr := os.Stat(dest)
	madeDest := errors.Is(statErr, fs.ErrNotExist)
	if err := requireEmptyDir(dest); err != nil {
		return err
	}
	f, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return fmt.Errorf("bundle: %s is not a gzip file: %w", filepath.Base(archive), err)
	}
	defer gz.Close()
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	defer func() {
		if err != nil {
			cleanDest(dest, madeDest)
		}
	}()

	tr := tar.NewReader(&limitReader{r: gz, n: maxExtractBytes})
	var total int64
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return extractErr("", err)
		}
		name, skip, err := entryName(h)
		if err != nil {
			return err
		}
		if skip {
			continue
		}
		target := filepath.Join(dest, filepath.FromSlash(name))
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return extractErr(name, err)
			}
		case tar.TypeReg:
			total += h.Size
			if total > maxExtractBytes {
				return extractErr(name, errTooBig)
			}
			if err := extractFile(tr, target, h.Size); err != nil {
				return extractErr(name, err)
			}
		}
	}
}

// entryName checks the header of one entry and returns its clean name, without
// a trailing slash. skip is true for the bare "./" entry.
func entryName(h *tar.Header) (name string, skip bool, err error) {
	name = h.Name
	switch h.Typeflag {
	case tar.TypeReg, tar.TypeDir:
	default:
		return "", false, extractErr(h.Name, fmt.Errorf("is of type %q; only regular files and directories are allowed", string(h.Typeflag)))
	}
	if name == "./" && h.Typeflag == tar.TypeDir {
		return "", true, nil
	}
	name = strings.TrimPrefix(name, "./")
	if h.Typeflag == tar.TypeDir {
		name = strings.TrimSuffix(name, "/")
	}
	switch {
	case strings.HasPrefix(name, "/"):
		return "", false, extractErr(h.Name, errors.New("is an absolute path"))
	case strings.Contains(name, `\`):
		return "", false, extractErr(h.Name, errors.New("holds a backslash"))
	case name == "." || !fs.ValidPath(name):
		return "", false, extractErr(h.Name, errors.New("is not a clean relative path"))
	}
	return name, false, nil
}

// extractFile writes size bytes from r to a new file at target.
func extractFile(r io.Reader, target string, size int64) error {
	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, err = io.CopyN(f, r, size)
	return errors.Join(err, f.Close())
}

// extractErr describes a fault in an archive entry.
func extractErr(name string, err error) error {
	if errors.Is(err, errTooBig) {
		return fmt.Errorf("bundle: %w (the limit is %d bytes)", err, maxExtractBytes)
	}
	if name == "" {
		return fmt.Errorf("bundle: reading the archive: %w", err)
	}
	return fmt.Errorf("bundle: archive entry %q: %w", clip(name), err)
}

// cleanDest removes what Extract wrote: dest itself when Extract made it, and
// the contents of dest otherwise.
func cleanDest(dest string, remove bool) {
	if remove {
		_ = os.RemoveAll(dest)
		return
	}
	ents, _ := os.ReadDir(dest)
	for _, e := range ents {
		_ = os.RemoveAll(filepath.Join(dest, e.Name()))
	}
}
