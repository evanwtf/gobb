package board_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// spamState is every row and counter that the Spammer action and its Undo
// touch, as text, except the bans and the sessions.
func spamState(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var s string
	if err := pool.QueryRow(context.Background(), `
select concat_ws(E'\n',
  (select string_agg(concat_ws(' ', id, visibility, visibility_before_delete), ',' order by id) from posts),
  (select string_agg(concat_ws(' ', id, visibility, visibility_before_delete, reply_count, last_post_id, last_post_at), ',' order by id) from topics),
  (select string_agg(concat_ws(' ', id, topic_count, post_count, last_post_id), ',' order by id) from forums),
  (select string_agg(concat_ws(' ', id, post_count), ',' order by id) from users),
  (select string_agg(concat_ws(' ', pm_id, user_id, is_read, is_deleted), ',' order by pm_id, user_id) from pm_recipients),
  (select string_agg(concat_ws(' ', user_id, ip, last_seen_at), ',' order by user_id, ip) from user_ips))`).Scan(&s); err != nil {
		t.Fatal(err)
	}
	return s
}

// seedSpam gives bob addresses (two recent, one old, one that the owner alice
// used too) and a private message to carol.
func seedSpam(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	old := t0.Add(-40 * 24 * time.Hour)
	mustExec(t, pool, `insert into user_ips (user_id, ip, last_seen_at) values
	  (2, '192.0.2.7', $1), (2, '2001:db8:1:2::5', $1), (2, '198.51.100.9', $2), (2, '203.0.113.4', $1), (1, '203.0.113.4', $1), (2, '198.18.0.1', $1)`, t0, old)
	mustExec(t, pool, `insert into pms (author_id, author_name, created_at, subject, body_tree, conversation_id)
	  values (2, 'bob', $1, 'Buy now', '{}', 0)`, t0)
	mustExec(t, pool, `update pms set conversation_id = id where conversation_id = 0`)
	mustExec(t, pool, `insert into pm_recipients (pm_id, user_id, kind) select max(id), 3, 'to' from pms`)
}

func TestSpammerThenUndo(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSpam(t, pool)
	seedSessions(t, pool)
	before := spamState(t, pool)
	var bansBefore int
	if err := pool.QueryRow(ctx, `select count(*) from bans`).Scan(&bansBefore); err != nil {
		t.Fatal(err)
	}

	plan, err := board.PlanSpammer(ctx, pool, bob, t0)
	if err != nil {
		t.Fatal(err)
	}
	if plan != (board.SpammerPlan{Posts: 2, Topics: 1, Messages: 1, Addresses: 5}) {
		t.Errorf("plan = %+v", plan)
	}

	a := viewerFor(t, pool, alice)
	adminIP := netip.MustParseAddr("198.18.0.1")
	logID, err := board.MarkSpammer(ctx, pool, a, bob, adminIP, t0)
	if err != nil {
		t.Fatal(err)
	}

	// The posts of bob and his topic are deleted; carol's message is gone.
	var postsLeft, topicsLeft, inbox, ips, sessions int
	if err := pool.QueryRow(ctx, `
select (select count(*) from posts where author_id = 2 and visibility <> 'deleted'),
       (select count(*) from topics where author_id = 2 and visibility <> 'deleted'),
       (select count(*) from pm_recipients where user_id = 3 and not is_deleted and pm_id = (select max(id) from pms)),
       (select count(*) from user_ips where user_id = 2),
       (select count(*) from sessions where user_id = 2)`).Scan(&postsLeft, &topicsLeft, &inbox, &ips, &sessions); err != nil {
		t.Fatal(err)
	}
	if postsLeft+topicsLeft+inbox+ips+sessions != 0 {
		t.Errorf("after the action: %d posts, %d topics, %d inbox copies, %d addresses, %d sessions; want 0 each",
			postsLeft, topicsLeft, inbox, ips, sessions)
	}
	// The bans: the user, the email address, 192.0.2.7/32 and 2001:db8:1:2::/64.
	// 198.51.100.9 is too old. 203.0.113.4 and 10.1.2.3 (from seedSessions) are
	// addresses of the owner too, and the admin acts from 198.18.0.1.
	rows, err := pool.Query(ctx, `select concat(kind, ' ', coalesce(user_id::text, email, ip::text)) from bans where undo_log_id = $1 order by 1`, logID)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		got = append(got, s)
	}
	rows.Close()
	want := []string{"email bob@example.com", "ip 192.0.2.7/32", "ip 2001:db8:1:2::/64", "user 2"}
	if len(got) != len(want) {
		t.Fatalf("bans = %q, want %q", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("ban %d = %q, want %q", i, got[i], want[i])
		}
	}
	var details string
	if err := pool.QueryRow(ctx, `select details::text from mod_log where id = $1 and action = 'user.spammer' and undo_until = $2`,
		logID, t0.Add(30*24*time.Hour)).Scan(&details); err != nil {
		t.Fatalf("the log row: %v", err)
	}

	if err := board.UndoSpammer(ctx, pool, a, logID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if after := spamState(t, pool); after != before {
		t.Errorf("after the undo:\n%s\nbefore:\n%s", after, before)
	}
	var bansAfter int
	if err := pool.QueryRow(ctx, `select count(*) from bans`).Scan(&bansAfter); err != nil {
		t.Fatal(err)
	}
	if bansAfter != bansBefore {
		t.Errorf("%d bans after the undo, want %d", bansAfter, bansBefore)
	}
	var undone int
	if err := pool.QueryRow(ctx, `select count(*) from mod_log where (id = $1 and undone_at is not null) or (action = 'user.spammer_undo' and target_id = 2)`,
		logID).Scan(&undone); err != nil {
		t.Fatal(err)
	}
	if undone != 2 {
		t.Errorf("%d log rows mark the undo, want 2", undone)
	}
	if err := board.UndoSpammer(ctx, pool, a, logID, t0.Add(2*time.Hour)); !errors.Is(err, board.ErrUndoClosed) {
		t.Errorf("second undo: err = %v, want ErrUndoClosed", err)
	}
	var kept int
	if err := pool.QueryRow(ctx, `select count(*) from mod_undo`).Scan(&kept); err != nil {
		t.Fatal(err)
	}
	if kept != 0 {
		t.Errorf("%d undo rows after the undo, want 0", kept)
	}
}

