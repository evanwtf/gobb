package login_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestEveryPasswordHashWriterBumpsTheEpoch covers T6-M2. A sign-in compares the
// password epoch, not the hash, so a write of password_hash that forgets to add
// one to password_epoch would leave a stale sign-in alive. The test reads every
// SQL string of the non-test code and checks each UPDATE that sets password_hash:
// it must bump the epoch, except the rehash on sign-in, which stores the same
// password in a new form and is named below. A new writer fails the test until it
// bumps the epoch or is named here on purpose.
//
// An INSERT of a user row sets the hash of a new user, whose epoch is the column
// default of 0, and is not a change of a password.
func TestEveryPasswordHashWriterBumpsTheEpoch(t *testing.T) {
	// The one writer that does not bump: the rehash in login.go. It must change
	// the hash only where the hash and the epoch are still the ones it read.
	const rehashFile = "internal/login/login.go"
	rehashGuard := regexp.MustCompile(`(?s)where id = \$1 and password_hash = \$\d+ and password_epoch = \$\d+`)
	setsHash := regexp.MustCompile(`(?is)\bupdate\b[^;]*\bset\b[^;]*\bpassword_hash\s*=`)
	bumps := regexp.MustCompile(`(?is)password_epoch\s*=\s*password_epoch\s*\+\s*1`)

	root := filepath.Join("..", "..")
	var writers, bumpers, rehashes int
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		name := d.Name()
		if d.IsDir() && (strings.HasPrefix(name, ".") && path != root || name == "docs" || name == "node_modules") {
			return fs.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		src, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		f, err := parser.ParseFile(token.NewFileSet(), path, src, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			sql, err := strconv.Unquote(lit.Value)
			if err != nil || !setsHash.MatchString(sql) {
				return true
			}
			writers++
			switch {
			case bumps.MatchString(sql):
				bumpers++
			case rel == rehashFile && rehashGuard.MatchString(sql):
				rehashes++
			default:
				t.Errorf("%s writes password_hash and does not add one to password_epoch:\n%s", rel, sql)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// The scan must see the writers that exist today, or it checks nothing.
	if bumpers < 2 || rehashes != 1 {
		t.Errorf("the scan found %d writers that bump the epoch and %d rehash (of %d), want at least 2 and exactly 1", bumpers, rehashes, writers)
	}
}
