package main

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/batch"
	"github.com/evanwtf/gobb/internal/board"
)

// epoch reads the page cache epoch.
func epoch(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	v, err := board.PageEpoch(t.Context(), pool)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

// Every command that writes the board from outside the server bumps the epoch,
// so a running server empties its guest page cache. A command that changes
// nothing, and a read, leave it.
func TestCommandsBumpThePageEpoch(t *testing.T) {
	pool := userBoard(t)
	for _, c := range []struct {
		args []string
		bump bool
	}{
		{[]string{"user", "ban", "bob"}, true},
		{[]string{"user", "ban", "bob"}, false}, // already banned
		{[]string{"user", "unban", "bob"}, true},
		{[]string{"user", "deactivate", "carol"}, true},
		{[]string{"user", "activate", "carol"}, true},
		{[]string{"user", "reset-password", "carol"}, true},
		{[]string{"ban", "list"}, false},
		{[]string{"ban", "lift", "2"}, true},
		{[]string{"user", "ban", "nobody"}, false},
	} {
		before := epoch(t, pool)
		code, _, errOut := runUser(t, c.args...)
		after := epoch(t, pool)
		if c.bump && (code != 0 || after == before) {
			t.Errorf("%v: exit %d, epoch %d then %d; want a new epoch\n%s", c.args, code, before, after, errOut)
		}
		if !c.bump && after != before {
			t.Errorf("%v: the epoch changed", c.args)
		}
	}
}

// The last flush at shutdown writes what the batched writes hold.
func TestFinalFlush(t *testing.T) {
	pool := userBoard(t)
	var before int
	if err := pool.QueryRow(t.Context(), `select view_count from topics where id = 1`).Scan(&before); err != nil {
		t.Fatal(err)
	}
	w := batch.New()
	w.View(1)
	finalFlush(w, pool)
	var after int
	if err := pool.QueryRow(t.Context(), `select view_count from topics where id = 1`).Scan(&after); err != nil {
		t.Fatal(err)
	}
	if after != before+1 {
		t.Errorf("view_count = %d, want %d", after, before+1)
	}
}