// The cleanup drops the undo rows once the 30 days end, and not before.
func TestCleanupUndo(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	seedSpam(t, pool)
	if _, err := board.MarkSpammer(ctx, pool, viewerFor(t, pool, alice), bob, netip.Addr{}, t0); err != nil {
		t.Fatal(err)
	}
	if n, err := board.CleanupUndo(ctx, pool, t0.Add(29*24*time.Hour)); err != nil || n != 0 {
		t.Errorf("cleanup on day 29 = %d, %v; want 0", n, err)
	}
	if n, err := board.CleanupUndo(ctx, pool, t0.Add(30*24*time.Hour)); err != nil || n == 0 {
		t.Errorf("cleanup on day 30 = %d, %v; want every row", n, err)
	}
	var left int
	if err := pool.QueryRow(ctx, `select count(*) from mod_undo`).Scan(&left); err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Errorf("%d undo rows left", left)
	}
}

// The Undo keeps what changed after the action: a ban that an admin lifted, and
// a post that a moderator restored, stay as they are.
func TestSpammerUndoAfterChanges(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	logID, err := board.MarkSpammer(ctx, pool, a, bob, netip.Addr{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	var banID int64
	if err := pool.QueryRow(ctx, `select id from bans where undo_log_id = $1 and kind = 'user'`, logID).Scan(&banID); err != nil {
		t.Fatal(err)
	}
	if err := board.LiftBan(ctx, pool, a, banID, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A moderator restores post 2.
	mustExec(t, pool, `update posts set visibility = 'approved', visibility_before_delete = null where id = 2`)
	if err := board.UndoSpammer(ctx, pool, a, logID, t0.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	var vis2, vis5, topic2 string
	var postCount int
	if err := pool.QueryRow(ctx, `select (select visibility from posts where id = 2), (select visibility from posts where id = 5),
	       (select visibility from topics where id = 2), (select post_count from users where id = 2)`).Scan(&vis2, &vis5, &topic2, &postCount); err != nil {
		t.Fatal(err)
	}
	if vis2 != "approved" || vis5 != "approved" || topic2 != "approved" {
		t.Errorf("posts 2 and 5, topic 2 = %s, %s, %s; want approved", vis2, vis5, topic2)
	}
}

func TestSpammerRefusals(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a, b := viewerFor(t, pool, alice), viewerFor(t, pool, bob)
	for _, c := range []struct {
		name   string
		v      board.Viewer
		target int64
		want   error
	}{
		{"not an admin", b, carol, board.ErrForbidden},
		{"self", a, alice, board.ErrSelf},
		{"no such member", a, 999, board.ErrNotFound},
	} {
		if _, err := board.MarkSpammer(ctx, pool, c.v, c.target, netip.Addr{}, t0); !errors.Is(err, c.want) {
			t.Errorf("%s: err = %v, want %v", c.name, err, c.want)
		}
	}
	// An owner cannot be marked, even by another admin.
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (2, 0, 'admin', 3)`)
	b = viewerFor(t, pool, bob)
	if _, err := board.MarkSpammer(ctx, pool, b, alice, netip.Addr{}, t0); !errors.Is(err, board.ErrOwnerOnly) {
		t.Errorf("owner: err = %v, want ErrOwnerOnly", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from mod_log where action = 'user.spammer'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("%d spammer rows after refusals, want 0", n)
	}
	// The undo is closed after 30 days, and needs an admin.
	logID, err := board.MarkSpammer(ctx, pool, a, carol, netip.Addr{}, t0)
	if err != nil {
		t.Fatal(err)
	}
	if err := board.UndoSpammer(ctx, pool, viewerFor(t, pool, dave), logID, t0); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("undo by a member: err = %v, want ErrForbidden", err)
	}
	if err := board.UndoSpammer(ctx, pool, a, logID, t0.Add(30*24*time.Hour)); !errors.Is(err, board.ErrUndoClosed) {
		t.Errorf("undo after 30 days: err = %v, want ErrUndoClosed", err)
	}
	if err := board.UndoSpammer(ctx, pool, a, 1_000_000, t0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("undo of a missing row: err = %v, want ErrNotFound", err)
	}
}

// A Spammer action that would ban the last admin is refused, and changes
// nothing.
func TestSpammerKeepsAnAdmin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// carol is an admin, and alice, the owner, is inactive: carol is the one
	// admin who can sign in.
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 0, 'admin', 3)`)
	a := viewerFor(t, pool, alice)
	mustExec(t, pool, `update users set state = 'inactive' where id = 1`)
	before := spamState(t, pool)
	if _, err := board.MarkSpammer(ctx, pool, a, carol, netip.Addr{}, t0); !errors.Is(err, board.ErrLastAdmin) {
		t.Fatalf("err = %v, want ErrLastAdmin", err)
	}
	if after := spamState(t, pool); after != before {
		t.Errorf("a refused action changed rows")
	}
}

func TestHeldPostsOnlyWhereTheViewerApproves(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Post 3 is held in forum 2. Hold post 6 in forum 3 too.
	mustExec(t, pool, `update posts set visibility = 'unapproved' where id = 6`)
	// carol may approve in forum 2 only; bob, a moderator, everywhere he reads.
	mustExec(t, pool, `insert into acl_user_grants (user_id, forum_id, option, setting) values (3, 2, 'mod.approve', 3)`)
	ids := func(v board.Viewer) []int64 {
		t.Helper()
		q, err := board.HeldPosts(ctx, pool, v, 1)
		if err != nil {
			t.Fatal(err)
		}
		var out []int64
		for _, p := range q.Posts {
			out = append(out, p.ID)
		}
		return out
	}
	if got := ids(viewerFor(t, pool, carol)); len(got) != 1 || got[0] != 3 {
		t.Errorf("carol's queue = %v, want [3]", got)
	}
	if got := ids(viewerFor(t, pool, bob)); len(got) != 2 || got[0] != 6 || got[1] != 3 {
		t.Errorf("bob's queue = %v, want [6 3], oldest first", got)
	}
	if _, err := board.HeldPosts(ctx, pool, viewerFor(t, pool, dave), 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("dave: err = %v, want ErrForbidden", err)
	}
	if _, err := board.HeldPosts(ctx, pool, asGuest(t, pool), 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest: err = %v, want ErrForbidden", err)
	}
	// A held post in a deleted topic is not in the queue.
	mustExec(t, pool, `update topics set visibility = 'deleted' where id = 2`)
	if got := ids(viewerFor(t, pool, bob)); len(got) != 1 || got[0] != 3 {
		t.Errorf("bob's queue with topic 2 deleted = %v, want [3]", got)
	}
}
