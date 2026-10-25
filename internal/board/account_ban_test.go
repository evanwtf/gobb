package board_test

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// TestAccountChangesRefuseABan covers T6-M1: a password or address change that
// was in flight when a ban landed changes nothing and answers 403.
func TestAccountChangesRefuseABan(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	before := readUser(t, pool, alice)
	epoch := epochOf(t, pool, alice)
	if _, _, err := boardtest.Session(ctx, pool, alice, false, ip1, "t", t0); err != nil {
		t.Fatal(err)
	}
	// The ban is written straight to the table: it ends no session, as a ban that
	// landed after the check of the current password would not.
	if _, err := pool.Exec(ctx, `insert into bans (kind, user_id, created_at) values ('user', $1, $2)`, alice, t0); err != nil {
		t.Fatal(err)
	}
	_, err := board.ChangePasswordWith(ctx, pool, a, "a brand new password", epoch, nil, t0.Add(time.Minute))
	wantIs(t, err, board.ErrForbidden, "password change of a banned member")
	wantIs(t, err, board.ErrBanned, "password change of a banned member")
	err = board.ChangeEmailWith(ctx, pool, a, "alice.new@example.org", epoch, t0.Add(time.Minute))
	wantIs(t, err, board.ErrForbidden, "email change of a banned member")
	wantIs(t, err, board.ErrBanned, "email change of a banned member")
	if after := readUser(t, pool, alice); after.Hash != before.Hash || after.Scheme != before.Scheme || after.Email != before.Email ||
		after.EmailClean != before.EmailClean || after.Verified != before.Verified {
		t.Errorf("the account changed: %+v, before %+v", after, before)
	}
	if got := epochOf(t, pool, alice); got != epoch {
		t.Errorf("epoch = %d, want %d", got, epoch)
	}
	if n := sessionCount(t, pool, alice); n != 1 {
		t.Errorf("%d sessions, want the one that the refused change left", n)
	}
	// A ban that has ended does not stop a change.
	if _, err := pool.Exec(ctx, `update bans set ends_at = $1`, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := board.ChangePasswordWith(ctx, pool, a, "a brand new password", epoch, nil, t0.Add(time.Minute)); err != nil {
		t.Errorf("password change after the ban ended: %v", err)
	}
}

// TestNoPublicUncheckedAccountWrites covers T6-C4: board.ChangePassword and
// board.ChangeEmail check no current password, so they are not part of the
// package API. The tests reach them through export_test.go. The test fails when
// a non-test file declares either name again.
func TestNoPublicUncheckedAccountWrites(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		src, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		f, err := parser.ParseFile(fset, name, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range f.Decls {
			if fn, ok := d.(*ast.FuncDecl); ok && fn.Recv == nil && (fn.Name.Name == "ChangePassword" || fn.Name.Name == "ChangeEmail") {
				t.Errorf("%s declares %s: an unchecked account write must stay out of the API", name, fn.Name.Name)
			}
		}
	}
}
