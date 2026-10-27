package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/db"
)

// The members pages of the admin area: find a member, read one, and the writes
// on one member. Each write goes through [AdminWrite], so it needs an admin and
// writes one mod_log row with the admin as the actor.

// membersPerAdminPage is the count of rows on one page of the admin's member
// search.
const membersPerAdminPage = 50

// adminSetLock is the key of the transaction lock that every write which can
// take the admin right away from a member holds. With it, two such writes
// cannot each see another admin and together leave none.
const adminSetLock = 0x676f6262_61646d6e // "gobbadmn"

var (
	// ErrLastAdmin means that the write would leave no active member with the
	// board-wide admin right. Nothing was written.
	ErrLastAdmin = errors.New("board: the board would have no admin")
	// ErrSelf means that an admin tried a write that the admin may not make on
	// their own account. Nothing was written.
	ErrSelf = errors.New("board: not on your own account")
	// ErrOwnerOnly means that only an owner may make this write on an owner.
	// Nothing was written.
	ErrOwnerOnly = errors.New("board: only an owner may do this to an owner")
)

// MemberRow is one member in the admin's member search.
type MemberRow struct {
	ID        int64
	Name      string
	Email     string
	Active    bool
	Owner     bool
	PostCount int
	Joined    time.Time
	// LastVisit is nil for a member who has never visited.
	LastVisit *time.Time
	// Banned is true when a user ban that has not ended holds the member.
	Banned bool
}

// MemberSearch is one page of the admin's member search.
type MemberSearch struct {
	Rows        []MemberRow
	Page, Pages int
}

// memberRowColumns are the columns of a [MemberRow], in the order of
// scanMemberRow. $1 is the time now, for the bans.
const memberRowColumns = `u.id, u.username, u.email, u.state = 'active', u.is_owner, u.post_count, u.registered_at, u.last_visit_at,
       exists (select 1 from bans b where b.kind = 'user' and b.user_id = u.id and (b.ends_at is null or b.ends_at > $1))`

func scanMemberRow(row rowScanner, m *MemberRow, extra ...any) error {
	return row.Scan(append([]any{&m.ID, &m.Name, &m.Email, &m.Active, &m.Owner, &m.PostCount, &m.Joined, &m.LastVisit, &m.Banned}, extra...)...)
}

// FindMembers returns page page of the members that match query, by username,
// 50 a page. An empty query matches every member. A query with an "@" matches
// the start of the clean email address ([account.CleanEmail]); any other query
// matches the start of the clean username ([account.CleanUsername]). Inactive
// members are in the list too. A page past the last is [ErrNotFound]; page 1
// of no match is not.
func FindMembers(ctx context.Context, q db.Querier, query string, page int, now time.Time) (MemberSearch, error) {
	col, prefix := "username_clean", ""
	query = strings.TrimSpace(query)
	switch {
	case query == "":
	case !utf8.ValidString(query) || strings.ContainsRune(query, 0):
		// No username or address holds such text.
		return MemberSearch{Page: 1, Pages: 1}, nil
	case strings.Contains(query, "@"):
		col, prefix = "email_clean", account.CleanEmail(query)
	default:
		prefix = account.CleanUsername(query)
	}
	// left(...) compares the start of the column with the prefix, with no
	// pattern characters to escape.
	where := func(arg string) string {
		return `left(u.` + col + `, char_length(` + arg + `::text)) = ` + arg + `::text`
	}
	var n int
	if err := q.QueryRow(ctx, `select count(*) from users u where `+where("$1"), prefix).Scan(&n); err != nil {
		return MemberSearch{}, fmt.Errorf("board: count members: %w", err)
	}
	out := MemberSearch{Page: page, Pages: pageCount(n, membersPerAdminPage)}
	if page < 1 || page > out.Pages {
		return MemberSearch{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `select `+memberRowColumns+` from users u where `+where("$2")+`
 order by u.username_clean, u.id limit $3 offset $4`, now, prefix, membersPerAdminPage, (page-1)*membersPerAdminPage)
	if err != nil {
		return MemberSearch{}, fmt.Errorf("board: find members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var m MemberRow
		if err := scanMemberRow(rows, &m); err != nil {
			return MemberSearch{}, fmt.Errorf("board: find members: %w", err)
		}
		out.Rows = append(out.Rows, m)
	}
	if err := rows.Err(); err != nil {
		return MemberSearch{}, fmt.Errorf("board: find members: %w", err)
	}
	return out, nil
}

// RankChoice is one rank that an admin can give.
type RankChoice struct {
	ID       int64
	Title    string
	Special  bool
	MinPosts int
}

// MemberDetail is what the admin's page of one member shows.
type MemberDetail struct {
	MemberRow
	EmailVerified bool
	// RankID is the rank that the member holds, or 0 for the rank of the post
	// count.
	RankID int64
	// IsAdmin is true when the member holds the board-wide admin right.
	IsAdmin bool
	// Ranks are the ranks of the board, the special ones first.
	Ranks []RankChoice
}

// AdminMember returns the member userID for the admin's page. A member who
// does not exist is [ErrNotFound].
func AdminMember(ctx context.Context, q db.Querier, userID int64, now time.Time) (MemberDetail, error) {
	var d MemberDetail
	err := scanMemberRow(q.QueryRow(ctx, `select `+memberRowColumns+`, u.email_verified, coalesce(u.rank_id, 0)
  from users u where u.id = $2`, now, userID), &d.MemberRow, &d.EmailVerified, &d.RankID)
	if errors.Is(err, pgx.ErrNoRows) {
		return MemberDetail{}, ErrNotFound
	}
	if err != nil {
		return MemberDetail{}, fmt.Errorf("board: read member %d: %w", userID, err)
	}
	if d.IsAdmin, err = holdsAdmin(ctx, q, userID); err != nil {
		return MemberDetail{}, err
	}
	rows, err := q.Query(ctx, `select id, title, special, min_posts from ranks order by special desc, min_posts, id`)
	if err != nil {
		return MemberDetail{}, fmt.Errorf("board: list ranks: %w", err)
	}
	d.Ranks, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (RankChoice, error) {
		var r RankChoice
		return r, row.Scan(&r.ID, &r.Title, &r.Special, &r.MinPosts)
	})
	if err != nil {
		return MemberDetail{}, fmt.Errorf("board: list ranks: %w", err)
	}
	return d, nil
}

// holdsAdmin reports whether member userID holds the board-wide admin right,
// resolved as the pages resolve it. It does not look at the state of the
// account.
func holdsAdmin(ctx context.Context, q db.Querier, userID int64) (bool, error) {
	s, err := acldb.Load(ctx, q, userID)
	if errors.Is(err, acldb.ErrUnknownUser) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, err
	}
	return acl.Resolve(s).IsAdmin(), nil
}

