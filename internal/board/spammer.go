package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/counters"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/session"
)

// The Spammer action (design section 11) and its Undo. One click bans a member,
// the member's email address and each address that the member used in the last
// 30 days, deletes the member's posts, hides the topics that the member started
// with their replies, and takes the member's private messages out of every
// inbox. All deletes are soft. The mod_log row of the action has an Undo for 30
// days; the mod_undo rows of the log row list what the Undo puts back.

const (
	// spammerAction is the mod_log action of the Spammer action, and
	// spammerUndoAction the action of its Undo.
	spammerAction     = "user.spammer"
	spammerUndoAction = "user.spammer_undo"
	// spammerUndoFor is how long the Undo of a Spammer action stays open.
	spammerUndoFor = 30 * 24 * time.Hour
	// spammerIPsFor is how far back the action reads the addresses to ban.
	spammerIPsFor = 30 * 24 * time.Hour
)

// ErrUndoClosed means that the Undo of a log row is no longer open: it ran
// already, or its 30 days ended.
var ErrUndoClosed = errors.New("board: the undo is closed")

// SpammerPlan is what the Spammer action would change for one member, for the
// confirm page.
type SpammerPlan struct {
	// Posts counts the posts that the action deletes, Topics the topics that it
	// hides, and Messages the inbox copies of private messages that it removes.
	Posts, Topics, Messages int
	// Addresses counts the addresses of the last 30 days, before the action
	// leaves out the ones it may not ban.
	Addresses int
	// Banned is true when a user ban holds the member already.
	Banned bool
}

// PlanSpammer returns what the Spammer action would change for member userID.
// A member who does not exist is [ErrNotFound]. It does not check the viewer:
// the caller is the admin area, behind its gate.
func PlanSpammer(ctx context.Context, q db.Querier, userID int64, now time.Time) (SpammerPlan, error) {
	var p SpammerPlan
	err := q.QueryRow(ctx, `
select (select count(*) from posts where author_id = u.id and visibility <> 'deleted'),
       (select count(*) from topics where author_id = u.id and visibility <> 'deleted'),
       (select count(*) from pm_recipients r join pms m on m.id = r.pm_id where m.author_id = u.id and not r.is_deleted),
       (select count(*) from user_ips where user_id = u.id and last_seen_at > $2),
       exists (select 1 from bans where kind = 'user' and user_id = u.id and (ends_at is null or ends_at > $3))
  from users u where u.id = $1`, userID, now.Add(-spammerIPsFor), now).
		Scan(&p.Posts, &p.Topics, &p.Messages, &p.Addresses, &p.Banned)
	if errors.Is(err, pgx.ErrNoRows) {
		return SpammerPlan{}, ErrNotFound
	}
	if err != nil {
		return SpammerPlan{}, fmt.Errorf("board: plan the spammer action for %d: %w", userID, err)
	}
	return p, nil
}

