package board

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/db"
)

// The permission pages of the admin area: the grants of the groups for each
// forum and board-wide, the grants of single members, and the options inside a
// role. Every write takes [LockAdminSet], makes its change, and calls
// [EnsureAdminRemains], so no change leaves the board without an admin.
//
// The scope rules are the ones of the bundle check and of [acl.Resolve]: a
// forum role needs a forum, a board role needs board-wide (forum 0), and a mod
// role fits both. A Forum option needs a forum, a Board option needs
// board-wide, and a Moderator option fits both.

// ErrScope means that a grant does not fit where it was asked for: a forum
// role or option board-wide, or a board role or option on a forum. Nothing was
// written.
var ErrScope = errors.New("board: the grant does not fit there")

// Role is one role with the options inside it.
type Role struct {
	ID          int64
	Name        string
	Kind        string
	Description string
	// Settings maps an option of the role to its setting.
	Settings map[string]acl.Setting
}

// FitsForum reports whether the role can be granted on forumID: forum 0 is
// board-wide.
func (r Role) FitsForum(forumID int64) bool { return roleFits(r.Kind, forumID) }

func roleFits(kind string, forumID int64) bool {
	switch kind {
	case "forum":
		return forumID != 0
	case "board":
		return forumID == 0
	}
	return true
}

// optionFits reports whether option can be granted on forumID.
func optionFits(option string, forumID int64) bool {
	o, ok := acl.Lookup(option)
	if !ok {
		return false
	}
	switch o.Scope {
	case acl.Forum:
		return forumID != 0
	case acl.Board:
		return forumID == 0
	}
	return true
}

// OptionsFor returns the names of the options that a grant on forumID can
// hold, in the order of the registry.
func OptionsFor(forumID int64) []string {
	var out []string
	for _, o := range acl.Options {
		if optionFits(o.Name, forumID) {
			out = append(out, o.Name)
		}
	}
	return out
}

// RoleOptions returns the names of the options that a role of kind can hold
// with an effect, in the order of the registry: a forum role holds the Forum
// and Moderator options, a board role the Board and Moderator options, and a
// mod role the Moderator options.
func RoleOptions(kind string) []string {
	var out []string
	for _, o := range acl.Options {
		switch {
		case o.Scope == acl.Moderator,
			kind == "forum" && o.Scope == acl.Forum,
			kind == "board" && o.Scope == acl.Board:
			out = append(out, o.Name)
		}
	}
	return out
}

// Roles returns every role with its settings, in the order of position.
func Roles(ctx context.Context, q db.Querier) ([]Role, error) {
	rows, err := q.Query(ctx, `select id, name, kind, description from acl_roles order by position, id`)
	if err != nil {
		return nil, fmt.Errorf("board: list roles: %w", err)
	}
	roles, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Role, error) {
		r := Role{Settings: map[string]acl.Setting{}}
		return r, row.Scan(&r.ID, &r.Name, &r.Kind, &r.Description)
	})
	if err != nil {
		return nil, fmt.Errorf("board: list roles: %w", err)
	}
	byID := map[int64]*Role{}
	for i := range roles {
		byID[roles[i].ID] = &roles[i]
	}
	rows, err = q.Query(ctx, `select role_id, option, setting from acl_role_settings`)
	if err != nil {
		return nil, fmt.Errorf("board: list role settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id      int64
			option  string
			setting int16
		)
		if err := rows.Scan(&id, &option, &setting); err != nil {
			return nil, fmt.Errorf("board: list role settings: %w", err)
		}
		if r := byID[id]; r != nil {
			r.Settings[option] = acl.Setting(setting)
		}
	}
	return roles, rows.Err()
}

// RoleByID returns one role. A role that does not exist is [ErrNotFound].
func RoleByID(ctx context.Context, q db.Querier, id int64) (Role, error) {
	roles, err := Roles(ctx, q)
	if err != nil {
		return Role{}, err
	}
	for _, r := range roles {
		if r.ID == id {
			return r, nil
		}
	}
	return Role{}, ErrNotFound
}

// Grant is one stored grant of a group or a member: a role, or one option
// with a setting.
type Grant struct {
	ID int64
	// Holder is the group or the member, by ID and name.
	Holder Author
	// ForumID is 0 for board-wide; ForumName is empty then.
	ForumID   int64
	ForumName string
	// RoleID is 0 for a grant of one option.
	RoleID   int64
	RoleName string
	Option   string
	Setting  acl.Setting
}

