package board_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
)

type modRow struct {
	Actor      *int64
	Action     string
	TargetType string
	Target     int64
	Details    string
}

func modRows(t *testing.T, pool *pgxpool.Pool) []modRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select actor_id, action, target_type, target_id, details::text from mod_log order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []modRow
	for rows.Next() {
		var m modRow
		if err := rows.Scan(&m.Actor, &m.Action, &m.TargetType, &m.Target, &m.Details); err != nil {
			t.Fatal(err)
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedSessions gives alice two sessions and bob one.
func seedSessions(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ip := netip.MustParseAddr("10.1.2.3")
	for _, id := range []int64{alice, alice, bob} {
		if _, _, err := boardtest.Session(context.Background(), pool, id, false, ip, "t", t0); err != nil {
			t.Fatal(err)
		}
	}
}

func TestUserByName(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	for _, name := range []string{"alice", "  ALICE ", "Alice"} {
		if id, username, err := board.UserByName(ctx, pool, name); err != nil || id != alice || username != "alice" {
			t.Errorf("UserByName(%q) = %d, %q, %v, want alice", name, id, username, err)
		}
	}
	for _, name := range []string{"nobody", "", "alice\x00", "\xff\xfe"} {
		if id, username, err := board.UserByName(ctx, pool, name); !errors.Is(err, board.ErrNotFound) || id != 0 || username != "" {
			t.Errorf("UserByName(%q) = %d, %q, %v, want ErrNotFound", name, id, username, err)
		}
	}
}

func TestResetPassword(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSessions(t, pool)
	before := readUser(t, pool, alice)

	pw, err := board.ResetPassword(ctx, pool, alice, t0)
	if err != nil {
		t.Fatal(err)
	}
	if len(pw) != 16 {
		t.Errorf("password %q has %d characters, want 16", pw, len(pw))
	}
	after := readUser(t, pool, alice)
	if after.Hash == before.Hash || after.Scheme != "gobb" {
		t.Errorf("hash changed = %v, scheme = %q", after.Hash != before.Hash, after.Scheme)
	}
	if ok, _ := password.Verify(pw, after.Hash, after.Scheme); !ok {
		t.Error("the stored hash does not verify the printed password")
	}
	if ok, _ := password.Verify("example-password", after.Hash, after.Scheme); ok {
		t.Error("the old password still verifies")
	}
	if e := epochOf(t, pool, alice); e != 1 {
		t.Errorf("epoch = %d, want 1", e)
	}
	if n := sessionCount(t, pool, alice); n != 0 {
		t.Errorf("alice has %d sessions, want 0", n)
	}
	if n := sessionCount(t, pool, bob); n != 1 {
		t.Errorf("bob has %d sessions, want 1", n)
	}
	if e := epochOf(t, pool, bob); e != 0 {
		t.Errorf("bob's epoch = %d, want 0", e)
	}
	rows := modRows(t, pool)
	if len(rows) != 1 || rows[0].Actor != nil || rows[0].Action != "user.reset_password" ||
		rows[0].TargetType != "user" || rows[0].Target != alice || rows[0].Details != `{"via": "cli"}` {
		t.Errorf("mod_log = %+v", rows)
	}
	for _, r := range rows {
		if strings.Contains(r.Details, pw) {
			t.Error("the mod_log row holds the password")
		}
	}

	// A legacy account (bob) moves to the gobb scheme. A second reset gives
	// another password and adds one to the epoch again.
	pw2, err := board.ResetPassword(ctx, pool, bob, t0)
	if err != nil || pw2 == pw {
		t.Fatalf("second reset = %q, %v", pw2, err)
	}
	if u := readUser(t, pool, bob); u.Scheme != "gobb" || !strings.HasPrefix(u.Hash, "$argon2id$") {
		t.Errorf("bob's scheme = %q, hash = %.12q", u.Scheme, u.Hash)
	}
}

func TestResetPasswordUnknownMemberChangesNothing(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSessions(t, pool)
	pw, err := board.ResetPassword(ctx, pool, 9999, t0)
	if !errors.Is(err, board.ErrNotFound) || pw != "" {
		t.Errorf("ResetPassword(9999) = %q, %v, want ErrNotFound", pw, err)
	}
	if rows := modRows(t, pool); len(rows) != 0 {
		t.Errorf("mod_log = %+v", rows)
	}
	if n := sessionCount(t, pool, alice); n != 2 {
		t.Errorf("alice has %d sessions", n)
	}
}

func TestBanUser(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSessions(t, pool)

	if err := board.BanUser(ctx, pool, alice, "spam run", t0); err != nil {
		t.Fatal(err)
	}
	var (
		n        int
		reason   string
		shown    string
		ends     *time.Time
		creator  *int64
		userID   *int64
		created  time.Time
		identity string
	)
	if err := pool.QueryRow(ctx, `select count(*) from bans where kind = 'user' and user_id = $1`, alice).Scan(&n); err != nil || n != 1 {
		t.Fatalf("%d user bans of alice, %v, want 1", n, err)
	}
	if err := pool.QueryRow(ctx, `select user_id, reason, reason_shown, ends_at, created_by, created_at, kind from bans where user_id = $1`, alice).
		Scan(&userID, &reason, &shown, &ends, &creator, &created, &identity); err != nil {
		t.Fatal(err)
	}
	if reason != "spam run" || shown != "" || ends != nil || creator != nil || !created.Equal(t0) {
		t.Errorf("ban = reason %q, shown %q, ends %v, creator %v, created %v", reason, shown, ends, creator, created)
	}
	if n := sessionCount(t, pool, alice); n != 0 {
		t.Errorf("alice has %d sessions after the ban, want 0", n)
	}
	if n := sessionCount(t, pool, bob); n != 1 {
		t.Errorf("bob has %d sessions, want 1", n)
	}
	rows := modRows(t, pool)
	if len(rows) != 1 || rows[0].Actor != nil || rows[0].Action != "user.ban" || rows[0].TargetType != "user" ||
		rows[0].Target != alice || rows[0].Details != `{"via": "cli"}` {
		t.Errorf("mod_log = %+v", rows)
	}

	// A second ban adds nothing: no ban, no log row.
	if err := board.BanUser(ctx, pool, alice, "again", t0); !errors.Is(err, board.ErrAlreadyBanned) {
		t.Errorf("second ban = %v, want ErrAlreadyBanned", err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from bans where kind = 'user' and user_id = $1`, alice).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d user bans of alice after the second, %v, want 1", n, err)
	}
	if rows := modRows(t, pool); len(rows) != 1 {
		t.Errorf("%d mod_log rows after the second ban, want 1", len(rows))
	}

	// A ban that has ended does not count: a new ban goes in. A ban of another
	// kind does not count either.
	if _, err := pool.Exec(ctx, `delete from bans where user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into bans (kind, user_id, created_at, ends_at) values ('user', $1, $2, $3)`, alice, t0.Add(-time.Hour), t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into bans (kind, email, created_at) values ('email', 'alice@example.com', $1)`, t0); err != nil {
		t.Fatal(err)
	}
	if err := board.BanUser(ctx, pool, alice, "", t0); err != nil {
		t.Errorf("ban after an ended ban: %v", err)
	}
}

// TestBanUserDropsTrustedAddresses: a ban removes the addresses that the member
// used (spec 10), so an unban does not bring back trust that the member had before.
// Another member keeps the addresses. A refused ban (the member is banned
// already) writes nothing, so it removes nothing.
func TestBanUserDropsTrustedAddresses(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	count := func(id int64) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from user_ips where user_id = $1`, id).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	seed := func() {
		t.Helper()
		if _, err := pool.Exec(ctx, `
insert into user_ips (user_id, ip, last_seen_at) values
  ($1, '10.1.2.3', $3), ($1, '2001:db8::1', $3), ($2, '10.1.2.3', $3)
on conflict do nothing`, alice, bob, t0); err != nil {
			t.Fatal(err)
		}
	}
	seed()
	if a, b := count(alice), count(bob); a != 2 || b != 1 {
		t.Fatalf("fixture: %d addresses of alice and %d of bob, want 2 and 1", a, b)
	}

	if err := board.BanUser(ctx, pool, alice, "spam run", t0); err != nil {
		t.Fatal(err)
	}
	if n := count(alice); n != 0 {
		t.Errorf("alice has %d addresses after the ban, want 0", n)
	}
	if n := count(bob); n != 1 {
		t.Errorf("bob has %d addresses, want 1: a ban touches the banned member only", n)
	}

	// A second ban is refused and writes nothing.
	seed()
	if err := board.BanUser(ctx, pool, alice, "again", t0); !errors.Is(err, board.ErrAlreadyBanned) {
		t.Fatalf("second ban = %v, want ErrAlreadyBanned", err)
	}
	if n := count(alice); n != 2 {
		t.Errorf("alice has %d addresses after a refused ban, want 2: a refusal writes nothing", n)
	}

	// An unban does not bring the addresses back.
	if _, err := pool.Exec(ctx, `delete from user_ips where user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	if err := board.UnbanUser(ctx, pool, alice, t0); err != nil {
		t.Fatal(err)
	}
	if n := count(alice); n != 0 {
		t.Errorf("alice has %d addresses after the unban, want 0", n)
	}
}

func TestBanUserRefusals(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSessions(t, pool)
	if err := board.BanUser(ctx, pool, 9999, "x", t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("ban of an unknown member = %v, want ErrNotFound", err)
	}
	for _, reason := range []string{"bad \xff text", "nul \x00 byte", strings.Repeat("x", 256)} {
		var in *board.InputError
		if err := board.BanUser(ctx, pool, alice, reason, t0); !errors.As(err, &in) || in.Field != "reason" {
			t.Errorf("ban with reason %q = %v, want an input error on reason", reason, err)
		}
	}
	if err := board.BanUser(ctx, pool, alice, strings.Repeat("é", 255), t0); err != nil {
		t.Errorf("ban with a reason of 255 characters: %v", err)
	}
	// Only the last call changed anything.
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from bans where user_id = $1`, alice).Scan(&n); err != nil || n != 1 {
		t.Errorf("%d bans of alice, %v, want 1", n, err)
	}
	if rows := modRows(t, pool); len(rows) != 1 {
		t.Errorf("%d mod_log rows, want 1", len(rows))
	}
}

func TestUnbanUser(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if err := board.UnbanUser(ctx, pool, alice, t0); !errors.Is(err, board.ErrNotBanned) {
		t.Errorf("unban of a member with no ban = %v, want ErrNotBanned", err)
	}
	if rows := modRows(t, pool); len(rows) != 0 {
		t.Errorf("mod_log after a no-op unban = %+v", rows)
	}
	if err := board.BanUser(ctx, pool, alice, "r", t0); err != nil {
		t.Fatal(err)
	}
	if err := board.BanUser(ctx, pool, bob, "r", t0); err != nil {
		t.Fatal(err)
	}
	// Bans of other kinds and of other members stay.
	if _, err := pool.Exec(ctx, `insert into bans (kind, email, created_at) values ('email', 'alice@example.com', $1)`, t0); err != nil {
		t.Fatal(err)
	}
	var kept int
	if err := pool.QueryRow(ctx, `select count(*) from bans where user_id is distinct from $1`, alice).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if err := board.UnbanUser(ctx, pool, alice, t0); err != nil {
		t.Fatal(err)
	}
	var n, others int
	if err := pool.QueryRow(ctx, `select count(*) from bans where user_id = $1`, alice).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d bans of alice after the unban, %v", n, err)
	}
	if err := pool.QueryRow(ctx, `select count(*) from bans where user_id is distinct from $1`, alice).Scan(&others); err != nil || others != kept {
		t.Errorf("%d other bans after, want %d (%v)", others, kept, err)
	}
	rows := modRows(t, pool)
	if len(rows) != 3 || rows[2].Action != "user.unban" || rows[2].Actor != nil || rows[2].Target != alice ||
		rows[2].TargetType != "user" || rows[2].Details != `{"via": "cli"}` {
		t.Errorf("mod_log = %+v", rows)
	}
	if err := board.UnbanUser(ctx, pool, 9999, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("unban of an unknown member = %v, want ErrNotFound", err)
	}
	// A ban that has ended is history: the unban leaves it, and says there is no
	// ban to remove.
	if _, err := pool.Exec(ctx, `insert into bans (kind, user_id, created_at, ends_at) values ('user', $1, $2, $3)`, carol, t0.Add(-time.Hour), t0.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err := board.UnbanUser(ctx, pool, carol, t0); !errors.Is(err, board.ErrNotBanned) {
		t.Errorf("unban with only an ended ban = %v, want ErrNotBanned", err)
	}
}

func stateOf(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), `select state from users where id = $1`, id).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// An activate makes an inactive member active, once, and writes one mod_log row.
// It is not an unban: a ban stays.
func TestActivateUser(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	const dave = int64(4) // inactive in the small bundle
	if got := stateOf(t, pool, dave); got != "inactive" {
		t.Fatalf("dave starts %q, want inactive", got)
	}
	if err := board.BanUser(ctx, pool, dave, "r", t0); err != nil {
		t.Fatal(err)
	}
	bans := func() int {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from bans where user_id = $1`, dave).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := bans()
	if err := board.ActivateUser(ctx, pool, dave, t0); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, pool, dave); got != "active" {
		t.Errorf("dave after the activate = %q, want active", got)
	}
	if after := bans(); after != before || after == 0 {
		t.Errorf("%d bans of dave after the activate, want %d (unchanged, not 0)", after, before)
	}
	rows := modRows(t, pool)
	if len(rows) != 2 || rows[1].Action != "user.activate" || rows[1].Actor != nil || rows[1].Target != dave ||
		rows[1].TargetType != "user" || rows[1].Details != `{"via": "cli"}` {
		t.Errorf("mod_log = %+v", rows)
	}
	for _, id := range []int64{dave, alice} {
		if err := board.ActivateUser(ctx, pool, id, t0); !errors.Is(err, board.ErrAlreadyActive) {
			t.Errorf("activate of active member %d = %v, want ErrAlreadyActive", id, err)
		}
	}
	if rows := modRows(t, pool); len(rows) != 2 {
		t.Errorf("mod_log after two no-op activates = %+v", rows)
	}
	if err := board.ActivateUser(ctx, pool, 9999, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("activate of an unknown member = %v, want ErrNotFound", err)
	}
}

// A deactivate makes an active member inactive and ends that member's sessions,
// once. The password stays, so an activate later gives the same sign-in back.
func TestDeactivateUser(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSessions(t, pool)
	if err := board.DeactivateUser(ctx, pool, alice, t0); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, pool, alice); got != "inactive" {
		t.Errorf("alice after the deactivate = %q, want inactive", got)
	}
	if n := sessionCount(t, pool, alice); n != 0 {
		t.Errorf("alice has %d sessions after the deactivate, want 0", n)
	}
	if n := sessionCount(t, pool, bob); n != 1 {
		t.Errorf("bob has %d sessions, want 1", n)
	}
	if e := epochOf(t, pool, alice); e != 0 {
		t.Errorf("alice's password epoch = %d, want 0 (unchanged)", e)
	}
	rows := modRows(t, pool)
	if len(rows) != 1 || rows[0].Action != "user.deactivate" || rows[0].Actor != nil || rows[0].Target != alice ||
		rows[0].TargetType != "user" || rows[0].Details != `{"via": "cli"}` {
		t.Errorf("mod_log = %+v", rows)
	}
	if err := board.DeactivateUser(ctx, pool, alice, t0); !errors.Is(err, board.ErrAlreadyInactive) {
		t.Errorf("second deactivate = %v, want ErrAlreadyInactive", err)
	}
	if rows := modRows(t, pool); len(rows) != 1 {
		t.Errorf("mod_log after a no-op deactivate = %+v", rows)
	}
	if err := board.DeactivateUser(ctx, pool, 9999, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("deactivate of an unknown member = %v, want ErrNotFound", err)
	}
}

