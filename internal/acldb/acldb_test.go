package acldb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/importer"
)

// imported returns a database that holds bundletest.Small().
func imported(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := dbtest.New(t)
	dir := bundletest.Write(t, bundletest.Small())
	_, err := importer.Run(context.Background(), pool, dir, importer.Options{DataDir: t.TempDir()})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	return pool
}

func mustLoad(t *testing.T, q db.Querier, id int64) acl.Subject {
	t.Helper()
	s, err := Load(context.Background(), q, id)
	if err != nil {
		t.Fatalf("Load(%d): %v", id, err)
	}
	return s
}

func hasGrant(s acl.Subject, g acl.Grant) bool {
	return slices.Contains(s.Grants, g)
}

func TestLoadExpandsRolesAndGroups(t *testing.T) {
	pool := imported(t)
	bob := mustLoad(t, pool, 2)
	if bob.Owner {
		t.Error("bob is not an owner")
	}
	// Role 1 on forum 2 comes through the members group, one grant per option.
	for _, g := range []acl.Grant{
		{ForumID: 2, Option: "topic.start", Setting: acl.Yes},
		{ForumID: 2, Option: "poll.vote", Setting: acl.Yes},
		{ForumID: 2, Option: "topic.global", Setting: acl.Never},
		{ForumID: 0, Option: "pm.send", Setting: acl.Yes},  // role 2, board-wide
		{ForumID: 0, Option: "mod.edit", Setting: acl.Yes}, // moderators group
		{ForumID: 3, Option: "topic.reply", Setting: acl.Yes},
	} {
		if !hasGrant(bob, g) {
			t.Errorf("bob lacks grant %+v", g)
		}
	}
	set := acl.Resolve(bob)
	if !set.Has("topic.start", 2) || set.Has("topic.start", 3) {
		t.Error("topic.start: want yes on forum 2, no on forum 3")
	}
	if set.Has("topic.global", 2) {
		t.Error("topic.global: the role says never")
	}
	if !set.Has("mod.edit", 3) {
		t.Error("mod.edit board-wide must reach forum 3")
	}

	// Carol is a pending member of group 5: no grants from it.
	carol := mustLoad(t, pool, 3)
	if hasGrant(carol, acl.Grant{ForumID: 3, Option: "topic.reply", Setting: acl.Yes}) {
		t.Error("carol got a grant from a pending membership")
	}
	cs := acl.Resolve(carol)
	if cs.Has("forum.read", 3) || cs.Has("topic.reply", 3) {
		t.Error("carol reads or replies in forum 3 through a pending membership")
	}
	if !cs.Has("forum.read", 2) {
		t.Error("carol reads forum 2 through the members group")
	}

	// Bob, a confirmed member of group 5, reads forum 3.
	if !set.Has("forum.read", 3) {
		t.Error("bob must read forum 3 through Beta testers")
	}

	alice := mustLoad(t, pool, 1)
	if !alice.Owner || !acl.Resolve(alice).IsAdmin() {
		t.Error("alice must be an owner and an admin")
	}
}

func TestLoadNeverWins(t *testing.T) {
	pool := imported(t)
	dave := mustLoad(t, pool, 4)
	if !hasGrant(dave, acl.Grant{ForumID: 2, Option: "forum.read", Setting: acl.Yes}) {
		t.Fatal("dave must hold the yes from the members group")
	}
	if !hasGrant(dave, acl.Grant{ForumID: 2, Option: "forum.read", Setting: acl.Never}) {
		t.Fatal("dave must hold his own never")
	}
	set := acl.Resolve(dave)
	if set.Has("forum.read", 2) {
		t.Error("never must beat yes for forum.read on forum 2")
	}
	if !set.Has("forum.list", 2) {
		t.Error("forum.list on forum 2 is unaffected")
	}
}

