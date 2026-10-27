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
	"github.com/evanwtf/gobb/internal/password"
)

// lastLog returns the actor, the action, the target and the details of the
// newest mod_log row.
func lastLog(t *testing.T, pool *pgxpool.Pool) (actor int64, action string, target int64, details string) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `select coalesce(actor_id, 0), action, target_id, details::text from mod_log order by id desc limit 1`).
		Scan(&actor, &action, &target, &details); err != nil {
		t.Fatal(err)
	}
	return actor, action, target, details
}

func mustExec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), sql, args...); err != nil {
		t.Fatal(err)
	}
}

func TestFindMembers(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	names := func(query string) []string {
		t.Helper()
		ms, err := board.FindMembers(ctx, pool, query, 1, t0)
		if err != nil {
			t.Fatal(err)
		}
		var out []string
		for _, m := range ms.Rows {
			out = append(out, m.Name)
		}
		return out
	}
	// Every member, the inactive one too, by name.
	if got := names(""); !slices.Equal(got, []string{"alice", "bob", "carol", "dave"}) {
		t.Errorf("all = %v", got)
	}
	for query, want := range map[string][]string{
		"al": {"alice"}, "  ALI ": {"alice"}, "dave": {"dave"}, "bob@": {"bob"}, "Carol@Example": {"carol"},
		"nobody": nil, "%": nil, "_": nil, "\x00": nil, "\xff": nil,
	} {
		if got := names(query); !slices.Equal(got, want) {
			t.Errorf("%q = %v, want %v", query, got, want)
		}
	}
	ms, err := board.FindMembers(ctx, pool, "dave", 1, t0)
	if err != nil || len(ms.Rows) != 1 || ms.Rows[0].Active {
		t.Errorf("dave = %+v, %v", ms, err)
	}
	if _, err := board.FindMembers(ctx, pool, "", 2, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 = %v", err)
	}
}

func TestAdminMember(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice, err := board.AdminMember(ctx, pool, 1, t0)
	if err != nil || !alice.IsAdmin || !alice.Owner || !alice.EmailVerified || alice.RankID != 1 || len(alice.Ranks) != 1 {
		t.Errorf("alice = %+v, %v", alice, err)
	}
	if bob, err := board.AdminMember(ctx, pool, 2, t0); err != nil || bob.IsAdmin || bob.Owner {
		t.Errorf("bob = %+v, %v", bob, err)
	}
	if _, err := board.AdminMember(ctx, pool, 999, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("999 = %v", err)
	}
}

func TestAdminResetPassword(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	if _, err := board.AdminResetPassword(ctx, pool, alice, 1, t0); !errors.Is(err, board.ErrSelf) {
		t.Errorf("self = %v", err)
	}
	if _, err := board.AdminResetPassword(ctx, pool, viewerFor(t, pool, 2), 3, t0); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("by bob = %v", err)
	}
	if _, err := board.AdminResetPassword(ctx, pool, alice, 999, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("999 = %v", err)
	}
	mustExec(t, pool, `insert into sessions (id, user_id, created_at, last_seen_at, expires_at, ip) values ('s-bob', 2, $1, $1, $1::timestamptz + interval '1 day', '192.0.2.1')`, t0)
	pw, err := board.AdminResetPassword(ctx, pool, alice, 2, t0)
	if err != nil || len(pw) != 16 {
		t.Fatalf("reset bob = %q, %v", pw, err)
	}
	var hash string
	var sessions int
	if err := pool.QueryRow(ctx, `select password_hash, (select count(*) from sessions where user_id = 2) from users where id = 2`).Scan(&hash, &sessions); err != nil {
		t.Fatal(err)
	}
	if ok, _ := password.Verify(pw, hash, "gobb"); !ok || sessions != 0 {
		t.Errorf("the new password does not verify (%v), or %d sessions remain", ok, sessions)
	}
	if actor, action, target, details := lastLog(t, pool); actor != 1 || action != "user.reset_password" || target != 2 || details != `{"via": "web"}` {
		t.Errorf("row = %d %s %d %s", actor, action, target, details)
	}
	// bob as an admin cannot reset the owner's password: he would see it.
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 0, 'admin', 3)`)
	if _, err := board.AdminResetPassword(ctx, pool, viewerFor(t, pool, 2), 1, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("bob resets the owner = %v", err)
	}
}

func TestAdminSetActive(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	if err := board.AdminSetActive(ctx, pool, alice, 1, false, t0); !errors.Is(err, board.ErrSelf) {
		t.Errorf("self = %v", err)
	}
	if err := board.AdminSetActive(ctx, pool, alice, 4, false, t0); !errors.Is(err, board.ErrAlreadyInactive) {
		t.Errorf("dave inactive again = %v", err)
	}
	if err := board.AdminSetActive(ctx, pool, alice, 4, true, t0); err != nil {
		t.Fatal(err)
	}
	if actor, action, target, _ := lastLog(t, pool); actor != 1 || action != "user.activate" || target != 4 {
		t.Errorf("row = %d %s %d", actor, action, target)
	}
	if err := board.AdminSetActive(ctx, pool, alice, 3, false, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, target, _ := lastLog(t, pool); action != "user.deactivate" || target != 3 {
		t.Errorf("row = %s %d", action, target)
	}
	// bob as an admin cannot deactivate the owner.
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 0, 'admin', 3)`)
	if err := board.AdminSetActive(ctx, pool, viewerFor(t, pool, 2), 1, false, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("bob deactivates the owner = %v", err)
	}
}