// MarkSpammer runs the Spammer action of the admin v on member userID and
// returns the ID of its mod_log row ("user.spammer", target type "user"), with
// an Undo open for 30 days. In one transaction it:
//
//   - adds a user ban with no end, unless one holds the member already, and ends
//     every session of the member;
//   - bans the clean email address of the member, unless a ban holds it
//     already or it is the address of v or of an owner;
//   - bans each address that the member used in the last 30 days, exactly: a
//     /32 for IPv4 and a /64 for IPv6. It leaves out a range that holds adminIP
//     or an address that an owner has used, and a range that a ban holds
//     already;
//   - removes the addresses of the member (user_ips), as a ban does;
//   - deletes every post of the member that is not deleted, and every topic
//     that the member started; the replies of others in those topics stay as
//     they are, hidden with the topic;
//   - marks every private message of the member deleted for each recipient;
//   - sets the counters of each topic and forum that changed, and the post
//     count of the member.
//
// A viewer without the board-wide admin right gets [ErrForbidden]. A member who
// does not exist is [ErrNotFound]. The member v is [ErrSelf], and an owner is
// [ErrOwnerOnly]. An action that would leave no active admin who can sign in is
// [ErrLastAdmin]. Each error rolls everything back.
func MarkSpammer(ctx context.Context, pool *pgxpool.Pool, v Viewer, userID int64, adminIP netip.Addr, now time.Time) (int64, error) {
	if v.UserID == 0 || !v.Perms.IsAdmin() {
		return 0, ErrForbidden
	}
	if userID == v.UserID {
		return 0, ErrSelf
	}
	var logID int64
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := LockAdminSet(ctx, tx); err != nil {
			return err
		}
		if err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		var (
			owner bool
			email string
		)
		if err := tx.QueryRow(ctx, `select is_owner, email_clean from users where id = $1`, userID).Scan(&owner, &email); err != nil {
			return fmt.Errorf("board: read member %d: %w", userID, err)
		}
		if owner {
			return ErrOwnerOnly
		}
		if err := tx.QueryRow(ctx, `
insert into mod_log (actor_id, action, target_type, target_id, details, created_at, undo_until)
values ($1, $2, 'user', $3, '{}', $4, $5) returning id`,
			v.UserID, spammerAction, userID, now, now.Add(spammerUndoFor)).Scan(&logID); err != nil {
			return fmt.Errorf("board: write the moderation log: %w", err)
		}
		bans, err := spammerBans(ctx, tx, v, logID, userID, email, adminIP, now)
		if err != nil {
			return err
		}
		if err := EnsureAdminRemains(ctx, tx); err != nil {
			return err
		}
		posts, topics, err := spammerHide(ctx, tx, v, logID, userID)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
with gone as (
  update pm_recipients r set is_deleted = true
    from pms m
   where m.id = r.pm_id and m.author_id = $2 and not r.is_deleted
  returning r.pm_id, r.user_id)
insert into mod_undo (log_id, kind, row_id, user_id)
select $1, 'pm_recipient', pm_id, user_id from gone`, logID, userID)
		if err != nil {
			return fmt.Errorf("board: remove the messages of %d: %w", userID, err)
		}
		details, err := json.Marshal(map[string]any{"via": "web", "bans": bans, "posts": posts, "topics": topics,
			"messages": tag.RowsAffected()})
		if err != nil {
			return fmt.Errorf("board: encode log details: %w", err)
		}
		if _, err := tx.Exec(ctx, `update mod_log set details = $2::jsonb where id = $1`, logID, string(details)); err != nil {
			return fmt.Errorf("board: write the moderation log: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return logID, nil
}

// spammerBans writes the bans of a Spammer action, each with the log ID logID,
// and removes the addresses and the sessions of the member. It keeps the
// addresses in mod_undo first. It returns the count of bans that it added.
func spammerBans(ctx context.Context, tx pgx.Tx, v Viewer, logID, userID int64, email string, adminIP netip.Addr, now time.Time) (int, error) {
	if _, err := tx.Exec(ctx, `
insert into mod_undo (log_id, kind, user_id, ip, seen_at)
select $1, 'user_ip', user_id, ip, last_seen_at from user_ips where user_id = $2`, logID, userID); err != nil {
		return 0, fmt.Errorf("board: keep the addresses of %d: %w", userID, err)
	}
	rows, err := tx.Query(ctx, `select host(ip) from user_ips where user_id = $1 and last_seen_at > $2 order by ip`,
		userID, now.Add(-spammerIPsFor))
	if err != nil {
		return 0, fmt.Errorf("board: read the addresses of %d: %w", userID, err)
	}
	ips, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return 0, fmt.Errorf("board: read the addresses of %d: %w", userID, err)
	}

	added := 0
	banID, err := banUserTx(ctx, tx, userBan{userID: userID, reason: "Spammer", createdBy: v.UserID, refuseOwner: true}, now)
	switch {
	case errors.Is(err, ErrAlreadyBanned):
		// The ban in force stays the member's ban, and the Undo leaves it. The
		// sessions and the addresses go as a new ban takes them.
		if _, err := session.DeleteUser(ctx, tx, userID); err != nil {
			return 0, err
		}
		if _, err := tx.Exec(ctx, `delete from user_ips where user_id = $1`, userID); err != nil {
			return 0, fmt.Errorf("board: remove the addresses of %d: %w", userID, err)
		}
	case err != nil:
		return 0, err
	default:
		added++
		if _, err := tx.Exec(ctx, `update bans set undo_log_id = $1 where id = $2`, logID, banID); err != nil {
			return 0, fmt.Errorf("board: mark the ban of %d: %w", userID, err)
		}
	}

	ok, err := spammerEmailBannable(ctx, tx, v, email, now)
	if err != nil {
		return 0, err
	}
	if ok {
		if _, err := tx.Exec(ctx, `
insert into bans (kind, email, reason, created_at, created_by, undo_log_id) values ('email', $1, 'Spammer', $2, $3, $4)`,
			email, now, v.UserID, logID); err != nil {
			return 0, fmt.Errorf("board: ban an address: %w", err)
		}
		added++
	}

	var ranges []netip.Prefix
	for _, text := range ips {
		a, err := netip.ParseAddr(text)
		if err != nil {
			continue
		}
		a = a.Unmap().WithZone("")
		bits := 32
		if a.Is6() {
			bits = 64
		}
		p := netip.PrefixFrom(a, bits).Masked()
		if slices.Contains(ranges, p) {
			continue
		}
		ranges = append(ranges, p)
	}
	for _, p := range ranges {
		if adminIP.IsValid() && p.Contains(adminIP.Unmap().WithZone("")) {
			continue
		}
		var skip bool
		if err := tx.QueryRow(ctx, `
select exists (select 1 from user_ips ui join users u on u.id = ui.user_id where u.is_owner and ui.ip <<= $1::cidr)
    or exists (select 1 from bans where kind = 'ip' and ip = $1::cidr and (ends_at is null or ends_at > $2))`,
			p.String(), now).Scan(&skip); err != nil {
			return 0, fmt.Errorf("board: check a range to ban: %w", err)
		}
		if skip {
			continue
		}
		if _, err := tx.Exec(ctx, `
insert into bans (kind, ip, reason, created_at, created_by, undo_log_id) values ('ip', $1::cidr, 'Spammer', $2, $3, $4)`,
			p.String(), now, v.UserID, logID); err != nil {
			return 0, fmt.Errorf("board: ban a range: %w", err)
		}
		added++
	}
	return added, nil
}

// spammerEmailBannable reports whether the Spammer action may ban the clean
// address email: it is not empty, not the address of v or of an owner, and no
// ban in force holds it. The bans are compared with [account.CleanEmail], as
// sign-in compares them.
func spammerEmailBannable(ctx context.Context, tx pgx.Tx, v Viewer, email string, now time.Time) (bool, error) {
	if email == "" {
		return false, nil
	}
	var guarded bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from users where email_clean = $1 and (id = $2 or is_owner))`,
		email, v.UserID).Scan(&guarded); err != nil {
		return false, fmt.Errorf("board: check the owners for a ban: %w", err)
	}
	if guarded {
		return false, nil
	}
	rows, err := tx.Query(ctx, `select email from bans where kind = 'email' and email is not null and (ends_at is null or ends_at > $1)`, now)
	if err != nil {
		return false, fmt.Errorf("board: read the email bans: %w", err)
	}
	held, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return false, fmt.Errorf("board: read the email bans: %w", err)
	}
	for _, e := range held {
		if account.CleanEmail(e) == email {
			return false, nil
		}
	}
	return true, nil
}