// grantColumns returns the columns that read a [Grant] from a grants table g,
// with the joins of grantJoins. The holders are the table h, and name is the
// column of h that holds the name.
func grantColumns(name string) string {
	return `g.id, h.id, h.` + name + `, g.forum_id, coalesce(f.name, ''), coalesce(g.role_id, 0), coalesce(r.name, ''),
       coalesce(g.option, ''), coalesce(g.setting, 0)`
}

const grantJoins = `
  left join forums f on f.id = g.forum_id
  left join acl_roles r on r.id = g.role_id`

func collectGrants(rows pgx.Rows) ([]Grant, error) {
	return pgx.CollectRows(rows, func(row pgx.CollectableRow) (Grant, error) {
		var (
			g       Grant
			setting int16
		)
		err := row.Scan(&g.ID, &g.Holder.ID, &g.Holder.Name, &g.ForumID, &g.ForumName, &g.RoleID, &g.RoleName, &g.Option, &setting)
		g.Setting = acl.Setting(setting)
		return g, err
	})
}

// GridGroup is one group of the permission grid of a forum.
type GridGroup struct {
	ID   int64
	Name string
	// Roles holds the IDs of the roles that the group holds there.
	Roles map[int64]bool
	// Options holds the grants of one option that the group holds there.
	Options []Grant
}

// PermissionGrid is the grants of every group on one forum, or board-wide.
type PermissionGrid struct {
	ForumID   int64
	ForumName string
	// Roles are the roles that fit there.
	Roles  []Role
	Groups []GridGroup
}

// Grid returns the permission grid of forumID; forum 0 is board-wide. A forum
// that does not exist is [ErrNotFound].
func Grid(ctx context.Context, q db.Querier, forumID int64) (PermissionGrid, error) {
	out := PermissionGrid{ForumID: forumID}
	if forumID != 0 {
		err := q.QueryRow(ctx, `select name from forums where id = $1`, forumID).Scan(&out.ForumName)
		if errors.Is(err, pgx.ErrNoRows) {
			return PermissionGrid{}, ErrNotFound
		}
		if err != nil {
			return PermissionGrid{}, fmt.Errorf("board: read forum %d: %w", forumID, err)
		}
	}
	roles, err := Roles(ctx, q)
	if err != nil {
		return PermissionGrid{}, err
	}
	for _, r := range roles {
		if r.FitsForum(forumID) {
			out.Roles = append(out.Roles, r)
		}
	}
	rows, err := q.Query(ctx, `select id, name from groups order by builtin is null, id`)
	if err != nil {
		return PermissionGrid{}, fmt.Errorf("board: list groups: %w", err)
	}
	groups, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (GridGroup, error) {
		g := GridGroup{Roles: map[int64]bool{}}
		return g, row.Scan(&g.ID, &g.Name)
	})
	if err != nil {
		return PermissionGrid{}, fmt.Errorf("board: list groups: %w", err)
	}
	rows, err = q.Query(ctx, `select `+grantColumns("name")+` from acl_group_grants g join groups h on h.id = g.group_id`+grantJoins+`
 where g.forum_id = $1 order by g.option, g.id`, forumID)
	if err != nil {
		return PermissionGrid{}, fmt.Errorf("board: list group grants: %w", err)
	}
	grants, err := collectGrants(rows)
	if err != nil {
		return PermissionGrid{}, fmt.Errorf("board: list group grants: %w", err)
	}
	for i := range groups {
		for _, g := range grants {
			switch {
			case g.Holder.ID != groups[i].ID:
			case g.RoleID != 0:
				groups[i].Roles[g.RoleID] = true
			default:
				groups[i].Options = append(groups[i].Options, g)
			}
		}
	}
	out.Groups = groups
	return out, nil
}

// UserGrants returns every grant of single members, by member name, then forum.
func UserGrants(ctx context.Context, q db.Querier) ([]Grant, error) {
	rows, err := q.Query(ctx, `select `+grantColumns("username")+` from acl_user_grants g join users h on h.id = g.user_id`+grantJoins+`
 order by h.username_clean, g.forum_id, g.option, g.id`)
	if err != nil {
		return nil, fmt.Errorf("board: list member grants: %w", err)
	}
	out, err := collectGrants(rows)
	if err != nil {
		return nil, fmt.Errorf("board: list member grants: %w", err)
	}
	return out, nil
}