// TestAdminSetActiveKeepsAnAdmin: a deactivate that would leave no active admin
// is refused and changes nothing. Here alice's right ends after her page was
// built, so after she deactivates bob, the only other admin, no admin remains.
func TestAdminSetActiveKeepsAnAdmin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 0, 'admin', 3)`)
	alice := viewerFor(t, pool, 1)
	mustExec(t, pool, `update users set is_owner = false where id = 1`)
	mustExec(t, pool, `delete from group_members where user_id = 1 and group_id = 4`)
	before := logCount(t, pool)
	if err := board.AdminSetActive(ctx, pool, alice, 2, false, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Fatalf("deactivate the last admin = %v", err)
	}
	var state string
	if err := pool.QueryRow(ctx, `select state from users where id = 2`).Scan(&state); err != nil || state != "active" || logCount(t, pool) != before {
		t.Errorf("bob is %s, rows %d -> %d", state, before, logCount(t, pool))
	}
}

func TestEnsureAdminRemains(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	check := func(setup string) error {
		t.Helper()
		var got error
		err := pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
			if err := board.LockAdminSet(ctx, tx); err != nil {
				return err
			}
			if setup != "" {
				if _, err := tx.Exec(ctx, setup); err != nil {
					return err
				}
			}
			got = board.EnsureAdminRemains(ctx, tx)
			return errors.New("roll back")
		})
		if err == nil || err.Error() != "roll back" {
			t.Fatal(err)
		}
		return got
	}
	for setup, want := range map[string]error{
		"": nil,
		// alice is an owner: an owner holds admin over a Never.
		`insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'admin', 2)`:                                                 nil,
		`update users set is_owner = false where id = 1`:                                                                                             nil,
		`update users set is_owner = false where id = 1; insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'admin', 2)`: board.ErrLastAdmin,
		`update users set state = 'inactive'`:                                                                                                        board.ErrLastAdmin,
		// A forum grant of admin is not board-wide.
		`update users set is_owner = false where id = 1; delete from group_members where group_id = 4; insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'admin', 3)`: board.ErrLastAdmin,
		// A pending member of the admins group does not count.
		`update users set is_owner = false where id = 1; update group_members set is_pending = true where group_id = 4`: board.ErrLastAdmin,
	} {
		if got := check(setup); !errors.Is(got, want) && got != want {
			t.Errorf("%s: %v, want %v", setup, got, want)
		}
	}
}

func TestAdminSetRankAndVerify(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	if err := board.AdminSetRank(ctx, pool, alice, 3, 1, t0); err != nil {
		t.Fatal(err)
	}
	if actor, action, target, details := lastLog(t, pool); actor != 1 || action != "user.rank" || target != 3 || details != `{"via": "web", "rank": 1}` {
		t.Errorf("row = %d %s %d %s", actor, action, target, details)
	}
	if err := board.AdminSetRank(ctx, pool, alice, 3, 1, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("same rank = %v", err)
	}
	var in *board.InputError
	if err := board.AdminSetRank(ctx, pool, alice, 3, 999, t0); !errors.As(err, &in) || in.Field != "rank" {
		t.Errorf("no such rank = %v", err)
	}
	if err := board.AdminSetRank(ctx, pool, alice, 3, 0, t0); err != nil {
		t.Errorf("back to the post count = %v", err)
	}
	if err := board.AdminVerifyEmail(ctx, pool, alice, 3, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, target, _ := lastLog(t, pool); action != "user.verify_email" || target != 3 {
		t.Errorf("row = %s %d", action, target)
	}
	if err := board.AdminVerifyEmail(ctx, pool, alice, 3, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("verified again = %v", err)
	}
	if err := board.AdminVerifyEmail(ctx, pool, viewerFor(t, pool, 2), 3, t0); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("by bob = %v", err)
	}
}