// spammerHide deletes the posts and the topics of member userID for the Spammer
// action logID, keeps each one in mod_undo, and sets the counters. It returns
// the counts of posts and topics that it deleted.
func spammerHide(ctx context.Context, tx pgx.Tx, v Viewer, logID, userID int64) (posts, topics int, err error) {
	// The forums of the rows change their counters, so lock them first, in
	// order, as every other write takes one forum.
	rows, err := tx.Query(ctx, `
select forum_id from posts where author_id = $1 and visibility <> 'deleted'
union
select forum_id from topics where author_id = $1 and visibility <> 'deleted'
order by 1`, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("board: read the forums of %d: %w", userID, err)
	}
	forums, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return 0, 0, fmt.Errorf("board: read the forums of %d: %w", userID, err)
	}
	for _, f := range forums {
		if _, _, err := lockForum(ctx, tx, f); err != nil {
			return 0, 0, err
		}
	}
	// The post count drops by the approved posts in the forums where the member
	// holds post.counts, as a delete of each one would.
	drop := 0
	rows, err = tx.Query(ctx, `select forum_id, count(*) from posts where author_id = $1 and visibility = 'approved' group by 1`, userID)
	if err != nil {
		return 0, 0, fmt.Errorf("board: count the posts of %d: %w", userID, err)
	}
	type forumCount struct {
		forum int64
		n     int
	}
	counted, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (forumCount, error) {
		var c forumCount
		return c, r.Scan(&c.forum, &c.n)
	})
	if err != nil {
		return 0, 0, fmt.Errorf("board: count the posts of %d: %w", userID, err)
	}
	for _, c := range counted {
		ok, err := authorCounts(ctx, tx, v, userID, c.forum)
		if err != nil {
			return 0, 0, err
		}
		if ok {
			drop += c.n
		}
	}

	var changed []int64
	for _, w := range []struct {
		table, kind string
		n           *int
	}{{"posts", "post", &posts}, {"topics", "topic", &topics}} {
		rows, err := tx.Query(ctx, `
with gone as (
  update `+w.table+` set visibility = 'deleted', visibility_before_delete = visibility
   where author_id = $2 and visibility <> 'deleted'
  returning id, `+map[string]string{"posts": "topic_id", "topics": "id"}[w.table]+` as topic_id),
kept as (insert into mod_undo (log_id, kind, row_id) select $1, '`+w.kind+`', id from gone)
select topic_id from gone`, logID, userID)
		if err != nil {
			return 0, 0, fmt.Errorf("board: delete the %s of %d: %w", w.table, userID, err)
		}
		ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
		if err != nil {
			return 0, 0, fmt.Errorf("board: delete the %s of %d: %w", w.table, userID, err)
		}
		*w.n = len(ids)
		changed = append(changed, ids...)
	}
	if err := recount(ctx, tx, changed, forums); err != nil {
		return 0, 0, err
	}
	if drop > 0 {
		if err := addPostCount(ctx, tx, userID, -drop); err != nil {
			return 0, 0, err
		}
	}
	return posts, topics, nil
}

