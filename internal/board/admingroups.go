package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
)

// The groups of the admin area: create, edit and delete a group, and change its
// members. A change of the members changes what they may do, so each one runs
// under the lock of the admin set and keeps one admin ([permWrite]).

// GroupKinds are the kinds that an admin can give a group. A built-in group has
// the kind "builtin" and keeps it.
var GroupKinds = []string{"open", "request", "closed", "hidden"}

// The limits of the text of a group, in characters.
const (
	maxGroupNameRunes        = 100
	maxGroupDescriptionRunes = 500
)

// ErrBuiltinGroup means that a write would rename, change the kind of, or delete
// a built-in group, or add a member to the guests group.
var ErrBuiltinGroup = errors.New("board: a built-in group keeps its name and kind")

// ErrGroupInUse means that a group to delete still has members, requests or
// grants.
var ErrGroupInUse = errors.New("board: the group has members or grants")

// AdminGroup is one group on the Groups page.
type AdminGroup struct {
	ID          int64
	Name        string
	Kind        string
	Description string
	// Builtin is the built-in name ("guests", "members", "moderators",
	// "admins"), or "" for a group that an admin made.
	Builtin string
	// Members counts the confirmed members, Pending the requests, and Grants the
	// grants of the group, on any forum or board-wide.
	Members, Pending, Grants int
}

// GroupMember is one member of a group, or one request to join it.
type GroupMember struct {
	UserID  int64
	Name    string
	Leader  bool
	Pending bool
}

// GroupInput holds the fields of the group form, as the admin sent them.
type GroupInput struct {
	Name, Kind, Description string
}

const adminGroupColumns = `
select g.id, g.name, g.kind, g.description, coalesce(g.builtin, ''),
       (select count(*) from group_members gm where gm.group_id = g.id and not gm.is_pending),
       (select count(*) from group_members gm where gm.group_id = g.id and gm.is_pending),
       (select count(*) from acl_group_grants gg where gg.group_id = g.id)
  from groups g`

func scanAdminGroup(row pgx.CollectableRow) (AdminGroup, error) {
	var g AdminGroup
	err := row.Scan(&g.ID, &g.Name, &g.Kind, &g.Description, &g.Builtin, &g.Members, &g.Pending, &g.Grants)
	return g, err
}

// AdminGroups returns every group: the built-in groups first, then the others by
// name.
func AdminGroups(ctx context.Context, q db.Querier) ([]AdminGroup, error) {
	rows, err := q.Query(ctx, adminGroupColumns+` order by g.builtin is null, lower(g.name), g.id`)
	if err != nil {
		return nil, fmt.Errorf("board: read groups: %w", err)
	}
	out, err := pgx.CollectRows(rows, scanAdminGroup)
	if err != nil {
		return nil, fmt.Errorf("board: read groups: %w", err)
	}
	return out, nil
}

// AdminGroupDetail returns group id and its members and requests: the leaders
// first, then by name; the requests last. A group that does not exist is
// [ErrNotFound].
func AdminGroupDetail(ctx context.Context, q db.Querier, id int64) (AdminGroup, []GroupMember, error) {
	rows, err := q.Query(ctx, adminGroupColumns+` where g.id = $1`, id)
	if err != nil {
		return AdminGroup{}, nil, fmt.Errorf("board: read group %d: %w", id, err)
	}
	g, err := pgx.CollectExactlyOneRow(rows, scanAdminGroup)
	if errors.Is(err, pgx.ErrNoRows) {
		return AdminGroup{}, nil, ErrNotFound
	}
	if err != nil {
		return AdminGroup{}, nil, fmt.Errorf("board: read group %d: %w", id, err)
	}
	rows, err = q.Query(ctx, `
select u.id, u.username, gm.is_leader, gm.is_pending
  from group_members gm join users u on u.id = gm.user_id
 where gm.group_id = $1
 order by gm.is_pending, gm.is_leader desc, u.username_clean, u.id`, id)
	if err != nil {
		return AdminGroup{}, nil, fmt.Errorf("board: read members of group %d: %w", id, err)
	}
	members, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (GroupMember, error) {
		var m GroupMember
		err := row.Scan(&m.UserID, &m.Name, &m.Leader, &m.Pending)
		return m, err
	})
	if err != nil {
		return AdminGroup{}, nil, fmt.Errorf("board: read members of group %d: %w", id, err)
	}
	return g, members, nil
}