func TestLoadGuest(t *testing.T) {
	pool := imported(t)
	g := mustLoad(t, pool, 0)
	if g.Owner {
		t.Error("a guest is not an owner")
	}
	want := []acl.Grant{
		{ForumID: 1, Option: "forum.list", Setting: acl.Yes},
		{ForumID: 2, Option: "forum.list", Setting: acl.Yes},
		{ForumID: 2, Option: "forum.read", Setting: acl.Yes},
	}
	less := func(a, b acl.Grant) int {
		if a.ForumID != b.ForumID {
			return int(a.ForumID - b.ForumID)
		}
		return strings.Compare(a.Option, b.Option)
	}
	got := slices.Clone(g.Grants)
	slices.SortFunc(got, less)
	if !slices.Equal(got, want) {
		t.Errorf("guest grants = %+v, want %+v", got, want)
	}
	set := acl.Resolve(g)
	if !set.Has("forum.read", 2) || set.Has("forum.read", 3) {
		t.Error("guest reads forum 2 but not forum 3")
	}
}

func TestLoadUnknownUser(t *testing.T) {
	pool := imported(t)
	for _, id := range []int64{99, -1} {
		if _, err := Load(context.Background(), pool, id); !errors.Is(err, ErrUnknownUser) {
			t.Errorf("Load(%d) error = %v, want ErrUnknownUser", id, err)
		}
	}
}

func TestLoadPassesUnknownOptionsThrough(t *testing.T) {
	pool := imported(t)
	ctx := context.Background()
	// The schema does not check option names; stored data may outlive an option.
	if _, err := pool.Exec(ctx,
		`insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'gone.option', 3)`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`insert into acl_role_settings (role_id, option, setting) values (1, 'gone.role_option', 3)`); err != nil {
		t.Fatal(err)
	}
	s := mustLoad(t, pool, 3)
	if !hasGrant(s, acl.Grant{ForumID: 2, Option: "gone.option", Setting: acl.Yes}) {
		t.Error("the unknown option must reach the subject")
	}
	if !acl.Resolve(s).Has("forum.read", 2) {
		t.Error("Resolve must skip the unknown option and keep the rest")
	}
}

func TestLoadRolesWithNoSettings(t *testing.T) {
	pool := imported(t)
	ctx := context.Background()
	before := len(mustLoad(t, pool, 3).Grants)
	if _, err := pool.Exec(ctx, `insert into acl_roles (name, kind) values ('Empty', 'forum')`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx,
		`insert into acl_user_grants (user_id, forum_id, role_id)
		 select 3, 2, id from acl_roles where name = 'Empty'`); err != nil {
		t.Fatal(err)
	}
	after := len(mustLoad(t, pool, 3).Grants)
	if before == 0 || after != before {
		t.Errorf("carol has %d grants before the empty role and %d after; want the same, and more than 0", before, after)
	}
	for _, g := range mustLoad(t, pool, 3).Grants {
		if g.Option == "" {
			t.Errorf("empty role produced a grant with no option: %+v", g)
		}
	}
}

// counter counts the queries that reach the database.
type counter struct {
	db.Querier
	n int
}

func (c *counter) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n++
	return c.Querier.Query(ctx, sql, args...)
}

func (c *counter) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n++
	return c.Querier.QueryRow(ctx, sql, args...)
}

func (c *counter) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.n++
	return c.Querier.Exec(ctx, sql, args...)
}

func TestLoadQueryCount(t *testing.T) {
	pool := imported(t)
	for _, id := range []int64{0, 1, 2, 3, 4} {
		c := &counter{Querier: pool}
		mustLoad(t, c, id)
		if c.n > 3 {
			t.Errorf("Load(%d) ran %d queries, want at most 3", id, c.n)
		}
	}
}

type parsedLine struct {
	UserID int64               `json:"user_id"`
	Board  []string            `json:"board"`
	Forums map[string][]string `json:"forums"`
}