// recount sets the counters of the topics and then of the forums.
func recount(ctx context.Context, tx pgx.Tx, topics, forums []int64) error {
	slices.Sort(topics)
	for _, t := range slices.Compact(topics) {
		if err := counters.Topic(ctx, tx, t); err != nil {
			return err
		}
	}
	for _, f := range forums {
		if err := counters.Forum(ctx, tx, f); err != nil {
			return err
		}
	}
	return nil
}

// UndoSpammer undoes the Spammer action of mod_log row logID for the admin v.
// In one transaction it lifts the bans of the action that are still in force,
// puts back the addresses of the member, restores each post and topic that the
// action deleted and that is still deleted, puts the private messages back in
// the inboxes, and sets the counters again. It sets undone_at on the row and
// writes one mod_log row ("user.spammer_undo", target type "user", details
// with the log ID). The sessions of the member stay ended.
//
// A viewer without the board-wide admin right gets [ErrForbidden]. A row that
// does not exist or is no Spammer action is [ErrNotFound]. A row that was
// undone already, or whose 30 days ended, is [ErrUndoClosed].
func UndoSpammer(ctx context.Context, pool *pgxpool.Pool, v Viewer, logID int64, now time.Time) error {
	if v.UserID == 0 || !v.Perms.IsAdmin() {
		return ErrForbidden
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		var (
			userID    int64
			undoUntil *time.Time
			undoneAt  *time.Time
		)
		err := tx.QueryRow(ctx, `select target_id, undo_until, undone_at from mod_log where id = $1 and action = $2 for update`,
			logID, spammerAction).Scan(&userID, &undoUntil, &undoneAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: read log row %d: %w", logID, err)
		}
		if undoneAt != nil || undoUntil == nil || !now.Before(*undoUntil) {
			return ErrUndoClosed
		}
		if err := lockUser(ctx, tx, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `delete from bans where undo_log_id = $1 and (ends_at is null or ends_at > $2)`, logID, now); err != nil {
			return fmt.Errorf("board: lift the bans of log row %d: %w", logID, err)
		}
		if _, err := tx.Exec(ctx, `
insert into user_ips (user_id, ip, last_seen_at)
select user_id, ip, seen_at from mod_undo where log_id = $1 and kind = 'user_ip'
on conflict (user_id, ip) do update set last_seen_at = greatest(user_ips.last_seen_at, excluded.last_seen_at)`, logID); err != nil {
			return fmt.Errorf("board: put back the addresses of %d: %w", userID, err)
		}
		if _, err := tx.Exec(ctx, `
update pm_recipients r set is_deleted = false
  from mod_undo u
 where u.log_id = $1 and u.kind = 'pm_recipient' and r.pm_id = u.row_id and r.user_id = u.user_id and r.is_deleted`, logID); err != nil {
			return fmt.Errorf("board: put back the messages of %d: %w", userID, err)
		}
		if err := spammerRestore(ctx, tx, v, logID, userID); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `update mod_log set undone_at = $2 where id = $1`, logID, now); err != nil {
			return fmt.Errorf("board: close the undo of log row %d: %w", logID, err)
		}
		// The rows have done their job, and they hold addresses.
		if _, err := tx.Exec(ctx, `delete from mod_undo where log_id = $1`, logID); err != nil {
			return fmt.Errorf("board: drop the undo rows of log row %d: %w", logID, err)
		}
		return logMod(ctx, tx, v, spammerUndoAction, "user", userID, fmt.Sprintf(`{"via":"web","log":%d}`, logID), now)
	})
}