// LockAdminSet takes the transaction lock of the writes that can take the admin
// right away from a member. Every such write takes it before it reads or
// changes anything, then calls [EnsureAdminRemains] after its change.
func LockAdminSet(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock($1)`, int64(adminSetLock)); err != nil {
		return fmt.Errorf("board: lock the admins: %w", err)
	}
	return nil
}

// EnsureAdminRemains returns [ErrLastAdmin] when no active member without a user
// ban in force holds the board-wide admin right, as the transaction tx sees the
// board. A write calls
// it after its change and before the commit, under [LockAdminSet], so the
// error rolls the change back.
func EnsureAdminRemains(ctx context.Context, tx pgx.Tx) error {
	// The candidates: owners, and members with a board-wide grant of admin, or of
	// a role that holds admin, of their own or through a confirmed group. The
	// resolver then decides, so a Never on any of them counts.
	rows, err := tx.Query(ctx, `
select u.id from users u
 where u.state = 'active'
   and not exists (select 1 from bans b where b.kind = 'user' and b.user_id = u.id
                                          and (b.ends_at is null or b.ends_at > now()))
   and (u.is_owner or exists (
        select 1
          from (select forum_id, role_id, option from acl_user_grants where user_id = u.id
                union all
                select gg.forum_id, gg.role_id, gg.option
                  from acl_group_grants gg
                  join group_members gm on gm.group_id = gg.group_id and gm.user_id = u.id and not gm.is_pending) g
          left join acl_role_settings rs on rs.role_id = g.role_id
         where g.forum_id = 0 and coalesce(g.option, rs.option) = 'admin'))
 order by u.id`)
	if err != nil {
		return fmt.Errorf("board: find the admins: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("board: find the admins: %w", err)
	}
	for _, id := range ids {
		ok, err := holdsAdmin(ctx, tx, id)
		if err != nil {
			return err
		}
		if ok {
			return nil
		}
	}
	return ErrLastAdmin
}

// targetIsOwner reports whether member userID is an owner, for the guards of a
// write.
func targetIsOwner(ctx context.Context, tx pgx.Tx, userID int64) (bool, error) {
	var owner bool
	if err := tx.QueryRow(ctx, `select is_owner from users where id = $1`, userID).Scan(&owner); err != nil {
		return false, fmt.Errorf("board: read member %d: %w", userID, err)
	}
	return owner, nil
}

// actorIsOwner reports whether the admin v is an owner.
func actorIsOwner(ctx context.Context, tx pgx.Tx, v Viewer) (bool, error) {
	return targetIsOwner(ctx, tx, v.UserID)
}

// guardOwner returns [ErrOwnerOnly] when member userID is an owner and the
// admin v is not.
func guardOwner(ctx context.Context, tx pgx.Tx, v Viewer, userID int64) error {
	owner, err := targetIsOwner(ctx, tx, userID)
	if err != nil || !owner {
		return err
	}
	if self, err := actorIsOwner(ctx, tx, v); err != nil || self {
		return err
	}
	return ErrOwnerOnly
}

// AdminResetPassword gives member userID a new random password and returns it,
// as [ResetPassword] does, with the admin v as the actor of the mod_log row
// ("user.reset_password", {"via":"web"}). An admin cannot reset their own
// password here ([ErrSelf]: the account page changes it), and only an owner can
// reset an owner's ([ErrOwnerOnly]), because the admin sees the new password.
// The password goes to the caller and nowhere else.
func AdminResetPassword(ctx context.Context, pool *pgxpool.Pool, v Viewer, userID int64, now time.Time) (string, error) {
	if userID == v.UserID {
		return "", ErrSelf
	}
	pw, hash, err := newHashedPassword()
	if err != nil {
		return "", err
	}
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := lockUser(ctx, tx, userID); err != nil {
			return Change{}, err
		}
		if err := guardOwner(ctx, tx, v, userID); err != nil {
			return Change{}, err
		}
		if err := storePassword(ctx, tx, userID, hash); err != nil {
			return Change{}, err
		}
		return Change{Action: "user.reset_password", TargetType: "user", TargetID: userID}, nil
	})
	if err != nil {
		return "", err
	}
	return pw, nil
}

// AdminSetActive makes the account of member userID active or inactive, as
// [ActivateUser] and [DeactivateUser] do, with the admin v as the actor of the
// mod_log row ("user.activate" or "user.deactivate", {"via":"web"}). Making an
// account inactive ends its sessions. The guards on a deactivate: not the
// admin's own account ([ErrSelf]), an owner only by an owner
// ([ErrOwnerOnly]), and never the last active admin ([ErrLastAdmin]).
func AdminSetActive(ctx context.Context, pool *pgxpool.Pool, v Viewer, userID int64, active bool, now time.Time) error {
	if !active && userID == v.UserID {
		return ErrSelf
	}
	action := "user.activate"
	if !active {
		action = "user.deactivate"
	}
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if !active {
			if err := LockAdminSet(ctx, tx); err != nil {
				return Change{}, err
			}
			if err := lockUser(ctx, tx, userID); err != nil {
				return Change{}, err
			}
			if err := guardOwner(ctx, tx, v, userID); err != nil {
				return Change{}, err
			}
		}
		if err := setActive(ctx, tx, userID, active); err != nil {
			return Change{}, err
		}
		if !active {
			if err := EnsureAdminRemains(ctx, tx); err != nil {
				return Change{}, err
			}
		}
		return Change{Action: action, TargetType: "user", TargetID: userID}, nil
	})
}

// AdminSetRank gives member userID the rank rankID, or with rankID 0 the rank
// of the post count, with one mod_log row ("user.rank", {"via":"web", "rank":
// rankID}). A rank that does not exist is an [*InputError] on "rank". A member
// who holds that rank already is [ErrNoChange].
func AdminSetRank(ctx context.Context, pool *pgxpool.Pool, v Viewer, userID, rankID int64, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := lockUser(ctx, tx, userID); err != nil {
			return Change{}, err
		}
		if rankID != 0 {
			var ok bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from ranks where id = $1)`, rankID).Scan(&ok); err != nil {
				return Change{}, fmt.Errorf("board: find rank %d: %w", rankID, err)
			}
			if !ok {
				return Change{}, inputError("rank", "Pick a rank from the list.")
			}
		}
		tag, err := tx.Exec(ctx, `update users set rank_id = $2 where id = $1 and rank_id is distinct from $2`, userID, nullID(rankID))
		if err != nil {
			return Change{}, fmt.Errorf("board: set the rank of %d: %w", userID, err)
		}
		if tag.RowsAffected() == 0 {
			return Change{}, ErrNoChange
		}
		return Change{Action: "user.rank", TargetType: "user", TargetID: userID, Details: map[string]any{"rank": rankID}}, nil
	})
}

// AdminVerifyEmail marks the email address of member userID verified, with one
// mod_log row ("user.verify_email", {"via":"web"}). An address that is
// verified already is [ErrNoChange].
func AdminVerifyEmail(ctx context.Context, pool *pgxpool.Pool, v Viewer, userID int64, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		if err := lockUser(ctx, tx, userID); err != nil {
			return Change{}, err
		}
		tag, err := tx.Exec(ctx, `update users set email_verified = true where id = $1 and not email_verified`, userID)
		if err != nil {
			return Change{}, fmt.Errorf("board: verify the address of %d: %w", userID, err)
		}
		if tag.RowsAffected() == 0 {
			return Change{}, ErrNoChange
		}
		return Change{Action: "user.verify_email", TargetType: "user", TargetID: userID}, nil
	})
}