// Holder names the group or the member of a grant. Exactly one ID is set.
type Holder struct {
	GroupID, UserID int64
}

func (h Holder) table() (table, col, targetType string, id int64) {
	if h.GroupID != 0 {
		return "acl_group_grants", "group_id", "group", h.GroupID
	}
	return "acl_user_grants", "user_id", "user", h.UserID
}

// checkHolder returns [ErrNotFound] when the group or the member of h does not
// exist, or h names neither or both.
func checkHolder(ctx context.Context, tx pgx.Tx, h Holder) error {
	if (h.GroupID == 0) == (h.UserID == 0) {
		return ErrNotFound
	}
	table, id := "groups", h.GroupID
	if h.UserID != 0 {
		table, id = "users", h.UserID
	}
	var ok bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from `+table+` where id = $1)`, id).Scan(&ok); err != nil {
		return fmt.Errorf("board: find %s %d: %w", table, id, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// checkHolderFor is [checkHolder] for a write of the admin v. Only an owner can
// change the grants of an owner ([ErrOwnerOnly]), as with the other changes to
// an owner.
func checkHolderFor(ctx context.Context, tx pgx.Tx, v Viewer, h Holder) error {
	if err := checkHolder(ctx, tx, h); err != nil || h.UserID == 0 {
		return err
	}
	return guardOwner(ctx, tx, v, h.UserID)
}

// checkForum returns [ErrNotFound] when forumID is not 0 and no forum has it.
func checkForum(ctx context.Context, tx pgx.Tx, forumID int64) error {
	if forumID == 0 {
		return nil
	}
	var ok bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from forums where id = $1)`, forumID).Scan(&ok); err != nil {
		return fmt.Errorf("board: find forum %d: %w", forumID, err)
	}
	if !ok {
		return ErrNotFound
	}
	return nil
}

// permWrite runs change for the admin v under [LockAdminSet], then checks that
// an admin remains.
func permWrite(ctx context.Context, pool *pgxpool.Pool, v Viewer, now time.Time, change func(tx pgx.Tx) (Change, error)) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := LockAdminSet(ctx, tx); err != nil {
			return Change{}, err
		}
		c, err := change(tx)
		if err != nil {
			return Change{}, err
		}
		if err := EnsureAdminRemains(ctx, tx); err != nil {
			return Change{}, err
		}
		return c, nil
	})
}

// SetRoleGrant gives the role roleID to the holder h on forumID (0 is
// board-wide) when on is true, and takes it away when on is false. A role that
// does not fit there is [ErrScope]; a holder, a forum or a role that does not
// exist is [ErrNotFound]; a grant that is there already, or a removal of one
// that is not, is [ErrNoChange]. It writes one mod_log row, "perm.grant" or
// "perm.revoke", on the group or the member.
func SetRoleGrant(ctx context.Context, pool *pgxpool.Pool, v Viewer, h Holder, forumID, roleID int64, on bool, now time.Time) error {
	return permWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := checkHolderFor(ctx, tx, v, h); err != nil {
			return Change{}, err
		}
		if err := checkForum(ctx, tx, forumID); err != nil {
			return Change{}, err
		}
		var kind string
		err := tx.QueryRow(ctx, `select kind from acl_roles where id = $1`, roleID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read role %d: %w", roleID, err)
		}
		if !roleFits(kind, forumID) {
			return Change{}, ErrScope
		}
		table, col, targetType, id := h.table()
		var n int64
		if on {
			tag, err := tx.Exec(ctx, `insert into `+table+` (`+col+`, forum_id, role_id)
select $1, $2, $3 where not exists (select 1 from `+table+` where `+col+` = $1 and forum_id = $2 and role_id = $3)`, id, forumID, roleID)
			if err != nil {
				return Change{}, fmt.Errorf("board: grant role %d: %w", roleID, err)
			}
			n = tag.RowsAffected()
		} else {
			tag, err := tx.Exec(ctx, `delete from `+table+` where `+col+` = $1 and forum_id = $2 and role_id = $3`, id, forumID, roleID)
			if err != nil {
				return Change{}, fmt.Errorf("board: revoke role %d: %w", roleID, err)
			}
			n = tag.RowsAffected()
		}
		if n == 0 {
			return Change{}, ErrNoChange
		}
		action := "perm.grant"
		if !on {
			action = "perm.revoke"
		}
		return Change{Action: action, TargetType: targetType, TargetID: id, Details: map[string]any{"forum": forumID, "role": roleID}}, nil
	})
}