func exportBytes(t *testing.T, q db.Querier) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := Export(context.Background(), q, &buf); err != nil {
		t.Fatalf("Export: %v", err)
	}
	return buf.Bytes()
}

func TestExportFormat(t *testing.T) {
	pool := imported(t)
	out := exportBytes(t, pool)
	if again := exportBytes(t, pool); !bytes.Equal(out, again) {
		t.Fatal("two exports differ")
	}
	if !bytes.HasSuffix(out, []byte("\n")) {
		t.Error("the last line has no newline")
	}
	lines := strings.Split(strings.TrimSuffix(string(out), "\n"), "\n")
	if len(lines) != 5 {
		t.Fatalf("got %d lines, want 5 (guest and 4 users)", len(lines))
	}
	// The guest line is exact, which pins the key order, the empty arrays and
	// the sort.
	wantGuest := `{"user_id":0,"board":[],"forums":{"1":["forum.list"],"2":["forum.list","forum.read"],"3":[]}}`
	if lines[0] != wantGuest {
		t.Errorf("guest line:\n got %s\nwant %s", lines[0], wantGuest)
	}
	parsed := make([]parsedLine, len(lines))
	for i, l := range lines {
		if err := json.Unmarshal([]byte(l), &parsed[i]); err != nil {
			t.Fatalf("line %d: %v", i, err)
		}
		if parsed[i].UserID != int64(i) {
			t.Errorf("line %d has user_id %d", i, parsed[i].UserID)
		}
		if !slices.IsSorted(parsed[i].Board) {
			t.Errorf("line %d board is not sorted: %v", i, parsed[i].Board)
		}
		for id, opts := range parsed[i].Forums {
			if opts == nil {
				t.Errorf("line %d forum %s is null", i, id)
			}
			if !slices.IsSorted(opts) {
				t.Errorf("line %d forum %s is not sorted: %v", i, id, opts)
			}
		}
		if len(parsed[i].Forums) != 3 {
			t.Errorf("line %d has %d forum keys, want 3", i, len(parsed[i].Forums))
		}
	}
	if slices.Contains(parsed[0].Forums["3"], "forum.read") {
		t.Error("the guest must not read forum 3")
	}
	if !slices.Contains(parsed[1].Board, "admin") {
		t.Error("alice must have admin")
	}
	// Dave: never on forum 2.
	if slices.Contains(parsed[4].Forums["2"], "forum.read") || !slices.Contains(parsed[4].Forums["2"], "forum.list") {
		t.Errorf("dave on forum 2 = %v", parsed[4].Forums["2"])
	}
	// A Moderator option shows in the board list and in every forum.
	if !slices.Contains(parsed[2].Board, "mod.edit") || !slices.Contains(parsed[2].Forums["3"], "mod.edit") {
		t.Error("bob must have mod.edit board-wide and in forum 3")
	}
	// A Forum option is not in the board list, a Board option not in a forum.
	if slices.Contains(parsed[2].Board, "topic.start") || slices.Contains(parsed[2].Forums["2"], "pm.send") {
		t.Error("an option appears under the wrong scope")
	}
}

func TestExportEmptyBoard(t *testing.T) {
	pool := dbtest.New(t)
	got := string(exportBytes(t, pool))
	want := `{"user_id":0,"board":[],"forums":{}}` + "\n"
	if got != want {
		t.Errorf("export of an empty board = %q, want %q", got, want)
	}
}

func TestExportOpensTheListsOnce(t *testing.T) {
	pool := imported(t)
	c := &counter{Querier: pool}
	exportBytes(t, c)
	// Two lists, then the queries for each of the five subjects.
	if c.n != 11 {
		t.Errorf("Export ran %d queries, want 11", c.n)
	}
}

func TestExportFailsWithoutSchema(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	var buf bytes.Buffer
	if err := Export(context.Background(), pool, &buf); err == nil {
		t.Fatal("Export succeeded without a schema")
	}
}
