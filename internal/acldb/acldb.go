// Package acldb loads an [acl.Subject] from the database and exports the
// effective permissions of every user.
//
// [Load] is the one place that reads grants. The package acl does the
// resolving; this package only fetches the stored rows.
package acldb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/db"
)

// ErrUnknownUser is the error that [Load] wraps when no user has the ID.
var ErrUnknownUser = errors.New("acldb: unknown user")

// grantSources are the two ways to pick the grants that apply to a subject.
// Both leave a CTE named g with the columns forum_id, role_id, option and
// setting.
const (
	// memberGrants takes the user's own grants and the grants of every group the
	// user belongs to with a confirmed membership. $1 is the user ID.
	memberGrants = `with g as (
	select forum_id, role_id, option, setting from acl_user_grants where user_id = $1
	union all
	select forum_id, role_id, option, setting from acl_group_grants
	 where group_id in (select group_id from group_members
	                     where user_id = $1 and not is_pending)
)`
	// guestGrants takes the grants of the built-in guests group. $1 is unused
	// and must be the guest ID, 0, so both texts take the same argument.
	guestGrants = `with g as (
	select gg.forum_id, gg.role_id, gg.option, gg.setting
	  from acl_group_grants gg join groups on groups.id = gg.group_id
	 where groups.builtin = 'guests' and $1::bigint = 0
)`

	// expandGrants turns each grant in g into one row for each option. A grant
	// of one option passes through. A grant of a role joins to the settings of
	// the role. A role without settings gives no row.
	expandGrants = `
select g.forum_id,
       coalesce(g.option, rs.option),
       coalesce(g.setting, rs.setting)::int
  from g left join acl_role_settings rs on rs.role_id = g.role_id
 where coalesce(g.option, rs.option) is not null`
)

// Load builds the subject for userID with two queries (one for a guest).
//
// The grants are the user's own grants plus the grants of every group with a
// confirmed membership. A grant of a role becomes one [acl.Grant] for each
// option of the role, on the forum of the grant. Owner is users.is_owner.
//
// userID 0 is a guest: the grants of the built-in guests group, and nothing
// else. A board without a guests group gives a guest no grants. Any other ID
// that is not a user gives an error that wraps [ErrUnknownUser].
//
// Load does not check option names. A stored option that the registry no longer
// holds reaches the subject, and [acl.Resolve] skips it.
func Load(ctx context.Context, q db.Querier, userID int64) (acl.Subject, error) {
	var s acl.Subject
	source := guestGrants
	if userID != 0 {
		err := q.QueryRow(ctx, `select is_owner from users where id = $1`, userID).Scan(&s.Owner)
		if errors.Is(err, pgx.ErrNoRows) {
			return acl.Subject{}, fmt.Errorf("%w: %d", ErrUnknownUser, userID)
		}
		if err != nil {
			return acl.Subject{}, fmt.Errorf("acldb: load user %d: %w", userID, err)
		}
		source = memberGrants
	}

	rows, err := q.Query(ctx, source+expandGrants, userID)
	if err != nil {
		return acl.Subject{}, fmt.Errorf("acldb: load grants of user %d: %w", userID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			g       acl.Grant
			setting int
		)
		if err := rows.Scan(&g.ForumID, &g.Option, &setting); err != nil {
			return acl.Subject{}, fmt.Errorf("acldb: load grants of user %d: %w", userID, err)
		}
		switch setting {
		case 1:
			g.Setting = acl.No
		case 2:
			g.Setting = acl.Never
		case 3:
			g.Setting = acl.Yes
		default:
			return acl.Subject{}, fmt.Errorf("acldb: grant of %q for user %d has setting %d", g.Option, userID, setting)
		}
		s.Grants = append(s.Grants, g)
	}
	if err := rows.Err(); err != nil {
		return acl.Subject{}, fmt.Errorf("acldb: load grants of user %d: %w", userID, err)
	}
	return s, nil
}

// exportLine is one line of the export. The field order is the key order.
type exportLine struct {
	UserID int64               `json:"user_id"`
	Board  []string            `json:"board"`
	Forums map[string][]string `json:"forums"`
}

// Export writes the effective permissions of the guest (user_id 0) and then of
// each user in ID order, as JSON Lines:
//
//	{"user_id":N,"board":[...],"forums":{"<id>":[...],...}}
//
// board lists the Board and Moderator options that the subject has board-wide.
// forums has a key for every forum, and lists the Forum and Moderator options
// that the subject has in that forum; a forum with none gets []. Each list is
// sorted by option name. The same database gives the same bytes.
//
// Export reads the forum list and the user list once. Run it on a transaction
// with a repeatable-read snapshot if the board changes while it runs.
func Export(ctx context.Context, q db.Querier, w io.Writer) error {
	forumIDs, err := ids(ctx, q, "forums")
	if err != nil {
		return err
	}
	userIDs, err := ids(ctx, q, "users")
	if err != nil {
		return err
	}
	enc := json.NewEncoder(w)
	for _, id := range append([]int64{0}, userIDs...) {
		s, err := Load(ctx, q, id)
		if err != nil {
			return err
		}
		if err := enc.Encode(effective(id, acl.Resolve(s), forumIDs)); err != nil {
			return fmt.Errorf("acldb: write export: %w", err)
		}
	}
	return nil
}

// ids returns every ID of table, in order. table is a constant of this package.
func ids(ctx context.Context, q db.Querier, table string) ([]int64, error) {
	rows, err := q.Query(ctx, "select id from "+table+" order by id")
	if err != nil {
		return nil, fmt.Errorf("acldb: list %s: %w", table, err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("acldb: list %s: %w", table, err)
		}
		out = append(out, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("acldb: list %s: %w", table, err)
	}
	return out, nil
}

// effective lists what set grants, board-wide and in each forum.
func effective(userID int64, set acl.Set, forumIDs []int64) exportLine {
	l := exportLine{
		UserID: userID,
		Board:  options(set, 0, acl.Board),
		Forums: make(map[string][]string, len(forumIDs)),
	}
	for _, f := range forumIDs {
		l.Forums[strconv.FormatInt(f, 10)] = options(set, f, acl.Forum)
	}
	return l
}

// options returns the sorted names of the options with scope or acl.Moderator
// that set has in forumID. The result is never nil.
func options(set acl.Set, forumID int64, scope acl.Scope) []string {
	out := []string{}
	for _, o := range acl.Options {
		if (o.Scope == scope || o.Scope == acl.Moderator) && set.Has(o.Name, forumID) {
			out = append(out, o.Name)
		}
	}
	slices.Sort(out)
	return out
}
