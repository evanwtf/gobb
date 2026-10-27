package board_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// exportOf returns the line of `gobb perms export` for userID.
func exportOf(t *testing.T, pool *pgxpool.Pool, userID int64) (boardOpts []string, forums map[string][]string) {
	t.Helper()
	var buf bytes.Buffer
	if err := acldb.Export(context.Background(), pool, &buf); err != nil {
		t.Fatal(err)
	}
	for _, line := range bytes.Split(bytes.TrimSpace(buf.Bytes()), []byte("\n")) {
		var l struct {
			UserID int64               `json:"user_id"`
			Board  []string            `json:"board"`
			Forums map[string][]string `json:"forums"`
		}
		if err := json.Unmarshal(line, &l); err != nil {
			t.Fatal(err)
		}
		if l.UserID == userID {
			return l.Board, l.Forums
		}
	}
	t.Fatalf("the export has no line for user %d", userID)
	return nil, nil
}

func TestSetRoleGrant(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	carolGroup := board.Holder{GroupID: 5}
	// Role 1 is a forum role; role 2 a board role.
	if err := board.SetRoleGrant(ctx, pool, alice, carolGroup, 3, 1, true, t0); err != nil {
		t.Fatal(err)
	}
	if actor, action, target, details := lastLog(t, pool); actor != 1 || action != "perm.grant" || target != 5 || details != `{"via": "web", "role": 1, "forum": 3}` {
		t.Errorf("row = %d %s %d %s", actor, action, target, details)
	}
	grid, err := board.Grid(ctx, pool, 3)
	if err != nil {
		t.Fatal(err)
	}
	var beta board.GridGroup
	for _, g := range grid.Groups {
		if g.ID == 5 {
			beta = g
		}
	}
	if !beta.Roles[1] || len(beta.Options) != 3 || grid.ForumName != "Members Lounge" {
		t.Errorf("grid = %+v, beta = %+v", grid, beta)
	}
	for _, r := range grid.Roles {
		if r.Kind == "board" {
			t.Errorf("the grid of a forum offers board role %d", r.ID)
		}
	}
	for name, err := range map[string]error{
		"again":              board.SetRoleGrant(ctx, pool, alice, carolGroup, 3, 1, true, t0),
		"forum role, board":  board.SetRoleGrant(ctx, pool, alice, carolGroup, 0, 1, true, t0),
		"board role, forum":  board.SetRoleGrant(ctx, pool, alice, carolGroup, 3, 2, true, t0),
		"no group":           board.SetRoleGrant(ctx, pool, alice, board.Holder{GroupID: 99}, 3, 1, true, t0),
		"no forum":           board.SetRoleGrant(ctx, pool, alice, carolGroup, 99, 1, true, t0),
		"no role":            board.SetRoleGrant(ctx, pool, alice, carolGroup, 3, 99, true, t0),
		"no holder":          board.SetRoleGrant(ctx, pool, alice, board.Holder{}, 3, 1, true, t0),
		"by a moderator":     board.SetRoleGrant(ctx, pool, viewerFor(t, pool, 2), carolGroup, 3, 1, false, t0),
		"revoke not granted": board.SetRoleGrant(ctx, pool, alice, board.Holder{GroupID: 3}, 3, 1, false, t0),
	} {
		want := map[string]error{"again": board.ErrNoChange, "forum role, board": board.ErrScope, "board role, forum": board.ErrScope,
			"no group": board.ErrNotFound, "no forum": board.ErrNotFound, "no role": board.ErrNotFound, "no holder": board.ErrNotFound,
			"by a moderator": board.ErrForbidden, "revoke not granted": board.ErrNoChange}[name]
		if !errors.Is(err, want) {
			t.Errorf("%s = %v, want %v", name, err, want)
		}
	}
	if err := board.SetRoleGrant(ctx, pool, alice, carolGroup, 3, 1, false, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, _, _ := lastLog(t, pool); action != "perm.revoke" {
		t.Errorf("revoke logged %s", action)
	}
}

func TestSetOptionGrantShowsInTheExport(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	carol := board.Holder{UserID: 3}
	if _, forums := exportOf(t, pool, 3); slices.Contains(forums["3"], "topic.start") {
		t.Fatal("carol may start topics in forum 3 before the grant")
	}
	if err := board.SetOptionGrant(ctx, pool, alice, carol, 3, "topic.start", acl.Yes, t0); err != nil {
		t.Fatal(err)
	}
	if _, forums := exportOf(t, pool, 3); !slices.Contains(forums["3"], "topic.start") {
		t.Errorf("the export lacks the grant: %v", forums["3"])
	}
	if _, action, target, details := lastLog(t, pool); action != "perm.grant" || target != 3 || details != `{"via": "web", "forum": 3, "option": "topic.start", "setting": "yes"}` {
		t.Errorf("row = %s %d %s", action, target, details)
	}
	// Two grants of one option become one with the new setting.
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 3, 'topic.start', 3)`)
	if err := board.SetOptionGrant(ctx, pool, alice, carol, 3, "topic.start", acl.Never, t0); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from acl_user_grants where user_id = 3 and forum_id = 3 and option = 'topic.start' and setting = 2`).Scan(&n); err != nil || n != 1 {
		t.Errorf("never grants = %d, %v", n, err)
	}
	if _, forums := exportOf(t, pool, 3); slices.Contains(forums["3"], "topic.start") {
		t.Error("Never did not take topic.start away")
	}
	for name, c := range map[string]struct {
		forum   int64
		option  string
		setting acl.Setting
		want    error
	}{
		"same":                {3, "topic.start", acl.Never, board.ErrNoChange},
		"forum option, board": {0, "forum.read", acl.Yes, board.ErrScope},
		"board option, forum": {3, "admin", acl.Yes, board.ErrScope},
		"unknown option":      {3, "no.such", acl.Yes, board.ErrScope},
		"bad setting":         {3, "forum.read", acl.Setting(9), board.ErrScope},
		"unset nothing":       {2, "topic.start", acl.Unset, board.ErrNoChange},
	} {
		if err := board.SetOptionGrant(ctx, pool, alice, carol, c.forum, c.option, c.setting, t0); !errors.Is(err, c.want) {
			t.Errorf("%s = %v, want %v", name, err, c.want)
		}
	}
	if err := board.SetOptionGrant(ctx, pool, alice, carol, 3, "topic.start", acl.Unset, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, _, _ := lastLog(t, pool); action != "perm.revoke" {
		t.Errorf("unset logged %s", action)
	}
	// A moderator option fits both board-wide and a forum.
	if err := board.SetOptionGrant(ctx, pool, alice, carol, 0, "mod.lock", acl.Yes, t0); err != nil {
		t.Errorf("mod option board-wide = %v", err)
	}
}

