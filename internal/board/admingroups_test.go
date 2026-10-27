package board_test

import (
	"context"
	"errors"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func TestAdminGroups(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	groups, err := board.AdminGroups(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 5 || groups[4].Name != "Beta testers" || groups[4].Builtin != "" {
		t.Fatalf("groups = %+v", groups)
	}
	beta := groups[4]
	if beta.Members != 1 || beta.Pending != 1 || beta.Grants != 3 || beta.Kind != "request" {
		t.Errorf("Beta testers = %+v", beta)
	}
	_, members, err := board.AdminGroupDetail(ctx, pool, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(members) != 2 || members[0].Name != "bob" || !members[0].Leader || members[1].Name != "carol" || !members[1].Pending {
		t.Errorf("members = %+v", members)
	}
	if _, _, err := board.AdminGroupDetail(ctx, pool, 99); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("a missing group = %v", err)
	}
}

func TestCreateUpdateDeleteGroup(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice, bob := viewerFor(t, pool, 1), viewerFor(t, pool, 2)

	id, err := board.CreateGroup(ctx, pool, alice, board.GroupInput{Name: " Artists ", Kind: "open", Description: "Draw"}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if actor, action, target, details := lastLog(t, pool); actor != 1 || action != "group.create" || target != id ||
		details != `{"via": "web", "kind": "open", "name": "Artists"}` {
		t.Errorf("row = %d %s %d %s", actor, action, target, details)
	}
	for name, in := range map[string]board.GroupInput{
		"empty name":    {Name: " ", Kind: "open"},
		"taken name":    {Name: "beta TESTERS", Kind: "open"},
		"builtin kind":  {Name: "X", Kind: "builtin"},
		"two-line name": {Name: "a\nb", Kind: "open"},
		"long desc":     {Name: "Y", Kind: "open", Description: string(make([]byte, 501))},
	} {
		var in2 *board.InputError
		if _, err := board.CreateGroup(ctx, pool, alice, in, t0); !errors.As(err, &in2) {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := board.CreateGroup(ctx, pool, bob, board.GroupInput{Name: "Mods' club", Kind: "open"}, t0); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("a create by a moderator = %v", err)
	}

	if err := board.UpdateGroup(ctx, pool, alice, id, board.GroupInput{Name: "Artists", Kind: "hidden", Description: "Paint"}, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, _, details := lastLog(t, pool); action != "group.update" || details != `{"via": "web", "fields": ["kind", "description"]}` {
		t.Errorf("update row = %s %s", action, details)
	}
	if err := board.UpdateGroup(ctx, pool, alice, id, board.GroupInput{Name: "Artists", Kind: "hidden", Description: "Paint"}, t0); !errors.Is(err, board.ErrNoChange) {
		t.Errorf("a save with no change = %v", err)
	}
	if err := board.UpdateGroup(ctx, pool, bob, id, board.GroupInput{Name: "Bob's", Kind: "open"}, t0); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("an update by a moderator = %v", err)
	}

	// A built-in group keeps its name and kind, and takes a new description.
	if err := board.UpdateGroup(ctx, pool, alice, 3, board.GroupInput{Name: "mods", Kind: "builtin"}, t0); !errors.Is(err, board.ErrBuiltinGroup) {
		t.Errorf("a rename of a built-in group = %v", err)
	}
	if err := board.UpdateGroup(ctx, pool, alice, 3, board.GroupInput{Name: "moderators", Kind: "open"}, t0); !errors.Is(err, board.ErrBuiltinGroup) {
		t.Errorf("a kind change of a built-in group = %v", err)
	}
	if err := board.UpdateGroup(ctx, pool, alice, 3, board.GroupInput{Name: "moderators", Description: "The mods"}, t0); err != nil {
		t.Errorf("a new description of a built-in group = %v", err)
	}
	if err := board.DeleteGroup(ctx, pool, alice, 3, t0); !errors.Is(err, board.ErrBuiltinGroup) {
		t.Errorf("a delete of a built-in group = %v", err)
	}
	// Beta testers has members and grants.
	if err := board.DeleteGroup(ctx, pool, alice, 5, t0); !errors.Is(err, board.ErrGroupInUse) {
		t.Errorf("a delete of a group in use = %v", err)
	}
	if err := board.DeleteGroup(ctx, pool, bob, id, t0); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("a delete by a moderator = %v", err)
	}
	if err := board.DeleteGroup(ctx, pool, alice, id, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, target, details := lastLog(t, pool); action != "group.delete" || target != id || details != `{"via": "web", "name": "Artists"}` {
		t.Errorf("delete row = %s %d %s", action, target, details)
	}
	if err := board.DeleteGroup(ctx, pool, alice, id, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("a second delete = %v", err)
	}
}

func TestChangeGroupMember(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	alice, bob := viewerFor(t, pool, 1), viewerFor(t, pool, 2)
	inGroup := func(group, user int64) (member, pending, leader bool) {
		t.Helper()
		err := pool.QueryRow(ctx, `select true, is_pending, is_leader from group_members where group_id = $1 and user_id = $2`, group, user).
			Scan(&member, &pending, &leader)
		if err != nil {
			return false, false, false
		}
		return member, pending, leader
	}
	steps := []struct {
		act     board.GroupMemberAction
		user    int64
		action  string
		details string
	}{
		{board.GroupApprove, 3, "group.member_add", `{"via": "web", "user": 3, "approved": true}`},
		{board.GroupLead, 3, "group.member_update", `{"via": "web", "user": 3, "leader": true}`},
		{board.GroupUnlead, 3, "group.member_update", `{"via": "web", "user": 3, "leader": false}`},
		{board.GroupRemove, 3, "group.member_remove", `{"via": "web", "user": 3}`},
		{board.GroupAdd, 4, "group.member_add", `{"via": "web", "user": 4}`},
	}
	for _, s := range steps {
		if err := board.ChangeGroupMember(ctx, pool, alice, 5, s.user, s.act, t0); err != nil {
			t.Fatalf("%s: %v", s.act, err)
		}
		if actor, action, target, details := lastLog(t, pool); actor != 1 || action != s.action || target != 5 || details != s.details {
			t.Errorf("%s: row = %d %s %d %s", s.act, actor, action, target, details)
		}
	}
	if m, p, _ := inGroup(5, 3); m || p {
		t.Error("carol is still in the group")
	}
	if m, p, _ := inGroup(5, 4); !m || p {
		t.Error("dave is not a confirmed member")
	}
	// A request that an admin refuses is gone.
	mustExec(t, pool, `insert into group_members (group_id, user_id, is_pending) values (5, 3, true)`)
	if err := board.ChangeGroupMember(ctx, pool, alice, 5, 3, board.GroupRefuse, t0); err != nil {
		t.Fatal(err)
	}
	if _, action, _, details := lastLog(t, pool); action != "group.member_remove" || details != `{"via": "web", "user": 3, "refused": true}` {
		t.Errorf("refuse row = %s %s", action, details)
	}
	if m, _, _ := inGroup(5, 3); m {
		t.Error("the refused request is still there")
	}

	for name, tc := range map[string]struct {
		v           board.Viewer
		group, user int64
		act         board.GroupMemberAction
		want        error
	}{
		"add twice":         {alice, 5, 4, board.GroupAdd, board.ErrNoChange},
		"remove a stranger": {alice, 5, 3, board.GroupRemove, board.ErrNoChange},
		"approve a member":  {alice, 5, 4, board.GroupApprove, board.ErrNoChange},
		"lead a stranger":   {alice, 5, 3, board.GroupLead, board.ErrNoChange},
		"guests":            {alice, 1, 3, board.GroupAdd, board.ErrBuiltinGroup},
		"no group":          {alice, 99, 3, board.GroupAdd, board.ErrNotFound},
		"no member":         {alice, 5, 99, board.GroupAdd, board.ErrNotFound},
		"by a moderator":    {bob, 5, 3, board.GroupAdd, board.ErrForbidden},
	} {
		if err := board.ChangeGroupMember(ctx, pool, tc.v, tc.group, tc.user, tc.act, t0); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", name, err, tc.want)
		}
	}
	// alice is the owner, so she stays an admin outside the admins group.
	if err := board.ChangeGroupMember(ctx, pool, alice, 4, 1, board.GroupRemove, t0); err != nil {
		t.Errorf("a remove of the owner from the admins group = %v", err)
	}
}

// TestGroupMemberKeepsAnAdmin: a change of the members of a group follows the
// rule of the permission pages. It cannot leave the board with no admin, and
// only an owner can change the groups of an owner.
func TestGroupMemberKeepsAnAdmin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Make bob an admin through the admins group and take the owner flag from
	// alice, so that bob and alice are the two admins and neither is an owner.
	mustExec(t, pool, `insert into group_members (group_id, user_id) values (4, 2)`)
	mustExec(t, pool, `update users set is_owner = false where id = 1`)
	alice, bob := viewerFor(t, pool, 1), viewerFor(t, pool, 2)
	if err := board.ChangeGroupMember(ctx, pool, alice, 4, 2, board.GroupRemove, t0); err != nil {
		t.Fatalf("a remove of one of two admins: %v", err)
	}
	if err := board.ChangeGroupMember(ctx, pool, alice, 4, 1, board.GroupRemove, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Errorf("a remove of the last admin = %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from group_members where group_id = 4 and user_id = 1`).Scan(&n); err != nil || n != 1 {
		t.Errorf("alice left the admins group: %d, %v", n, err)
	}

	// An owner's groups: only an owner changes them.
	mustExec(t, pool, `update users set is_owner = true where id = 1`)
	mustExec(t, pool, `insert into group_members (group_id, user_id) values (4, 2)`)
	if err := board.ChangeGroupMember(ctx, pool, bob, 5, 1, board.GroupAdd, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("a change by an admin to an owner's groups = %v", err)
	}
	alice = viewerFor(t, pool, 1)
	if err := board.ChangeGroupMember(ctx, pool, alice, 5, 1, board.GroupAdd, t0); err != nil {
		t.Errorf("a change by an owner to their own groups = %v", err)
	}
}