// SetOptionGrant sets option for the holder h on forumID (0 is board-wide) to
// setting: [acl.Yes], [acl.No] or [acl.Never]. [acl.Unset] removes the grants
// of that option there. The grants of that option there become one. An option
// that is not in the registry or does not fit there is [ErrScope]; a holder or
// a forum that does not exist is [ErrNotFound]; a setting that is there
// already is [ErrNoChange]. It writes one mod_log row: "perm.grant" with the
// option and the setting, or "perm.revoke" for Unset.
func SetOptionGrant(ctx context.Context, pool *pgxpool.Pool, v Viewer, h Holder, forumID int64, option string, setting acl.Setting, now time.Time) error {
	if !optionFits(option, forumID) || setting > acl.Yes {
		return ErrScope
	}
	return permWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := checkHolderFor(ctx, tx, v, h); err != nil {
			return Change{}, err
		}
		if err := checkForum(ctx, tx, forumID); err != nil {
			return Change{}, err
		}
		table, col, targetType, id := h.table()
		rows, err := tx.Query(ctx, `select setting from `+table+` where `+col+` = $1 and forum_id = $2 and option = $3 for update`, id, forumID, option)
		if err != nil {
			return Change{}, fmt.Errorf("board: read grants of %s: %w", option, err)
		}
		have, err := pgx.CollectRows(rows, pgx.RowTo[int16])
		if err != nil {
			return Change{}, fmt.Errorf("board: read grants of %s: %w", option, err)
		}
		if (setting == acl.Unset && len(have) == 0) || (len(have) == 1 && acl.Setting(have[0]) == setting) {
			return Change{}, ErrNoChange
		}
		if _, err := tx.Exec(ctx, `delete from `+table+` where `+col+` = $1 and forum_id = $2 and option = $3`, id, forumID, option); err != nil {
			return Change{}, fmt.Errorf("board: clear grants of %s: %w", option, err)
		}
		if setting == acl.Unset {
			return Change{Action: "perm.revoke", TargetType: targetType, TargetID: id, Details: map[string]any{"forum": forumID, "option": option}}, nil
		}
		if _, err := tx.Exec(ctx, `insert into `+table+` (`+col+`, forum_id, option, setting) values ($1, $2, $3, $4)`, id, forumID, option, int16(setting)); err != nil {
			return Change{}, fmt.Errorf("board: grant %s: %w", option, err)
		}
		return Change{Action: "perm.grant", TargetType: targetType, TargetID: id,
			Details: map[string]any{"forum": forumID, "option": option, "setting": settingName(setting)}}, nil
	})
}

// RevokeGrant removes the grant grantID of a member (user is true) or of a
// group. A grant that does not exist is [ErrNotFound]. It writes one mod_log
// row, "perm.revoke", on the member or the group.
func RevokeGrant(ctx context.Context, pool *pgxpool.Pool, v Viewer, user bool, grantID int64, now time.Time) error {
	table, col, targetType := "acl_group_grants", "group_id", "group"
	if user {
		table, col, targetType = "acl_user_grants", "user_id", "user"
	}
	return permWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var (
			holder, forumID, roleID int64
			option                  string
		)
		err := tx.QueryRow(ctx, `delete from `+table+` where id = $1 returning `+col+`, forum_id, coalesce(role_id, 0), coalesce(option, '')`, grantID).
			Scan(&holder, &forumID, &roleID, &option)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: revoke grant %d: %w", grantID, err)
		}
		if user {
			if err := guardOwner(ctx, tx, v, holder); err != nil {
				return Change{}, err
			}
		}
		details := map[string]any{"forum": forumID}
		if roleID != 0 {
			details["role"] = roleID
		} else {
			details["option"] = option
		}
		return Change{Action: "perm.revoke", TargetType: targetType, TargetID: holder, Details: details}, nil
	})
}

