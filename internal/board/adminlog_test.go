package board_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// logCount returns the count of mod_log rows.
func logCount(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from mod_log`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestAdminWrite(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	rename := func(name string) func(pgx.Tx) (board.Change, error) {
		return func(tx pgx.Tx) (board.Change, error) {
			if _, err := tx.Exec(ctx, `update forums set name = $1 where id = 2`, name); err != nil {
				return board.Change{}, err
			}
			return board.Change{Action: "forum.update", TargetType: "forum", TargetID: 2, Details: map[string]any{"fields": []string{"name"}}}, nil
		}
	}
	forumName := func() string {
		var name string
		if err := pool.QueryRow(ctx, `select name from forums where id = 2`).Scan(&name); err != nil {
			t.Fatal(err)
		}
		return name
	}
	before := logCount(t, pool)

	// bob is a moderator, not an admin; a guest is neither. Neither runs the write.
	for _, v := range []board.Viewer{viewerFor(t, pool, 2), asGuest(t, pool)} {
		if err := board.AdminWrite(ctx, pool, v, t0, rename("Taken")); !errors.Is(err, board.ErrForbidden) {
			t.Errorf("user %d: %v, want ErrForbidden", v.UserID, err)
		}
	}
	if forumName() == "Taken" || logCount(t, pool) != before {
		t.Fatal("a refused write changed something")
	}

	// A failing write rolls back, and writes no row.
	boom := errors.New("boom")
	alice := viewerFor(t, pool, 1)
	err := board.AdminWrite(ctx, pool, alice, t0, func(tx pgx.Tx) (board.Change, error) {
		if _, err := rename("Half")(tx); err != nil {
			return board.Change{}, err
		}
		return board.Change{}, boom
	})
	if !errors.Is(err, boom) || forumName() == "Half" || logCount(t, pool) != before {
		t.Fatalf("a failed write: %v, name %q, rows %d", err, forumName(), logCount(t, pool))
	}

	// alice is an admin: the change and one row, with her as the actor.
	if err := board.AdminWrite(ctx, pool, alice, t0, rename("Renamed")); err != nil {
		t.Fatal(err)
	}
	if forumName() != "Renamed" {
		t.Errorf("name = %q", forumName())
	}
	var (
		actor           int64
		action, details string
		target          int64
		targetType      string
	)
	if err := pool.QueryRow(ctx, `select actor_id, action, target_type, target_id, details::text from mod_log order by id desc limit 1`).
		Scan(&actor, &action, &targetType, &target, &details); err != nil {
		t.Fatal(err)
	}
	if actor != 1 || action != "forum.update" || targetType != "forum" || target != 2 || details != `{"via": "web", "fields": ["name"]}` {
		t.Errorf("row = %d %s %s %d %s", actor, action, targetType, target, details)
	}
}

func TestModLogReader(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// A command (no actor, via cli), a web write by alice, and a moderator's lock
	// by bob, in that order.
	if err := board.ActivateUser(ctx, pool, 4, t0); err != nil {
		t.Fatal(err)
	}
	if err := board.AdminWrite(ctx, pool, viewerFor(t, pool, 1), t0, func(pgx.Tx) (board.Change, error) {
		return board.Change{Action: "forum.lock", TargetType: "forum", TargetID: 2, Details: map[string]any{"note": "x"}}, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into mod_log (actor_id, action, target_type, target_id, created_at) values (2, 'topic.lock', 'topic', 1, $1)`, t0); err != nil {
		t.Fatal(err)
	}
	actions := func(f board.LogFilter) []string {
		t.Helper()
		lp, err := board.ModLog(ctx, pool, f, 1)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, e := range lp.Entries {
			out = append(out, e.Action)
		}
		return out
	}
	all, err := board.ModLog(ctx, pool, board.LogFilter{}, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Entries) < 3 {
		t.Fatalf("log has %d rows", len(all.Entries))
	}
	// Newest first, with the names of the actor and the target.
	lock, web, cli := all.Entries[0], all.Entries[1], all.Entries[2]
	if lock.Action != "topic.lock" || lock.Actor != (board.Author{ID: 2, Name: "bob"}) || lock.TargetName == "" {
		t.Errorf("newest = %+v", lock)
	}
	if web.Via != "web" || web.TargetName != "Main Discussion" || !slices.Equal(web.Details, []board.LogDetail{{Key: "note", Value: "x"}}) {
		t.Errorf("web row = %+v", web)
	}
	if cli.Action != "user.activate" || cli.Actor.ID != 0 || cli.Via != "cli" || cli.TargetName != "dave" || len(cli.Details) != 0 {
		t.Errorf("command row = %+v", cli)
	}

	if got := actions(board.LogFilter{Action: "forum.lock"}); !slices.Equal(got, []string{"forum.lock"}) {
		t.Errorf("by action = %v", got)
	}
	if got := actions(board.LogFilter{Actor: board.LogNoActor}); !slices.Contains(got, "user.activate") || slices.Contains(got, "forum.lock") {
		t.Errorf("no actor = %v", got)
	}
	if got := actions(board.LogFilter{Actor: 2}); !slices.Equal(got, []string{"topic.lock"}) {
		t.Errorf("by bob = %v", got)
	}
	if got := actions(board.LogFilter{TargetType: "user", TargetID: 4}); !slices.Equal(got, []string{"user.activate"}) {
		t.Errorf("by target = %v", got)
	}
	if got := actions(board.LogFilter{Action: "nothing.like.this"}); len(got) != 0 {
		t.Errorf("no match = %v", got)
	}
	if _, err := board.ModLog(ctx, pool, board.LogFilter{}, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 = %v, want ErrNotFound", err)
	}

	gotActions, types, err := board.LogChoices(ctx, pool)
	if err != nil || !slices.Contains(gotActions, "forum.lock") || !slices.Contains(types, "user") || !slices.IsSorted(gotActions) {
		t.Errorf("choices = %v %v, %v", gotActions, types, err)
	}
}

func TestModLogReaderPages(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(ctx, `delete from mod_log`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into mod_log (action, target_type, target_id, created_at)
		select 'topic.lock', 'topic', g, $1 from generate_series(1, 51) g`, t0); err != nil {
		t.Fatal(err)
	}
	p1, err := board.ModLog(ctx, pool, board.LogFilter{}, 1)
	if err != nil || len(p1.Entries) != 50 || p1.Pages != 2 || p1.Entries[0].TargetID != 51 {
		t.Fatalf("page 1 = %d rows of %d pages, first %d, %v", len(p1.Entries), p1.Pages, p1.Entries[0].TargetID, err)
	}
	p2, err := board.ModLog(ctx, pool, board.LogFilter{}, 2)
	if err != nil || len(p2.Entries) != 1 || p2.Entries[0].TargetID != 1 {
		t.Fatalf("page 2 = %+v, %v", p2, err)
	}
	if _, err := pool.Exec(ctx, `delete from mod_log`); err != nil {
		t.Fatal(err)
	}
	if empty, err := board.ModLog(ctx, pool, board.LogFilter{}, 1); err != nil || len(empty.Entries) != 0 || empty.Pages != 1 {
		t.Errorf("empty log = %+v, %v", empty, err)
	}
}
