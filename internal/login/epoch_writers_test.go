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

// rehashFile holds the one writer that does not bump the epoch: the rehash in
// login.go. It stores the same password in a new form, and it must change the
// hash only where the hash and the epoch are still the ones it read.
const rehashFile = "internal/login/login.go"

var (
	rehashGuard = regexp.MustCompile(`(?s)where id = \$1 and password_hash = \$\d+ and password_epoch = \$\d+`)
	// setClause cuts the SET list out of an UPDATE: what follows "set", up to
	// the first WHERE, FROM or RETURNING.
	setClause = regexp.MustCompile(`(?is)\bupdate\b.*?\bset\b(.*?)(?:\bwhere\b|\bfrom\b|\breturning\b|$)`)
	hashWord  = regexp.MustCompile(`(?i)\bpassword_hash\b`)
	// bumps matches the plain form, "password_epoch = password_epoch + 1", and
	// the tuple form, "(password_hash, password_epoch) = ($2, password_epoch + 1)".
	bumps = regexp.MustCompile(`(?is)\bpassword_epoch\s*=\s*password_epoch\s*\+\s*1|\([^)]*\bpassword_epoch\b[^)]*\)\s*=\s*\([^)]*\bpassword_epoch\s*\+\s*1`)
)

// scanEpochWriters reads the SQL strings of one Go file and counts the UPDATE
// statements that set password_hash. A statement that the code builds from
// literals joined with "+" is read as one string, and the tuple form
// "set (password_hash, ...) = (...)" counts as a write of the hash. It returns
// the writers that bump the epoch, the rehashes, and the statements that do
// neither.
func scanEpochWriters(rel string, src []byte) (writers, bumpers, rehashes int, bad []string, err error) {
	f, err := parser.ParseFile(token.NewFileSet(), rel, src, 0)
	if err != nil {
		return 0, 0, 0, nil, err
	}
	check := func(sql string) {
		m := setClause.FindStringSubmatch(sql)
		if m == nil || !hashWord.MatchString(m[1]) {
			return
		}
		writers++
		switch {
		case bumps.MatchString(sql):
			bumpers++
		case rel == rehashFile && rehashGuard.MatchString(sql):
			rehashes++
		default:
			bad = append(bad, sql)
		}
	}
	ast.Inspect(f, func(n ast.Node) bool {
		switch n := n.(type) {
		case *ast.BinaryExpr:
			// A chain of "+" is one statement: join its literals in order, and
			// do not read the pieces again.
			if n.Op != token.ADD {
				return true
			}
			var sb strings.Builder
			var join func(e ast.Expr)
			join = func(e ast.Expr) {
				switch e := e.(type) {
				case *ast.BinaryExpr:
					if e.Op == token.ADD {
						join(e.X)
						join(e.Y)
					}
				case *ast.ParenExpr:
					join(e.X)
				case *ast.BasicLit:
					if e.Kind == token.STRING {
						if v, err := strconv.Unquote(e.Value); err == nil {
							sb.WriteString(v)
						}
					}
				}
			}
			join(n)
			check(sb.String())
			return false
		case *ast.BasicLit:
			if n.Kind == token.STRING {
				if sql, err := strconv.Unquote(n.Value); err == nil {
					check(sql)
				}
			}
		}
		return true
	})
	return writers, bumpers, rehashes, bad, nil
}

// TestEveryPasswordHashWriterBumpsTheEpoch covers T6-M2. A sign-in compares the
// password epoch, not the hash, so a write of password_hash that forgets to add
// one to password_epoch would leave a stale sign-in alive. The test reads every
// SQL string of the non-test code and checks each UPDATE that sets password_hash:
// it must bump the epoch, except the rehash on sign-in, which is named above. A
// new writer fails the test until it bumps the epoch or is named here on purpose.
//
// An INSERT of a user row sets the hash of a new user, whose epoch is the column
// default of 0, and is not a change of a password.
func TestEveryPasswordHashWriterBumpsTheEpoch(t *testing.T) {
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
		w, b, r, bad, err := scanEpochWriters(rel, src)
		if err != nil {
			return err
		}
		writers, bumpers, rehashes = writers+w, bumpers+b, rehashes+r
		for _, sql := range bad {
			t.Errorf("%s writes password_hash and does not add one to password_epoch:\n%s", rel, sql)
		}
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

// TestEpochScanSeesSplitAndTupleWriters: the scan fails a statement that the
// code splits across literals, and the tuple form of the update, and it passes
// the same statements when they bump the epoch.
func TestEpochScanSeesSplitAndTupleWriters(t *testing.T) {
	for name, tc := range map[string]struct {
		sql string // the Go expression of the statement
		bad bool
	}{
		"one literal, no bump":    {"`update users set password_hash = $2 where id = $1`", true},
		"one literal, bump":       {"`update users set password_hash = $2, password_epoch = password_epoch + 1 where id = $1`", false},
		"split, no bump":          {"\"update users set password_scheme = 'gobb', \" + \"password_hash = $2 where id = $1\"", true},
		"split in three, no bump": {"\"update users \" + \"set password_scheme = 'gobb', \" + \"password_hash = $2 where id = $1\"", true},
		"split, bump":             {"\"update users set password_hash = $2, \" + \"password_epoch = password_epoch + 1 where id = $1\"", false},
		"split inside the bump":   {"\"update users set password_hash = $2, password_epoch = \" + \"password_epoch + 1 where id = $1\"", false},
		"tuple, no bump":          {"`update users set (password_hash, password_scheme) = ($2, 'gobb') where id = $1`", true},
		"tuple, bump":             {"`update users set (password_hash, password_epoch) = ($2, password_epoch + 1) where id = $1`", false},
		"hash only in where":      {"`update users set last_seen_at = $2 where password_hash = $1`", false},
		"upper case":              {"`UPDATE users SET PASSWORD_HASH = $2 WHERE id = $1`", true},
	} {
		t.Run(name, func(t *testing.T) {
			src := "package p\n\nvar q = " + tc.sql + "\n"
			writers, _, _, bad, err := scanEpochWriters("internal/x/x.go", []byte(src))
			if err != nil {
				t.Fatal(err)
			}
			if tc.bad && len(bad) != 1 {
				t.Errorf("the scan did not fail %s (%d writers, %d bad)", tc.sql, writers, len(bad))
			}
			if !tc.bad && len(bad) != 0 {
				t.Errorf("the scan failed %s", tc.sql)
			}
		})
	}
}
