package db_test

import (
	"context"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
)

// schemaTables lists every table the release-1 schema defines.
var schemaTables = []string{
	"users", "gone_users", "groups", "group_members",
	"acl_roles", "acl_role_settings", "acl_group_grants", "acl_user_grants",
	"forums", "topics", "posts",
	"polls", "poll_options", "poll_votes",
	"attachments", "pms", "pm_recipients",
	"topic_watches", "bookmarks", "topic_marks", "forum_marks",
	"bans", "ranks", "smilies",
	"legacy_routes", "legacy_files",
	"sessions", "user_ips", "settings", "mod_log",
	"email_queue", "email_tokens", "static_pages",
}

// TestSchemaTables compares the base tables in the schema with schemaTables.
// The migration runner's own table is not part of the release-1 schema.
func TestSchemaTables(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	rows, err := pool.Query(ctx,
		`select table_name from information_schema.tables
		 where table_schema = current_schema() and table_type = 'BASE TABLE'
		   and table_name <> 'schema_migrations'
		 order by table_name`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		got = append(got, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	want := slices.Clone(schemaTables)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("base tables differ\n got: %v\nwant: %v", got, want)
	}
}

func TestSchemaForeignKeysAreDeferrable(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	var total int
	if err := pool.QueryRow(ctx,
		`select count(*) from pg_constraint where contype = 'f'`).Scan(&total); err != nil {
		t.Fatal(err)
	}
	if total == 0 {
		t.Fatal("the schema has no foreign keys")
	}
	rows, err := pool.Query(ctx,
		`select conrelid::regclass::text, conname from pg_constraint
		 where contype = 'f' and not condeferrable order by 1, 2`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table, name string
		if err := rows.Scan(&table, &name); err != nil {
			t.Fatal(err)
		}
		t.Errorf("foreign key %s on %s is not deferrable", name, table)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func insertUser(ctx context.Context, t *testing.T, pool *pgxpool.Pool, id any, name string) {
	t.Helper()
	_, err := pool.Exec(ctx,
		`insert into users (id, username, username_clean, email, email_clean,
			password_hash, password_scheme, state, registered_at)
		 values (coalesce($1::bigint, nextval(pg_get_serial_sequence('users', 'id'))),
			$2, $2, $2 || '@example.invalid', $2 || '@example.invalid',
			'x', 'gobb', 'active', now())`, id, name)
	if err != nil {
		t.Fatalf("insert user %s: %v", name, err)
	}
}

func TestExplicitIDsThenResetSequences(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	insertUser(ctx, t, pool, 54, "alpha")
	if err := db.ResetSequences(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var id int64
	err := pool.QueryRow(ctx,
		`insert into users (username, username_clean, email, email_clean,
			password_hash, password_scheme, state, registered_at)
		 values ('beta', 'beta', 'b@example.invalid', 'b@example.invalid',
			'x', 'gobb', 'active', now()) returning id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if id != 55 {
		t.Fatalf("new user id = %d, want 55", id)
	}
}

func TestResetSequencesOnEmptyTables(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	if err := db.ResetSequences(ctx, pool); err != nil {
		t.Fatal(err)
	}
	var id int64
	err := pool.QueryRow(ctx,
		`insert into ranks (title, min_posts, special) values ('Newcomer', 0, false)
		 returning id`).Scan(&id)
	if err != nil {
		t.Fatal(err)
	}
	if id != 1 {
		t.Fatalf("first rank id = %d, want 1", id)
	}
}

func TestResetSequencesCoversEveryIdentityColumn(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	rows, err := pool.Query(ctx,
		`select table_name, column_name from information_schema.columns
		 where table_schema = current_schema() and is_identity = 'YES'
		 order by table_name`)
	if err != nil {
		t.Fatal(err)
	}
	type col struct{ table, column string }
	var cols []col
	for rows.Next() {
		var c col
		if err := rows.Scan(&c.table, &c.column); err != nil {
			t.Fatal(err)
		}
		cols = append(cols, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(cols) < 10 {
		t.Fatalf("found %d identity columns, want at least 10", len(cols))
	}

	// Move every sequence far from 1, then reset: an empty table must give 1.
	for _, c := range cols {
		if _, err := pool.Exec(ctx,
			`select setval(pg_get_serial_sequence($1, $2), 500)`,
			c.table, c.column); err != nil {
			t.Fatalf("setval %s.%s: %v", c.table, c.column, err)
		}
	}
	if err := db.ResetSequences(ctx, pool); err != nil {
		t.Fatal(err)
	}
	for _, c := range cols {
		var next int64
		if err := pool.QueryRow(ctx,
			`select nextval(pg_get_serial_sequence($1, $2))`,
			c.table, c.column).Scan(&next); err != nil {
			t.Fatalf("nextval %s.%s: %v", c.table, c.column, err)
		}
		if next != 1 {
			t.Errorf("%s.%s: next id = %d, want 1", c.table, c.column, next)
		}
	}
}

func TestGrantExactlyOneOf(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	var groupID, roleID int64
	if err := pool.QueryRow(ctx,
		`insert into groups (name, kind) values ('Crew', 'open') returning id`).Scan(&groupID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`insert into acl_roles (name, kind) values ('Reader', 'forum') returning id`).Scan(&roleID); err != nil {
		t.Fatal(err)
	}

	bad := map[string]string{
		"both":                   `insert into acl_group_grants (group_id, role_id, option, setting) values ($1, $2, 'f_read', 3)`,
		"neither":                `insert into acl_group_grants (group_id) values ($1)`,
		"option without setting": `insert into acl_group_grants (group_id, option) values ($1, 'f_read')`,
		"setting without option": `insert into acl_group_grants (group_id, setting) values ($1, 3)`,
		"role and setting":       `insert into acl_group_grants (group_id, role_id, setting) values ($1, $2, 3)`,
		"negative forum":         `insert into acl_group_grants (group_id, forum_id, role_id) values ($1, -1, $2)`,
		"setting out of range":   `insert into acl_group_grants (group_id, option, setting) values ($1, 'f_read', 4)`,
	}
	for name, q := range bad {
		args := []any{groupID, roleID}
		if !strings.Contains(q, "$2") {
			args = args[:1]
		}
		if _, err := pool.Exec(ctx, q, args...); err == nil {
			t.Errorf("%s: insert succeeded, want a constraint error", name)
		}
	}

	good := []struct {
		q    string
		args []any
	}{
		{`insert into acl_group_grants (group_id, role_id) values ($1, $2)`, []any{groupID, roleID}},
		{`insert into acl_group_grants (group_id, forum_id, option, setting) values ($1, 7, 'f_read', 3)`, []any{groupID}},
	}
	for _, g := range good {
		if _, err := pool.Exec(ctx, g.q, g.args...); err != nil {
			t.Errorf("%s: %v", g.q, err)
		}
	}

	// The user table has the same rule.
	insertUser(ctx, t, pool, nil, "gamma")
	var userID int64
	if err := pool.QueryRow(ctx, `select id from users where username = 'gamma'`).Scan(&userID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`insert into acl_user_grants (user_id, role_id, option, setting) values ($1, $2, 'f_read', 3)`,
		userID, roleID); err == nil {
		t.Error("acl_user_grants: both succeeded, want a constraint error")
	}
	if _, err := pool.Exec(ctx,
		`insert into acl_user_grants (user_id) values ($1)`, userID); err == nil {
		t.Error("acl_user_grants: neither succeeded, want a constraint error")
	}
	if _, err := pool.Exec(ctx,
		`insert into acl_user_grants (user_id, option, setting) values ($1, 'f_read', 1)`,
		userID); err != nil {
		t.Errorf("acl_user_grants: option and setting: %v", err)
	}
}

func TestPostSearchVector(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	insertUser(ctx, t, pool, nil, "delta")
	var forumID, topicID int64
	if err := pool.QueryRow(ctx,
		`insert into forums (kind, name, position) values ('forum', 'General', 1) returning id`).Scan(&forumID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx,
		`insert into topics (forum_id, title, created_at, last_post_at, kind, visibility)
		 values ($1, 'Maintenance', now(), now(), 'normal', 'approved') returning id`,
		forumID).Scan(&topicID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`insert into posts (topic_id, forum_id, created_at, subject, body_source,
			body_tree, body_text, visibility)
		 values ($1, $2, now(), 'Status', 'server rebuilds', '[]'::jsonb, 'server rebuilds', 'approved')`,
		topicID, forumID); err != nil {
		t.Fatal(err)
	}

	count := func(query string) int {
		var n int
		if err := pool.QueryRow(ctx,
			`select count(*) from posts where search @@ plainto_tsquery('english', $1)`,
			query).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// The English stemmer maps "rebuilds" to "rebuild". It does not map the
	// irregular "rebuilt" to it, so the body uses the regular form.
	if n := count("rebuild"); n != 1 {
		t.Errorf("search for rebuild: %d rows, want 1", n)
	}
	if n := count("status"); n != 1 {
		t.Errorf("search for the subject: %d rows, want 1", n)
	}
	if n := count("database"); n != 0 {
		t.Errorf("search for an absent word: %d rows, want 0", n)
	}
}

// columnsLike returns each column of table that matches the LIKE pattern, as
// "type nullability default".
func columnsLike(ctx context.Context, t *testing.T, pool *pgxpool.Pool, table, pattern string) map[string]string {
	t.Helper()
	rows, err := pool.Query(ctx,
		`select column_name, data_type, is_nullable, coalesce(column_default, '')
		 from information_schema.columns
		 where table_schema = current_schema() and table_name = $1
		   and column_name like $2`, table, pattern)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]string{}
	for rows.Next() {
		var name, typ, nullable, def string
		if err := rows.Scan(&name, &typ, &nullable, &def); err != nil {
			t.Fatal(err)
		}
		got[name] = typ + " " + nullable + " " + def
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return got
}

// Each post and private message stores markup flags, and each member stores the
// same three for the signature. The three markup flags default to true: a new
// post wants every feature on. The match is exact, so a new column fails here.
func TestMarkupFlagColumns(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	on := "boolean NO true"
	check := func(table, pattern string, want map[string]string) {
		t.Helper()
		got := columnsLike(ctx, t, pool, table, pattern)
		if !maps.Equal(got, want) {
			t.Errorf("%s %s\n got: %v\nwant: %v", table, pattern, got, want)
		}
	}
	for _, table := range []string{"posts", "pms"} {
		check(table, `enable\_%`, map[string]string{
			"enable_markdown": on,
			"enable_smilies":  on,
			"enable_autolink": on,
			"enable_sig":      "boolean NO false",
		})
	}
	check("users", `signature\_%`, map[string]string{
		"signature_markdown": on,
		"signature_smilies":  on,
		"signature_autolink": on,
		"signature_source":   "text NO ''::text",
		"signature_tree":     "jsonb YES ",
	})
}

// The importer loads a bundle in one transaction with every constraint
// deferred. The last-post pointers are circular, so they need the deferral.
func TestImportOrderNeedsDeferredConstraints(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	load := func(deferred bool) error {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		if deferred {
			if _, err := tx.Exec(ctx, `set constraints all deferred`); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := tx.Exec(ctx,
			`insert into forums (id, kind, name, position, last_post_id)
			 values (1, 'forum', 'General', 1, 3)`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`insert into topics (id, forum_id, created_at, last_post_id, last_post_at, kind, visibility)
			 values (2, 1, now(), 3, now(), 'normal', 'approved')`); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx,
			`insert into posts (id, topic_id, forum_id, created_at, body_source, body_tree, body_text, visibility)
			 values (3, 2, 1, now(), 'hi', '[]'::jsonb, 'hi', 'approved')`); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}

	if err := load(false); err == nil {
		t.Error("load without deferral succeeded, want a foreign key error")
	}
	if err := load(true); err != nil {
		t.Errorf("load with deferred constraints: %v", err)
	}
}

func TestForumPositionDefaultsToZero(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)

	var position int
	err := pool.QueryRow(ctx,
		`insert into forums (kind, name) values ('forum', 'General') returning position`).Scan(&position)
	if err != nil {
		t.Fatalf("insert a forum without position: %v", err)
	}
	if position != 0 {
		t.Errorf("position = %d, want 0", position)
	}
}