// spammerRestore restores the posts and the topics of the Spammer action logID
// that are still deleted, each in the state it had before, sets the counters,
// and gives the post count of member userID back.
func spammerRestore(ctx context.Context, tx pgx.Tx, v Viewer, logID, userID int64) error {
	rows, err := tx.Query(ctx, `
select p.forum_id from posts p join mod_undo u on u.row_id = p.id
 where u.log_id = $1 and u.kind = 'post' and p.visibility = 'deleted'
union
select t.forum_id from topics t join mod_undo u on u.row_id = t.id
 where u.log_id = $1 and u.kind = 'topic' and t.visibility = 'deleted'
order by 1`, logID)
	if err != nil {
		return fmt.Errorf("board: read the forums of log row %d: %w", logID, err)
	}
	forums, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return fmt.Errorf("board: read the forums of log row %d: %w", logID, err)
	}
	for _, f := range forums {
		if _, _, err := lockForum(ctx, tx, f); err != nil {
			return err
		}
	}
	var changed []int64
	add := map[int64]int{}
	for _, w := range []struct{ table, kind, topic string }{{"posts", "post", "x.topic_id"}, {"topics", "topic", "x.id"}} {
		rows, err := tx.Query(ctx, `
update `+w.table+` x set visibility = coalesce(x.visibility_before_delete, 'unapproved'), visibility_before_delete = null
  from mod_undo u
 where u.log_id = $1 and u.kind = '`+w.kind+`' and x.id = u.row_id and x.visibility = 'deleted'
returning `+w.topic+`, x.forum_id, x.visibility = 'approved'`, logID)
		if err != nil {
			return fmt.Errorf("board: restore the %s of log row %d: %w", w.table, logID, err)
		}
		for rows.Next() {
			var (
				topic, forum int64
				approved     bool
			)
			if err := rows.Scan(&topic, &forum, &approved); err != nil {
				rows.Close()
				return fmt.Errorf("board: restore the %s of log row %d: %w", w.table, logID, err)
			}
			changed = append(changed, topic)
			if w.kind == "post" && approved {
				add[forum]++
			}
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("board: restore the %s of log row %d: %w", w.table, logID, err)
		}
	}
	if err := recount(ctx, tx, changed, forums); err != nil {
		return err
	}
	gain := 0
	for forum, n := range add {
		ok, err := authorCounts(ctx, tx, v, userID, forum)
		if err != nil {
			return err
		}
		if ok {
			gain += n
		}
	}
	if gain > 0 {
		return addPostCount(ctx, tx, userID, gain)
	}
	return nil
}

// CleanupUndo deletes the mod_undo rows of every log row whose Undo closed by
// now: its 30 days ended. They hold the addresses of the member, which must not
// outlive their use. It returns how many rows it deleted.
func CleanupUndo(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx, `
delete from mod_undo u using mod_log l
 where l.id = u.log_id and (l.undone_at is not null or l.undo_until is null or l.undo_until <= $1)`, now)
	if err != nil {
		return 0, fmt.Errorf("board: clean up the undo rows: %w", err)
	}
	return tag.RowsAffected(), nil
}
