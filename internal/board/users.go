package board

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// cardColumns are the columns of a [UserCard], from the table users as u and
// the rank joins of [cardJoins]. Every list of members reads them, so the rank
// rule lives in one place.
const cardColumns = `u.id, u.username, coalesce(u.avatar_path, ''), u.post_count, u.registered_at,
       coalesce(r.title, ar.title, ''), coalesce(r.image_path, ar.image_path, '')`

// cardJoins find the rank of u: the rank the member holds, or else the highest
// ordinary rank that the post count reaches.
const cardJoins = `
  left join ranks r on r.id = u.rank_id
  left join lateral (
        select title, image_path from ranks
         where special = false and min_posts <= u.post_count
         order by min_posts desc, id
         limit 1) ar on u.rank_id is null`

// rowScanner is the Scan method of a pgx row.
type rowScanner interface{ Scan(dest ...any) error }

// scanCard reads the cardColumns of a row into c, then the extra values.
func scanCard(row rowScanner, c *UserCard, extra ...any) error {
	dst := append([]any{&c.ID, &c.Name, &c.AvatarPath, &c.PostCount, &c.Joined, &c.RankTitle, &c.RankImage}, extra...)
	return row.Scan(dst...)
}

// ProfilePage is the profile of one member. It holds no email address, no IP
// address and no hidden group.
type ProfilePage struct {
	Card UserCard
	// LastVisit is nil when the member has no recorded visit.
	LastVisit *time.Time
	// Signature is nil when the member has none.
	Signature *markup.Node
	// Groups lists the names of the confirmed groups whose kind is not hidden.
	Groups []string
}

// Profile returns the profile of member userID as v sees it.
//
// A user who was removed for good gives [ErrGone], before any permission check.
// Without account.view_profiles the viewer gets [ErrForbidden], whatever the ID.
// A user who does not exist or is not active gives [ErrNotFound].
func Profile(ctx context.Context, q db.Querier, v Viewer, userID int64) (ProfilePage, error) {
	var gone bool
	if err := q.QueryRow(ctx, `select exists (select 1 from gone_users where id = $1)`, userID).Scan(&gone); err != nil {
		return ProfilePage{}, fmt.Errorf("board: check user %d: %w", userID, err)
	}
	if gone {
		return ProfilePage{}, ErrGone
	}
	if !v.Perms.Has("account.view_profiles", 0) {
		return ProfilePage{}, ErrForbidden
	}
	var (
		out ProfilePage
		sig []byte
	)
	err := scanCard(q.QueryRow(ctx, `select `+cardColumns+`, u.last_visit_at, u.signature_tree
  from users u`+cardJoins+`
 where u.id = $1 and u.state = 'active'`, userID), &out.Card, &out.LastVisit, &sig)
	if errors.Is(err, pgx.ErrNoRows) {
		return ProfilePage{}, ErrNotFound
	}
	if err != nil {
		return ProfilePage{}, fmt.Errorf("board: read profile %d: %w", userID, err)
	}
	if len(sig) > 0 {
		if n := decodeTree(sig, "signature of user", userID); len(n.Children) > 0 {
			out.Signature = n
		}
	}
	rows, err := q.Query(ctx, `
select g.name
  from group_members gm
  join groups g on g.id = gm.group_id
 where gm.user_id = $1 and not gm.is_pending and g.kind <> 'hidden'
 order by lower(g.name), g.id`, userID)
	if err != nil {
		return ProfilePage{}, fmt.Errorf("board: read groups of %d: %w", userID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return ProfilePage{}, fmt.Errorf("board: read groups of %d: %w", userID, err)
		}
		out.Groups = append(out.Groups, name)
	}
	if err := rows.Err(); err != nil {
		return ProfilePage{}, fmt.Errorf("board: read groups of %d: %w", userID, err)
	}
	return out, nil
}

// MembersPage is one page of the member list.
type MembersPage struct {
	Members []UserCard
	// Sort is "name", "joined" or "posts".
	Sort        string
	Page, Pages int
}