// UpdateRole replaces the settings of role roleID with settings. An option
// with [acl.Unset] is left out of the role. An option that the role's kind
// cannot hold with an effect ([RoleOptions]) is [ErrScope]. A role that does
// not exist is [ErrNotFound], and settings equal to the stored ones are
// [ErrNoChange]. It writes one mod_log row, "role.update", with the names of
// the options that changed.
func UpdateRole(ctx context.Context, pool *pgxpool.Pool, v Viewer, roleID int64, settings map[string]acl.Setting, now time.Time) error {
	return permWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		var kind string
		err := tx.QueryRow(ctx, `select kind from acl_roles where id = $1 for update`, roleID).Scan(&kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return Change{}, ErrNotFound
		}
		if err != nil {
			return Change{}, fmt.Errorf("board: read role %d: %w", roleID, err)
		}
		allowed := RoleOptions(kind)
		for option, s := range settings {
			if s > acl.Yes || (s != acl.Unset && !slices.Contains(allowed, option)) {
				return Change{}, ErrScope
			}
		}
		rows, err := tx.Query(ctx, `select option, setting from acl_role_settings where role_id = $1`, roleID)
		if err != nil {
			return Change{}, fmt.Errorf("board: read role %d: %w", roleID, err)
		}
		stored := map[string]acl.Setting{}
		for rows.Next() {
			var (
				option string
				s      int16
			)
			if err := rows.Scan(&option, &s); err != nil {
				rows.Close()
				return Change{}, fmt.Errorf("board: read role %d: %w", roleID, err)
			}
			stored[option] = acl.Setting(s)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return Change{}, fmt.Errorf("board: read role %d: %w", roleID, err)
		}
		var changed []string
		for _, option := range allowed {
			want := settings[option]
			if want == stored[option] {
				continue
			}
			changed = append(changed, option)
			if _, err := tx.Exec(ctx, `delete from acl_role_settings where role_id = $1 and option = $2`, roleID, option); err != nil {
				return Change{}, fmt.Errorf("board: update role %d: %w", roleID, err)
			}
			if want != acl.Unset {
				if _, err := tx.Exec(ctx, `insert into acl_role_settings (role_id, option, setting) values ($1, $2, $3)`, roleID, option, int16(want)); err != nil {
					return Change{}, fmt.Errorf("board: update role %d: %w", roleID, err)
				}
			}
		}
		if len(changed) == 0 {
			return Change{}, ErrNoChange
		}
		return Change{Action: "role.update", TargetType: "role", TargetID: roleID, Details: map[string]any{"options": changed}}, nil
	})
}

// settingName returns the name of a setting for the log: "yes", "no" or
// "never".
func settingName(s acl.Setting) string {
	switch s {
	case acl.Yes:
		return "yes"
	case acl.No:
		return "no"
	case acl.Never:
		return "never"
	}
	return "unset"
}

// Preview returns the options that member userID holds on forumID, resolved
// as the pages resolve them: the options that fit there, in the order of the
// registry. A forum option on forum 0 is never held. A member who does not
// exist is [ErrNotFound]. The state of the account is not checked: an inactive
// member holds the rights again after an activate.
func Preview(ctx context.Context, q db.Querier, userID, forumID int64) ([]string, error) {
	s, err := acldb.Load(ctx, q, userID)
	if errors.Is(err, acldb.ErrUnknownUser) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	set := acl.Resolve(s)
	var out []string
	for _, o := range acl.Options {
		if optionFits(o.Name, forumID) && set.Has(o.Name, forumID) {
			out = append(out, o.Name)
		}
	}
	return out, nil
}

// ForumName is one forum in a list of the admin pages, with its depth in the
// tree: 0 for a forum with no parent.
type ForumName struct {
	ID    int64
	Name  string
	Kind  string
	Depth int
}

// ForumNames returns every forum in the order of the tree: each forum after its
// parent, siblings by position.
func ForumNames(ctx context.Context, q db.Querier) ([]ForumName, error) {
	rows, err := q.Query(ctx, `
with recursive t as (
	select id, name, kind, 0 as depth, array[position::bigint, id] as path
	  from forums where parent_id is null
	union all
	select f.id, f.name, f.kind, t.depth + 1, t.path || array[f.position::bigint, f.id]
	  from forums f join t on f.parent_id = t.id
)
select id, name, kind, depth from t order by path`)
	if err != nil {
		return nil, fmt.Errorf("board: list forums: %w", err)
	}
	out, err := pgx.CollectRows(rows, pgx.RowToStructByPos[ForumName])
	if err != nil {
		return nil, fmt.Errorf("board: list forums: %w", err)
	}
	return out, nil
}
