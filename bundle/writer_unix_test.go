//go:build unix

package bundle

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestAddFileRefusesNonRegularSources pins that AddFile checks the source with
// Lstat before it opens the source: a FIFO has no writer, so an open would
// block.
func TestAddFileRefusesNonRegularSources(t *testing.T) {
	w, err := Create(filepath.Join(t.TempDir(), "b"), "example", validSettings())
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	fifo := filepath.Join(tmp, "fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(tmp, "target")
	writeFile(t, target, "x")
	link := filepath.Join(tmp, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	for name, src := range map[string]string{"fifo": fifo, "symlink": link, "directory": tmp} {
		t.Run(name, func(t *testing.T) {
			done := make(chan error, 1)
			go func() {
				_, err := w.AddFile(src, "avatars/"+name+".png")
				done <- err
			}()
			select {
			case err := <-done:
				if err == nil || !strings.Contains(err.Error(), "is not a regular file") {
					t.Errorf("err = %v", err)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("AddFile blocked")
			}
		})
	}
}