// TestPermissionsKeepAnAdmin: with alice not an owner, the admins group's grant
// of admin is the last one. Taking it away, by option, by revoke or through a
// role, is refused and changes nothing.
func TestPermissionsKeepAnAdmin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	mustExec(t, pool, `update users set is_owner = false where id = 1`)
	before := logCount(t, pool)
	admins := board.Holder{GroupID: 4}
	if err := board.SetOptionGrant(ctx, pool, alice, admins, 0, "admin", acl.Unset, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Errorf("unset admin = %v", err)
	}
	if err := board.SetOptionGrant(ctx, pool, alice, admins, 0, "admin", acl.No, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Errorf("admin to No = %v", err)
	}
	if err := board.SetOptionGrant(ctx, pool, alice, board.Holder{UserID: 1}, 0, "admin", acl.Never, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Errorf("Never on alice = %v", err)
	}
	var grantID int64
	if err := pool.QueryRow(ctx, `select id from acl_group_grants where group_id = 4 and option = 'admin'`).Scan(&grantID); err != nil {
		t.Fatal(err)
	}
	if err := board.RevokeGrant(ctx, pool, alice, false, grantID, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Errorf("revoke = %v", err)
	}
	if logCount(t, pool) != before {
		t.Error("a refused change wrote a row")
	}
	// Move admin into role 2 and grant that role to the admins group: then a role
	// change that removes admin is refused too.
	mustExec(t, pool, `insert into acl_role_settings (role_id, option, setting) values (2, 'admin', 3)`)
	mustExec(t, pool, `insert into acl_group_grants (group_id, forum_id, role_id) values (4, 0, 2)`)
	mustExec(t, pool, `delete from acl_group_grants where id = $1`, grantID)
	role, err := board.RoleByID(ctx, pool, 2)
	if err != nil {
		t.Fatal(err)
	}
	without := map[string]acl.Setting{}
	for k, v := range role.Settings {
		if k != "admin" {
			without[k] = v
		}
	}
	before = logCount(t, pool)
	if err := board.UpdateRole(ctx, pool, alice, 2, without, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Errorf("role without admin = %v", err)
	}
	if logCount(t, pool) != before {
		t.Error("a refused change wrote a row")
	}
	if ok, err := board.Preview(ctx, pool, 1, 0); err != nil || !slices.Contains(ok, "admin") {
		t.Errorf("alice lost admin: %v, %v", ok, err)
	}
}