// memberOrder maps a sort name to its ORDER BY clause. The name is the only
// part of the clause that a visitor can influence, and only as a key here.
var memberOrder = map[string]string{
	"name":   "u.username_clean, u.id",
	"joined": "u.registered_at, u.id",
	"posts":  "u.post_count desc, u.username_clean, u.id",
}

// Members returns page number page (from 1) of the active members, in the order
// sort names: "name" (ascending, the default), "joined" (oldest first) or "posts"
// (most first). Any other sort is "name". The viewer needs account.view_profiles,
// or gets [ErrForbidden]. A page outside the list gives [ErrNotFound]. It runs two
// statements, however many members there are.
func Members(ctx context.Context, q db.Querier, v Viewer, sort string, page int) (MembersPage, error) {
	if !v.Perms.Has("account.view_profiles", 0) {
		return MembersPage{}, ErrForbidden
	}
	order, ok := memberOrder[sort]
	if !ok {
		sort, order = "name", memberOrder["name"]
	}
	var n int
	if err := q.QueryRow(ctx, `select count(*) from users where state = 'active'`).Scan(&n); err != nil {
		return MembersPage{}, fmt.Errorf("board: count members: %w", err)
	}
	out := MembersPage{Sort: sort, Page: page, Pages: pageCount(n, membersPerPage)}
	if page < 1 || page > out.Pages {
		return MembersPage{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `select `+cardColumns+`
  from users u`+cardJoins+`
 where u.state = 'active'
 order by `+order+`
 limit $1 offset $2`, membersPerPage, (page-1)*membersPerPage)
	if err != nil {
		return MembersPage{}, fmt.Errorf("board: list members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var c UserCard
		if err := scanCard(rows, &c); err != nil {
			return MembersPage{}, fmt.Errorf("board: list members: %w", err)
		}
		out.Members = append(out.Members, c)
	}
	if err := rows.Err(); err != nil {
		return MembersPage{}, fmt.Errorf("board: list members: %w", err)
	}
	return out, nil
}

// TeamGroup is a heading of the team page and the people under it.
type TeamGroup struct {
	Name    string
	Members []UserCard
}

// Team lists the active owners, then the confirmed active members of the built-in
// admins group, then those of the moderators group. A person shows once, in the
// first group that holds them, and a group with nobody left is left out. The
// heading of a group is its name; the owners have the heading "Owners". Anyone
// may see the team. It runs one statement.
func Team(ctx context.Context, q db.Querier) ([]TeamGroup, error) {
	rows, err := q.Query(ctx, `
select `+cardColumns+`, t.ord, t.heading
  from (
        select id, 0 as ord, 'Owners'::text as heading from users where is_owner and state = 'active'
        union all
        select gm.user_id, case g.builtin when 'admins' then 1 else 2 end, g.name
          from groups g
          join group_members gm on gm.group_id = g.id and not gm.is_pending
          join users gu on gu.id = gm.user_id and gu.state = 'active'
         where g.builtin in ('admins', 'moderators')
       ) t
  join users u on u.id = t.id`+cardJoins+`
 order by t.ord, u.username_clean, u.id`)
	if err != nil {
		return nil, fmt.Errorf("board: read team: %w", err)
	}
	defer rows.Close()
	var out []TeamGroup
	seen := map[int64]bool{}
	lastOrd := -1
	for rows.Next() {
		var (
			ord     int
			heading string
			c       UserCard
		)
		if err := scanCard(rows, &c, &ord, &heading); err != nil {
			return nil, fmt.Errorf("board: read team: %w", err)
		}
		if seen[c.ID] {
			continue
		}
		seen[c.ID] = true
		if len(out) == 0 || lastOrd != ord {
			out = append(out, TeamGroup{Name: heading})
			lastOrd = ord
		}
		out[len(out)-1].Members = append(out[len(out)-1].Members, c)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: read team: %w", err)
	}
	return out, nil
}