// cleanGroupInput trims the fields of in and checks them. A field that fails
// gives an [*InputError] on "name", "kind" or "description".
func cleanGroupInput(in GroupInput) (GroupInput, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Description = strings.TrimSpace(in.Description)
	switch {
	case in.Name == "":
		return in, inputError("name", "Enter a name.")
	case utf8.RuneCountInString(in.Name) > maxGroupNameRunes:
		return in, inputError("name", fmt.Sprintf("The name is longer than %d characters.", maxGroupNameRunes))
	case !utf8.ValidString(in.Name) || strings.ContainsAny(in.Name, "\r\n\t"):
		return in, inputError("name", "Enter the name on one line.")
	case utf8.RuneCountInString(in.Description) > maxGroupDescriptionRunes:
		return in, inputError("description", fmt.Sprintf("The description is longer than %d characters.", maxGroupDescriptionRunes))
	case !utf8.ValidString(in.Description) || strings.ContainsAny(in.Description, "\r\n"):
		return in, inputError("description", "Enter the description on one line.")
	}
	return in, nil
}

// nameTaken reports whether a group other than except has the name, in any case.
func nameTaken(ctx context.Context, tx pgx.Tx, name string, except int64) (bool, error) {
	var taken bool
	err := tx.QueryRow(ctx, `select exists (select 1 from groups where lower(name) = lower($1) and id <> $2)`, name, except).Scan(&taken)
	if err != nil {
		return false, fmt.Errorf("board: find group name: %w", err)
	}
	return taken, nil
}

// lockGroups locks the groups table against other writes of this file until the
// end of the transaction, so that two checks of a name cannot both pass.
func lockGroups(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `lock table groups in share row exclusive mode`); err != nil {
		return fmt.Errorf("board: lock groups: %w", err)
	}
	return nil
}

// CreateGroup adds a group with no members and no grants as the admin v, and
// returns its ID. A field that fails the checks, a kind that is not in
// [GroupKinds], and a name that another group has give an [*InputError]. It
// writes one group.create row.
func CreateGroup(ctx context.Context, pool *pgxpool.Pool, v Viewer, in GroupInput, now time.Time) (int64, error) {
	in, err := cleanGroupInput(in)
	if err != nil {
		return 0, err
	}
	if !validGroupKind(in.Kind) {
		return 0, inputError("kind", "Pick a kind from the list.")
	}
	var id int64
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := lockGroups(ctx, tx); err != nil {
			return Change{}, err
		}
		if taken, err := nameTaken(ctx, tx, in.Name, 0); err != nil || taken {
			if err == nil {
				err = inputError("name", "Another group has that name.")
			}
			return Change{}, err
		}
		if err := tx.QueryRow(ctx, `insert into groups (name, kind, description) values ($1, $2, $3) returning id`,
			in.Name, in.Kind, in.Description).Scan(&id); err != nil {
			return Change{}, fmt.Errorf("board: create group: %w", err)
		}
		return Change{Action: "group.create", TargetType: "group", TargetID: id,
			Details: map[string]any{"name": in.Name, "kind": in.Kind}}, nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// validGroupKind reports whether an admin can give a group the kind.
func validGroupKind(kind string) bool { return slices.Contains(GroupKinds, kind) }

// UpdateGroup saves the name, the kind and the description of group id. A
// built-in group takes a new description only: a new name or kind is
// [ErrBuiltinGroup]. A save that changes nothing is [ErrNoChange]. It writes
// one group.update row that lists the changed fields.
func UpdateGroup(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, in GroupInput, now time.Time) error {
	in, err := cleanGroupInput(in)
	if err != nil {
		return err
	}
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := lockGroups(ctx, tx); err != nil {
			return Change{}, err
		}
		var (
			name, kind, description string
			builtin                 *string
		)
		err := tx.QueryRow(ctx, `select name, kind, description, builtin from groups where id = $1`, id).
			Scan(&name, &kind, &description, &builtin)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read group %d: %w", id, err)
		}
		if builtin != nil {
			if in.Name != name || (in.Kind != "" && in.Kind != kind) {
				return Change{}, ErrBuiltinGroup
			}
			in.Kind = kind
		} else if !validGroupKind(in.Kind) {
			return Change{}, inputError("kind", "Pick a kind from the list.")
		}
		var fields []string
		if in.Name != name {
			if taken, err := nameTaken(ctx, tx, in.Name, id); err != nil || taken {
				if err == nil {
					err = inputError("name", "Another group has that name.")
				}
				return Change{}, err
			}
			fields = append(fields, "name")
		}
		if in.Kind != kind {
			fields = append(fields, "kind")
		}
		if in.Description != description {
			fields = append(fields, "description")
		}
		if len(fields) == 0 {
			return Change{}, ErrNoChange
		}
		if _, err := tx.Exec(ctx, `update groups set name = $2, kind = $3, description = $4 where id = $1`,
			id, in.Name, in.Kind, in.Description); err != nil {
			return Change{}, fmt.Errorf("board: update group %d: %w", id, err)
		}
		return Change{Action: "group.update", TargetType: "group", TargetID: id,
			Details: map[string]any{"fields": fields}}, nil
	})
}