func TestRevokeGrantAndUpdateRole(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice := viewerFor(t, pool, 1)
	grants, err := board.UserGrants(ctx, pool)
	if err != nil || len(grants) != 1 || grants[0].Holder != (board.Author{ID: 4, Name: "dave"}) || grants[0].Setting != acl.Never || grants[0].ForumName != "Main Discussion" {
		t.Fatalf("member grants = %+v, %v", grants, err)
	}
	if err := board.RevokeGrant(ctx, pool, alice, true, grants[0].ID, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, target, details := lastLog(t, pool); action != "perm.revoke" || target != 4 || details != `{"via": "web", "forum": 2, "option": "forum.read"}` {
		t.Errorf("row = %s %d %s", action, target, details)
	}
	if err := board.RevokeGrant(ctx, pool, alice, true, grants[0].ID, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("revoke again = %v", err)
	}

	role, err := board.RoleByID(ctx, pool, 1)
	if err != nil || role.Kind != "forum" {
		t.Fatalf("role 1 = %+v, %v", role, err)
	}
	next := map[string]acl.Setting{}
	for k, v := range role.Settings {
		next[k] = v
	}
	next["poll.change_vote"] = acl.Yes
	if err := board.UpdateRole(ctx, pool, alice, 1, next, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, target, details := lastLog(t, pool); action != "role.update" || target != 1 || details != `{"via": "web", "options": ["poll.change_vote"]}` {
		t.Errorf("row = %s %d %s", action, target, details)
	}
	if err := board.UpdateRole(ctx, pool, alice, 1, next, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("same settings = %v", err)
	}
	next["account.avatar"] = acl.Yes
	if err := board.UpdateRole(ctx, pool, alice, 1, next, t0); !errors.Is(err, board.ErrScope) {
		t.Errorf("a board option in a forum role = %v", err)
	}
	if err := board.UpdateRole(ctx, pool, alice, 99, next, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("no role = %v", err)
	}
}

func TestPreview(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	got, err := board.Preview(ctx, pool, 1, 3)
	if err != nil || !slices.Contains(got, "forum.read") || slices.Contains(got, "admin") {
		t.Errorf("alice in forum 3 = %v, %v", got, err)
	}
	if got, _ := board.Preview(ctx, pool, 1, 0); !slices.Contains(got, "admin") || slices.Contains(got, "forum.read") {
		t.Errorf("alice board-wide = %v", got)
	}
	// dave holds forum.read Never in forum 2, over the Yes of the members group.
	if got, _ := board.Preview(ctx, pool, 4, 2); slices.Contains(got, "forum.read") {
		t.Errorf("dave in forum 2 = %v", got)
	}
	if _, err := board.Preview(ctx, pool, 99, 0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("no member = %v", err)
	}
	if !slices.Contains(board.OptionsFor(0), "admin") || slices.Contains(board.OptionsFor(0), "forum.read") || !slices.Contains(board.OptionsFor(2), "mod.edit") {
		t.Error("OptionsFor does not follow the scopes")
	}
	if slices.Contains(board.RoleOptions("mod"), "forum.read") || !slices.Contains(board.RoleOptions("board"), "pm.send") {
		t.Error("RoleOptions does not follow the kinds")
	}
}

// TestOwnerGrantsNeedAnOwner: an admin who is not an owner cannot give, change
// or take away a grant of an owner. An owner can.
func TestOwnerGrantsNeedAnOwner(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	mustExec(t, pool, `insert into group_members (group_id, user_id) values (4, 2)`)
	alice, bob := viewerFor(t, pool, 1), viewerFor(t, pool, 2)
	owner := board.Holder{UserID: 1}
	if err := board.SetOptionGrant(ctx, pool, bob, owner, 3, "topic.start", acl.Never, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("an option grant on an owner by an admin = %v", err)
	}
	if err := board.SetRoleGrant(ctx, pool, bob, owner, 3, 1, true, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("a role grant on an owner by an admin = %v", err)
	}
	if err := board.SetOptionGrant(ctx, pool, alice, owner, 3, "topic.start", acl.Yes, t0); err != nil {
		t.Fatalf("an option grant on an owner by an owner = %v", err)
	}
	var id int64
	if err := pool.QueryRow(ctx, `select id from acl_user_grants where user_id = 1`).Scan(&id); err != nil {
		t.Fatal(err)
	}
	if err := board.RevokeGrant(ctx, pool, bob, true, id, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("a revoke of an owner's grant by an admin = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from acl_user_grants where id = $1`, id).Scan(&n); err != nil || n != 1 {
		t.Errorf("the grant is gone: %d, %v", n, err)
	}
	// A grant of a member who is not an owner stays open to every admin.
	if err := board.SetOptionGrant(ctx, pool, bob, board.Holder{UserID: 3}, 3, "topic.start", acl.Yes, t0); err != nil {
		t.Errorf("a grant on a member by an admin = %v", err)
	}
}
