package main

import (
	"strconv"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/dbtest"
)

// banID returns the ID of the one ban that matches where.
func banID(t *testing.T, pool *pgxpool.Pool, where string, args ...any) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `select id from bans where `+where, args...).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// seedUserBan adds a user ban of alice that ends in 2099, created now.
func seedUserBan(t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var id int64
	if err := pool.QueryRow(t.Context(), `
insert into bans (kind, user_id, created_at, ends_at)
values ('user', $1, now(), '2099-01-02T03:04:05Z') returning id`, alice).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

// fields splits out into lines and each line into its fields.
func fields(out string) [][]string {
	var rows [][]string
	for line := range strings.SplitSeq(strings.TrimSuffix(out, "\n"), "\n") {
		rows = append(rows, strings.Fields(line))
	}
	return rows
}

// TestBanList: the list shows the bans in force of every kind, newest first,
// with ID, kind, target and end; the import's ended user ban is not in it. The
// list writes nothing to the database.
func TestBanList(t *testing.T) {
	pool := userBoard(t)
	user := seedUserBan(t, pool)
	email := banID(t, pool, `kind = 'email'`)
	ip := banID(t, pool, `kind = 'ip'`)
	ended := banID(t, pool, `kind = 'user' and user_id = 4`)

	code, out, errOut := runUser(t, "ban", "list")
	if code != 0 {
		t.Fatalf("exit = %d\nstderr: %s", code, errOut)
	}
	got := fields(out)
	want := [][]string{
		{"ID", "KIND", "TARGET", "ENDS"},
		{strconv.FormatInt(user, 10), "user", "alice", "2099-01-02T03:04:05Z"},
		{strconv.FormatInt(ip, 10), "ip", "10.99.0.0/16", "never"},
		{strconv.FormatInt(email, 10), "email", "spammer@example.com", "never"},
	}
	if len(got) != len(want) {
		t.Fatalf("stdout =\n%s", out)
	}
	for i := range want {
		if strings.Join(got[i], " ") != strings.Join(want[i], " ") {
			t.Errorf("line %d = %q, want %q", i, got[i], want[i])
		}
	}
	for _, row := range got[1:] {
		if row[0] == strconv.FormatInt(ended, 10) {
			t.Errorf("the ended ban %d is in the list", ended)
		}
	}
	if n := count(t, pool, `select count(*) from mod_log`); n != 0 {
		t.Errorf("%d mod_log rows after a list", n)
	}
}

// TestBanLift: a lift of each kind removes the ban, writes nothing to stdout,
// and writes one mod_log row: ban.lift, target type ban, the ID of the ban, a
// null actor and the kind and target with "via": "cli". A second lift of the
// same ban exits 1 and writes no row. With every ban lifted, the list says so
// on stderr and prints nothing.
func TestBanLift(t *testing.T) {
	pool := userBoard(t)
	user := seedUserBan(t, pool)
	for i, c := range []struct {
		id      int64
		details string
	}{
		{user, `{"via": "cli", "kind": "user", "target": "alice"}`},
		{banID(t, pool, `kind = 'email'`), `{"via": "cli", "kind": "email", "target": "spammer@example.com"}`},
		{banID(t, pool, `kind = 'ip'`), `{"via": "cli", "kind": "ip", "target": "10.99.0.0/16"}`},
	} {
		arg := strconv.FormatInt(c.id, 10)
		code, out, errOut := runUser(t, "ban", "lift", arg)
		if code != 0 || out != "" || !strings.Contains(errOut, "ban lifted") {
			t.Fatalf("lift %d = exit %d, stdout %q, stderr %q", c.id, code, out, errOut)
		}
		if n := count(t, pool, `select count(*) from bans where id = $1`, c.id); n != 0 {
			t.Errorf("ban %d is still there", c.id)
		}
		if n := count(t, pool, `select count(*) from mod_log`); n != i+1 {
			t.Fatalf("%d mod_log rows, want %d", n, i+1)
		}
		var action, targetType, details string
		var target int64
		var nullActor bool
		if err := pool.QueryRow(t.Context(), `select action, target_type, target_id, details::text, actor_id is null from mod_log order by id desc limit 1`).
			Scan(&action, &targetType, &target, &details, &nullActor); err != nil {
			t.Fatal(err)
		}
		if action != "ban.lift" || targetType != "ban" || target != c.id || details != c.details || !nullActor {
			t.Errorf("mod_log row = %s %s %d %s, null actor %v; want details %s", action, targetType, target, details, nullActor, c.details)
		}

		code, out, errOut = runUser(t, "ban", "lift", arg)
		if code != 1 || out != "" || !strings.Contains(errOut, "no ban in force with ID "+arg) {
			t.Errorf("second lift %d = exit %d, stdout %q, stderr %q", c.id, code, out, errOut)
		}
		if n := count(t, pool, `select count(*) from mod_log`); n != i+1 {
			t.Errorf("a refused lift wrote a row: %d rows", n)
		}
	}
	code, out, errOut := runUser(t, "ban", "list")
	if code != 0 || out != "" || !strings.Contains(errOut, "no bans in force") {
		t.Errorf("empty list = exit %d, stdout %q, stderr %q", code, out, errOut)
	}
}

// An unknown ID, and the ID of a ban that has ended, exit 1 with the message on
// stderr, nothing on stdout, and no change.
func TestBanLiftUnknownID(t *testing.T) {
	pool := userBoard(t)
	ended := banID(t, pool, `kind = 'user' and user_id = 4`)
	for _, id := range []int64{99999, ended} {
		arg := strconv.FormatInt(id, 10)
		code, out, errOut := runUser(t, "ban", "lift", arg)
		if code != 1 || out != "" || !strings.Contains(errOut, "no ban in force with ID "+arg) {
			t.Errorf("lift %d = exit %d, stdout %q, stderr %q", id, code, out, errOut)
		}
	}
	if n := count(t, pool, `select count(*) from bans`); n != 3 {
		t.Errorf("%d bans, want the 3 of the import", n)
	}
	if n := count(t, pool, `select count(*) from mod_log`); n != 0 {
		t.Errorf("%d mod_log rows, want 0", n)
	}
}

// A usage error exits 2, writes nothing to stdout, and needs no database.
func TestBanCommandUsage(t *testing.T) {
	t.Setenv("GOBB_DSN", "")
	for _, args := range [][]string{
		{"ban"},
		{"ban", "bogus"},
		{"ban", "list", "x"},
		{"ban", "lift"},
		{"ban", "lift", "0"},
		{"ban", "lift", "-1"},
		{"ban", "lift", "abc"},
		{"ban", "lift", "1", "2"},
	} {
		code, out, errOut := runUser(t, args...)
		if code != 2 || out != "" || !strings.Contains(errOut, "usage:") {
			t.Errorf("%q = exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}

// A database that cannot do the work exits 1, prints nothing on stdout, and
// puts the failure on stderr.
func TestBanCommandDatabaseFailure(t *testing.T) {
	pool := dbtest.NewEmpty(t) // no schema
	t.Setenv("GOBB_DSN", dsnOf(t, pool))
	for _, args := range [][]string{{"ban", "list"}, {"ban", "lift", "1"}} {
		code, out, errOut := runUser(t, args...)
		if code != 1 || out != "" || errOut == "" {
			t.Errorf("%v = exit %d, stdout %q, stderr %q", args, code, out, errOut)
		}
	}
}