// DeleteGroup deletes group id. A built-in group is [ErrBuiltinGroup]; a group
// with members, requests or grants is [ErrGroupInUse]. It writes one
// group.delete row with the name of the group.
func DeleteGroup(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, now time.Time) error {
	return permWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := lockGroups(ctx, tx); err != nil {
			return Change{}, err
		}
		g, _, err := AdminGroupDetail(ctx, tx, id)
		if err != nil {
			return Change{}, err
		}
		switch {
		case g.Builtin != "":
			return Change{}, ErrBuiltinGroup
		case g.Members+g.Pending+g.Grants > 0:
			return Change{}, ErrGroupInUse
		}
		if _, err := tx.Exec(ctx, `delete from groups where id = $1`, id); err != nil {
			return Change{}, fmt.Errorf("board: delete group %d: %w", id, err)
		}
		return Change{Action: "group.delete", TargetType: "group", TargetID: id,
			Details: map[string]any{"name": g.Name}}, nil
	})
}

// GroupMemberAction is a change to one member of a group.
type GroupMemberAction string

// The changes to a member of a group.
const (
	// GroupAdd adds a member, confirmed. A pending request becomes confirmed.
	GroupAdd GroupMemberAction = "add"
	// GroupRemove removes a confirmed member.
	GroupRemove GroupMemberAction = "remove"
	// GroupLead and GroupUnlead make a confirmed member a leader, or not.
	GroupLead   GroupMemberAction = "lead"
	GroupUnlead GroupMemberAction = "unlead"
	// GroupApprove confirms a request; GroupRefuse deletes it.
	GroupApprove GroupMemberAction = "approve"
	GroupRefuse  GroupMemberAction = "refuse"
)

// ChangeGroupMember makes the change act to member userID of group groupID, as
// the admin v. Only an owner can change the groups of an owner
// ([ErrOwnerOnly]). The guests group takes no members ([ErrBuiltinGroup]). A
// change that would leave no active admin is [ErrLastAdmin]; one that finds
// nothing to do, such as a remove of a member who is not there, is
// [ErrNoChange]. The mod_log row is group.member_add (add and approve),
// group.member_remove (remove and refuse) or group.member_update (lead and
// unlead), on the group, with the member in details.user.
func ChangeGroupMember(ctx context.Context, pool *pgxpool.Pool, v Viewer, groupID, userID int64, act GroupMemberAction, now time.Time) error {
	return permWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var builtin *string
		err := tx.QueryRow(ctx, `select builtin from groups where id = $1 for update`, groupID).Scan(&builtin)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read group %d: %w", groupID, err)
		}
		if builtin != nil && *builtin == "guests" {
			return Change{}, ErrBuiltinGroup
		}
		if err := lockUser(ctx, tx, userID); err != nil {
			return Change{}, err
		}
		if err := guardOwner(ctx, tx, v, userID); err != nil {
			return Change{}, err
		}
		var leader, pending, found bool
		err = tx.QueryRow(ctx, `select is_leader, is_pending from group_members where group_id = $1 and user_id = $2`,
			groupID, userID).Scan(&leader, &pending)
		switch {
		case err == nil:
			found = true
		case !errors.Is(err, pgx.ErrNoRows):
			return Change{}, fmt.Errorf("board: read member %d of group %d: %w", userID, groupID, err)
		}
		member := found && !pending
		details := map[string]any{"user": userID}
		action, sql := "", ""
		switch act {
		case GroupAdd:
			if member {
				return Change{}, ErrNoChange
			}
			action = "group.member_add"
			sql = `insert into group_members (group_id, user_id) values ($1, $2)
			       on conflict (group_id, user_id) do update set is_pending = false`
		case GroupApprove:
			if !found || !pending {
				return Change{}, ErrNoChange
			}
			action, details["approved"] = "group.member_add", true
			sql = `update group_members set is_pending = false where group_id = $1 and user_id = $2`
		case GroupRemove, GroupRefuse:
			if act == GroupRemove && !member || act == GroupRefuse && !(found && pending) {
				return Change{}, ErrNoChange
			}
			action = "group.member_remove"
			if act == GroupRefuse {
				details["refused"] = true
			}
			sql = `delete from group_members where group_id = $1 and user_id = $2`
		case GroupLead, GroupUnlead:
			on := act == GroupLead
			if !member || leader == on {
				return Change{}, ErrNoChange
			}
			action, details["leader"] = "group.member_update", on
			sql = `update group_members set is_leader = $3 where group_id = $1 and user_id = $2`
			if _, err := tx.Exec(ctx, sql, groupID, userID, on); err != nil {
				return Change{}, fmt.Errorf("board: change member %d of group %d: %w", userID, groupID, err)
			}
			return Change{Action: action, TargetType: "group", TargetID: groupID, Details: details}, nil
		default:
			return Change{}, fmt.Errorf("board: unknown group change %q", act)
		}
		if _, err := tx.Exec(ctx, sql, groupID, userID); err != nil {
			return Change{}, fmt.Errorf("board: change member %d of group %d: %w", userID, groupID, err)
		}
		return Change{Action: action, TargetType: "group", TargetID: groupID, Details: details}, nil
	})
}