// waitForRowLockWait blocks until a statement of this test's database waits on a
// lock, or done reports that the call ended without waiting.
func waitForRowLockWait(t *testing.T, pool *pgxpool.Pool, done <-chan error) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(context.Background(), `
select exists (select 1 from pg_stat_activity
 where datname = current_database() and pid <> pg_backend_pid() and wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			return
		}
		select {
		case err := <-done:
			t.Fatalf("the call ended without waiting for the row lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the call never waited for the row lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// A sign-in that holds the user row (FOR SHARE, as session.CreateChecked does) and
// has inserted its session is ordered before a reset, a ban or a deactivate: the
// write waits for the commit, then deletes that session with the others.
func TestCLIWritesWaitForASignInAndEndItsSession(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, pool *pgxpool.Pool) error
	}{
		{"reset", func(ctx context.Context, pool *pgxpool.Pool) error {
			_, err := board.ResetPassword(ctx, pool, alice, t0)
			return err
		}},
		{"ban", func(ctx context.Context, pool *pgxpool.Pool) error {
			return board.BanUser(ctx, pool, alice, "r", t0)
		}},
		{"deactivate", func(ctx context.Context, pool *pgxpool.Pool) error {
			return board.DeactivateUser(ctx, pool, alice, t0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool, _ := boardtest.Load(t, bundletest.Small())
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			if _, err := tx.Exec(ctx, `select 1 from users where id = $1 for share`, alice); err != nil {
				t.Fatal(err)
			}
			// The session row is written inside the transaction that holds the lock.
			if _, err := tx.Exec(ctx, `insert into sessions (id, user_id, created_at, last_seen_at, expires_at, ip, user_agent, remember)
values ('test-session', $1, $2, $2, $3, '10.1.2.3', 't', false)`, alice, t0, t0.Add(time.Hour)); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() { done <- tc.run(ctx, pool) }()
			waitForRowLockWait(t, pool, done)
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if n := sessionCount(t, pool, alice); n != 0 {
				t.Errorf("alice has %d sessions after the %s, want 0", n, tc.name)
			}
		})
	}
}

// The other order: a reset, a ban or a deactivate holds the row. A sign-in that
// checks the state, its epoch and the bans (session.CreateChecked) waits, and is
// refused.
//
// The writer side here is hand-written SQL that takes the lock and writes the epoch,
// the ban or the state, as ResetPassword, BanUser and DeactivateUser do. The test proves the side of
// CreateChecked only. The web tests TestSignInInFlightDoesNotSurviveAReset and
// TestSignInInFlightDoesNotSurviveABanCommand call the real functions.
func TestCreateCheckedWaitsForTheRowLockOfACLIWrite(t *testing.T) {
	for _, tc := range []string{"reset", "ban", "deactivate"} {
		t.Run(tc, func(t *testing.T) {
			ctx := context.Background()
			pool, _ := boardtest.Load(t, bundletest.Small())
			tx, err := pool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback(ctx) }()
			// What the write does under its lock, before it commits.
			if _, err := tx.Exec(ctx, `select 1 from users where id = $1 for update`, alice); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, _, err := session.CreateChecked(ctx, pool, alice, 0, false, netip.MustParseAddr("10.1.2.3"), "t", t0)
				done <- err
			}()
			waitForRowLockWait(t, pool, done)
			switch tc {
			case "reset":
				_, err = tx.Exec(ctx, `update users set password_epoch = password_epoch + 1 where id = $1`, alice)
			case "ban":
				_, err = tx.Exec(ctx, `insert into bans (kind, user_id, created_at) values ('user', $1, $2)`, alice, t0)
			case "deactivate":
				_, err = tx.Exec(ctx, `update users set state = 'inactive' where id = $1`, alice)
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := tx.Commit(ctx); err != nil {
				t.Fatal(err)
			}
			if err := <-done; !errors.Is(err, session.ErrStale) {
				t.Errorf("sign-in after the %s = %v, want ErrStale", tc, err)
			}
			if n := sessionCount(t, pool, alice); n != 0 {
				t.Errorf("%d sessions", n)
			}
		})
	}
}
