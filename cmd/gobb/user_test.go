package main

import (
	"bytes"
	"errors"
	"net/netip"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/importer"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/session"
)

const (
	alicePassword = "example-password"
	alice         = 1
	bob           = 2
)

var (
	testIP  = netip.MustParseAddr("10.1.2.3")
	testNow = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
)

// userBoard imports the small test board and points GOBB_DSN at it.
func userBoard(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := dbtest.New(t)
	dir := bundletest.Write(t, bundletest.Small())
	if _, err := importer.Run(t.Context(), pool, dir, importer.Options{DataDir: t.TempDir()}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOBB_DSN", dsnOf(t, pool))
	return pool
}

// runUser runs gobb with args and returns the exit code, stdout and stderr.
func runUser(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var out, errOut bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = &out, &errOut
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
	code := run(t.Context(), args)
	stdout, stderr = oldOut, oldErr
	return code, out.String(), errOut.String()
}

func count(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), sql, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func seedSession(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	token, _, err := boardtest.Session(t.Context(), pool, id, false, testIP, "t", testNow)
	if err != nil {
		t.Fatal(err)
	}
	return token
}

// TestResetPassword: the printed password signs alice in and her old one fails;
// her earlier sessions are gone; one mod_log row has {"via":"cli"}; and the
// password appears nowhere but on stdout, once, as the only line there.
func TestResetPassword(t *testing.T) {
	pool := userBoard(t)
	old := seedSession(t, pool, alice)
	other := seedSession(t, pool, bob)
	svc := login.New(pool)
	svc.Floor = 0

	code, out, errOut := runUser(t, "user", "reset-password", "Alice")
	if code != 0 {
		t.Fatalf("exit = %d, want 0\nstderr: %s", code, errOut)
	}
	pw, ok := strings.CutSuffix(out, "\n")
	if !ok || strings.ContainsAny(pw, "\n ") || !regexp.MustCompile(`^[a-zA-Z0-9]{16}$`).MatchString(pw) {
		t.Fatalf("stdout = %q, want one line holding the 16-character password", out)
	}
	if id, err := svc.Check(t.Context(), "alice", pw, testIP, testNow); err != nil || id != alice {
		t.Errorf("sign-in with the printed password = %d, %v", id, err)
	}
	if _, err := svc.Check(t.Context(), "alice", alicePassword, testIP, testNow); !errors.Is(err, login.ErrWrong) {
		t.Errorf("sign-in with the old password = %v, want ErrWrong", err)
	}
	if _, err := session.Lookup(t.Context(), pool, old, testNow); !errors.Is(err, session.ErrNone) {
		t.Errorf("the old session of alice: %v, want ErrNone", err)
	}
	if _, err := session.Lookup(t.Context(), pool, other, testNow); err != nil {
		t.Errorf("the session of bob ended: %v", err)
	}
	var details, actor string
	if n := count(t, pool, `select count(*) from mod_log`); n != 1 {
		t.Fatalf("%d mod_log rows, want 1", n)
	}
	if err := pool.QueryRow(t.Context(), `select details::text, coalesce(actor_id::text, 'null') from mod_log where action = 'user.reset_password' and target_type = 'user' and target_id = $1`, alice).
		Scan(&details, &actor); err != nil {
		t.Fatal(err)
	}
	if details != `{"via": "cli"}` || actor != "null" {
		t.Errorf("mod_log row = details %s, actor %s", details, actor)
	}

	// The password is in no log line, and in no row of mod_log or bans.
	if strings.Contains(errOut, pw) {
		t.Errorf("stderr holds the password:\n%s", errOut)
	}
	var logged string
	if err := pool.QueryRow(t.Context(), `select coalesce(string_agg(m::text, ' '), '') from mod_log m`).Scan(&logged); err != nil || strings.Contains(logged, pw) {
		t.Errorf("mod_log holds the password: %v", err)
	}
}

// TestBanAndUnban: after ban, alice's live session answers as a guest, and a
// sign-in with her right password answers "banned"; after unban the sign-in
// works; two mod_log rows. Neither command writes to stdout.
func TestBanAndUnban(t *testing.T) {
	pool := userBoard(t)
	live := seedSession(t, pool, alice)
	svc := login.New(pool)
	svc.Floor = 0

	code, out, errOut := runUser(t, "user", "ban", "alice", "-reason", "spam run")
	if code != 0 || out != "" {
		t.Fatalf("ban = exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if _, err := session.Lookup(t.Context(), pool, live, testNow); !errors.Is(err, session.ErrNone) {
		t.Errorf("the live session of alice after the ban: %v, want ErrNone (a guest)", err)
	}
	var banned *login.BannedError
	if _, err := svc.Check(t.Context(), "alice", alicePassword, testIP, testNow); !errors.As(err, &banned) {
		t.Errorf("sign-in of a banned member = %v, want *BannedError", err)
	}
	var reason string
	if err := pool.QueryRow(t.Context(), `select reason from bans where kind = 'user' and user_id = $1`, alice).Scan(&reason); err != nil || reason != "spam run" {
		t.Errorf("ban reason = %q, %v", reason, err)
	}

	code, out, errOut = runUser(t, "user", "unban", "alice")
	if code != 0 || out != "" {
		t.Fatalf("unban = exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if id, err := svc.Check(t.Context(), "alice", alicePassword, testIP, testNow); err != nil || id != alice {
		t.Errorf("sign-in after the unban = %d, %v", id, err)
	}
	rows, err := pool.Query(t.Context(), `select action, details::text, actor_id is null from mod_log order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var action, details string
		var nullActor bool
		if err := rows.Scan(&action, &details, &nullActor); err != nil || !nullActor {
			t.Fatalf("row: %v, null actor %v", err, nullActor)
		}
		got = append(got, action+" "+details)
	}
	if want := []string{`user.ban {"via": "cli"}`, `user.unban {"via": "cli"}`}; strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("mod_log = %q, want %q", got, want)
	}
}

// The flag may come before or after the name.
func TestBanFlagOrder(t *testing.T) {
	pool := userBoard(t)
	if code, _, errOut := runUser(t, "user", "ban", "-reason", "first", "alice"); code != 0 {
		t.Fatalf("flag first: exit %d\n%s", code, errOut)
	}
	if code, _, errOut := runUser(t, "user", "ban", "bob", "-reason", "second"); code != 0 {
		t.Fatalf("flag last: exit %d\n%s", code, errOut)
	}
	for id, want := range map[int]string{alice: "first", bob: "second"} {
		var got string
		if err := pool.QueryRow(t.Context(), `select reason from bans where user_id = $1`, id).Scan(&got); err != nil || got != want {
			t.Errorf("reason of %d = %q, %v, want %q", id, got, err, want)
		}
	}
}

// A ban of a banned member adds no second ban, and an unban of a member with no
// ban removes nothing. Both exit 0, say so on stderr, and write no mod_log row.
func TestBanTwiceAndUnbanWithoutBan(t *testing.T) {
	pool := userBoard(t)
	code, out, errOut := runUser(t, "user", "unban", "alice")
	if code != 0 || out != "" || !strings.Contains(errOut, "no ban") {
		t.Errorf("unban without a ban = exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if n := count(t, pool, `select count(*) from mod_log`); n != 0 {
		t.Errorf("%d mod_log rows after a no-op unban", n)
	}
	if code, _, _ := runUser(t, "user", "ban", "alice"); code != 0 {
		t.Fatal("first ban failed")
	}
	code, out, errOut = runUser(t, "user", "ban", "alice", "-reason", "again")
	if code != 0 || out != "" || !strings.Contains(errOut, "already banned") {
		t.Errorf("second ban = exit %d, stdout %q, stderr %q", code, out, errOut)
	}
	if n := count(t, pool, `select count(*) from bans where kind = 'user' and user_id = $1`, alice); n != 1 {
		t.Errorf("%d user bans of alice, want 1", n)
	}
	if n := count(t, pool, `select count(*) from mod_log`); n != 1 {
		t.Errorf("%d mod_log rows, want 1 (the first ban)", n)
	}
}

// An unknown name exits 1 with the message on stderr, nothing on stdout, and
// no change.
func TestUserCommandUnknownName(t *testing.T) {
	pool := userBoard(t)
	seedSession(t, pool, alice)
	snapshot := func() string {
		var s string
		err := pool.QueryRow(t.Context(), `select
 (select string_agg(password_hash || password_epoch::text, ',' order by id) from users) || '|' ||
 (select count(*) from sessions) || '|' || (select count(*) from bans) || '|' || (select count(*) from mod_log)`).Scan(&s)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	before := snapshot()
	for _, args := range [][]string{
		{"user", "reset-password", "nobody"},
		{"user", "ban", "nobody", "-reason", "x"},
		{"user", "unban", "nobody"},
	} {
		code, out, errOut := runUser(t, args...)
		if code != 1 || out != "" || !strings.Contains(errOut, "no member named nobody") {
			t.Errorf("%v = exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
		if after := snapshot(); after != before {
			t.Errorf("%v changed the database", args)
		}
	}
}

// A usage error exits 2, writes nothing to stdout, and needs no database.
func TestUserCommandUsage(t *testing.T) {
	t.Setenv("GOBB_DSN", "")
	for _, args := range [][]string{
		{"user"},
		{"user", "bogus"},
		{"user", "reset-password"},
		{"user", "reset-password", "a", "b"},
		{"user", "reset-password", "-x", "a"},
		{"user", "reset-password", ""},
		{"user", "reset-password", "  "},
		{"user", "ban"},
		{"user", "ban", "a", "-bogus"},
		{"user", "ban", "a", "-reason"},
		{"user", "ban", "a", "b"},
		{"user", "unban"},
		{"user", "unban", "a", "b"},
		{"user", "unban", "a", "-reason", "x"},
	} {
		code, out, errOut := runUser(t, args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "usage:") {
			t.Errorf("%q = exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

// A database that cannot do the work exits 1, prints nothing on stdout, and
// puts the failure on stderr.
func TestUserCommandDatabaseFailure(t *testing.T) {
	pool := dbtest.NewEmpty(t) // no schema
	t.Setenv("GOBB_DSN", dsnOf(t, pool))
	for _, args := range [][]string{
		{"user", "reset-password", "alice"},
		{"user", "ban", "alice"},
		{"user", "unban", "alice"},
	} {
		code, out, errOut := runUser(t, args...)
		if code != 1 || out != "" || errOut == "" {
			t.Errorf("%v = exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
	// And a missing GOBB_DSN.
	t.Setenv("GOBB_DSN", "")
	if code, out, errOut := runUser(t, "user", "unban", "alice"); code != 1 || out != "" || errOut == "" {
		t.Errorf("no GOBB_DSN = exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// recordingWriter keeps what it was given and then fails.
type recordingWriter struct{ got bytes.Buffer }

func (w *recordingWriter) Write(p []byte) (int, error) {
	w.got.Write(p)
	return 0, errors.New("stdout is closed")
}

// When stdout cannot take the password, the password is changed and lost: the
// command exits 1 and says so on stderr, and the message does not hold the
// password.
func TestResetPasswordWhenStdoutFails(t *testing.T) {
	pool := userBoard(t)
	w := &recordingWriter{}
	var errOut bytes.Buffer
	oldOut, oldErr := stdout, stderr
	stdout, stderr = w, &errOut
	t.Cleanup(func() { stdout, stderr = oldOut, oldErr })
	code := run(t.Context(), []string{"user", "reset-password", "alice"})
	if code != 1 {
		t.Fatalf("exit = %d, want 1", code)
	}
	pw := strings.TrimSpace(w.got.String())
	if len(pw) != 16 {
		t.Fatalf("the command offered %q to stdout", w.got.String())
	}
	if strings.Contains(errOut.String(), pw) || !strings.Contains(errOut.String(), "could not be printed") {
		t.Errorf("stderr = %q", errOut.String())
	}
	// The change did happen: the old password is gone.
	svc := login.New(pool)
	svc.Floor = 0
	if _, err := svc.Check(t.Context(), "alice", alicePassword, testIP, testNow); !errors.Is(err, login.ErrWrong) {
		t.Errorf("sign-in with the old password = %v, want ErrWrong", err)
	}
}

// TestUserCommandsLogTheMatchedMember covers T6-M3. The name that the administrator
// types is folded (case, width, invisible characters), so the member it reaches
// may not be the one in mind. Each command logs the stored username and the ID on
// stderr, so the administrator sees a wrong match at once.
func TestUserCommandsLogTheMatchedMember(t *testing.T) {
	userBoard(t)
	for _, args := range [][]string{
		{"user", "ban", "  ALICE ", "-reason", "x"},
		{"user", "ban", "ALICE"},             // already banned: a no-op
		{"user", "unban", "Alice"},           // lifts the ban
		{"user", "unban", "alice"},           // no ban: a no-op
		{"user", "reset-password", "a​lice"}, // a zero-width space is folded away
	} {
		code, _, errOut := runUser(t, args...)
		if code != 0 {
			t.Fatalf("%v: exit %d\n%s", args, code, errOut)
		}
		if !strings.Contains(errOut, "username=alice") || !strings.Contains(errOut, "user=1") {
			t.Errorf("%v: stderr = %q, want the matched username=alice and user=1", args, errOut)
		}
	}
}
