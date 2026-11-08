package db_test

import (
	"context"
	"io/fs"
	"os"
	"slices"
	"testing"
	"testing/fstest"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
)

// migrationsBefore returns the migration files that sort before name, read
// from the source tree.
func migrationsBefore(t *testing.T, name string) fstest.MapFS {
	t.Helper()
	dir := os.DirFS("migrations")
	files, err := fs.Glob(dir, "*.sql")
	if err != nil {
		t.Fatal(err)
	}
	out := fstest.MapFS{}
	for _, f := range files {
		if f >= name {
			continue
		}
		b, err := fs.ReadFile(dir, f)
		if err != nil {
			t.Fatal(err)
		}
		out[f] = &fstest.MapFile{Data: b}
	}
	return out
}

// TestMigrationModPinCopiesModLock applies the migrations before 0017, stores
// mod.lock settings of every holder kind and scope, and applies 0017. Each
// mod.lock setting gets a mod.pin setting with the same holder, forum and value,
// and nothing else gets one.
func TestMigrationModPinCopiesModLock(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	if _, err := db.MigrateFS(ctx, pool, migrationsBefore(t, db.ModPinMigration)); err != nil {
		t.Fatal(err)
	}
	exec := func(q string) {
		t.Helper()
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	insertUser(ctx, t, pool, 1, "alpha")
	insertUser(ctx, t, pool, 2, "beta")
	exec(`insert into groups (id, name, kind) values (1, 'Crew', 'open'), (2, 'Others', 'open')`)
	exec(`insert into acl_roles (id, name, kind) values (1, 'Mods', 'mod'), (2, 'Readers', 'forum'), (3, 'Owns pin', 'mod')`)
	// Role 1 locks; role 2 never locks; role 3 holds mod.pin No already and keeps it.
	exec(`insert into acl_role_settings (role_id, option, setting) values
		(1, 'mod.lock', 3), (1, 'mod.edit', 3),
		(2, 'mod.lock', 2), (2, 'forum.read', 3),
		(3, 'mod.lock', 3), (3, 'mod.pin', 1)`)
	// Group 1: mod.lock Yes in forum 2 and board-wide. Group 2: a role and another option only.
	exec(`insert into acl_group_grants (group_id, forum_id, option, setting) values
		(1, 2, 'mod.lock', 3), (1, 0, 'mod.lock', 3), (2, 2, 'mod.edit', 3)`)
	exec(`insert into acl_group_grants (group_id, forum_id, role_id) values (2, 2, 1)`)
	// User 1: Yes in forum 5, Never in forum 6. User 2: mod.lock and a mod.pin of
	// its own in forum 7, which the copy leaves alone.
	exec(`insert into acl_user_grants (user_id, forum_id, option, setting) values
		(1, 5, 'mod.lock', 3), (1, 6, 'mod.lock', 2),
		(2, 7, 'mod.lock', 3), (2, 7, 'mod.pin', 1)`)

	applied, err := db.MigrateFS(ctx, pool, os.DirFS("migrations"))
	if err != nil {
		t.Fatal(err)
	}
	// Later migrations apply after 0017; the checks below read only what 0017
	// wrote.
	if len(applied) == 0 || applied[0] != db.ModPinMigration {
		t.Fatalf("applied %v, want %s first", applied, db.ModPinMigration)
	}

	rows := func(q string) []string {
		t.Helper()
		r, err := pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []string
		for r.Next() {
			var s string
			if err := r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		if err := r.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, c := range []struct {
		q    string
		want []string
	}{
		{`select role_id || ':' || setting from acl_role_settings where option = 'mod.pin' order by role_id`,
			[]string{"1:3", "2:2", "3:1"}},
		{`select group_id || '/' || forum_id || ':' || setting from acl_group_grants where option = 'mod.pin' order by group_id, forum_id`,
			[]string{"1/0:3", "1/2:3"}},
		{`select user_id || '/' || forum_id || ':' || setting from acl_user_grants where option = 'mod.pin' order by user_id, forum_id`,
			[]string{"1/5:3", "1/6:2", "2/7:1"}},
		// The role grant of group 2 is not an option row, and stays the only row.
		{`select count(*)::text from acl_group_grants where group_id = 2`, []string{"2"}},
	} {
		if got := rows(c.q); !slices.Equal(got, c.want) {
			t.Errorf("%s\n got %v, want %v", c.q, got, c.want)
		}
	}
}

// TestDeriveModPin: on a migrated database, DeriveModPin runs the migration's
// own SQL when no row names mod.pin, and does nothing once one does.
func TestDeriveModPin(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	insertUser(ctx, t, pool, 1, "alpha")
	for _, q := range []string{
		`insert into groups (id, name, kind) values (1, 'Crew', 'open')`,
		`insert into acl_roles (id, name, kind) values (1, 'Mods', 'mod')`,
		`insert into acl_role_settings (role_id, option, setting) values (1, 'mod.lock', 3)`,
		`insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 2, 'mod.lock', 3)`,
		`insert into acl_user_grants (user_id, forum_id, option, setting) values (1, 0, 'mod.lock', 2)`,
	} {
		if _, err := pool.Exec(ctx, q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	derived, err := db.DeriveModPin(ctx, pool)
	if err != nil || !derived {
		t.Fatalf("DeriveModPin = %v, %v; want true, nil", derived, err)
	}
	q := `select 'role ' || role_id || ':' || setting from acl_role_settings where option = 'mod.pin'
union all select 'group ' || group_id || '/' || forum_id || ':' || setting from acl_group_grants where option = 'mod.pin'
union all select 'user ' || user_id || '/' || forum_id || ':' || setting from acl_user_grants where option = 'mod.pin'
order by 1`
	read := func() []string {
		t.Helper()
		r, err := pool.Query(ctx, q)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Close()
		var out []string
		for r.Next() {
			var s string
			if err := r.Scan(&s); err != nil {
				t.Fatal(err)
			}
			out = append(out, s)
		}
		return out
	}
	want := []string{"group 1/2:3", "role 1:3", "user 1/0:2"}
	if got := read(); !slices.Equal(got, want) {
		t.Fatalf("mod.pin rows %v, want %v", got, want)
	}
	// A second call finds mod.pin named and adds nothing.
	if _, err := pool.Exec(ctx, `insert into acl_group_grants (group_id, forum_id, option, setting) values (1, 3, 'mod.lock', 3)`); err != nil {
		t.Fatal(err)
	}
	if derived, err := db.DeriveModPin(ctx, pool); err != nil || derived {
		t.Errorf("second DeriveModPin = %v, %v; want false, nil", derived, err)
	}
	if got := read(); !slices.Equal(got, want) {
		t.Errorf("after the second call, mod.pin rows %v, want %v", got, want)
	}
}
