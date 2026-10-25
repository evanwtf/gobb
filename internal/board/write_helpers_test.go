package board_test

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/counters"
)

// The times and the address that the write tests use. The fixture's own rows are
// older than t0.
var (
	t0  = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	ip1 = netip.MustParseAddr("10.1.2.3")
	ip2 = netip.MustParseAddr("10.1.2.4")
)

const (
	alice = int64(1) // owner and admin; a plain member in forum 2
	bob   = int64(2) // moderator: mod.edit and mod.approve board-wide
	carol = int64(3) // plain member
)

// setGrant stores one option for a user in a forum (0 is board-wide). setting is
// 3 for yes and 2 for never.
func setGrant(t *testing.T, pool *pgxpool.Pool, user, forum int64, setting int, options ...string) {
	t.Helper()
	for _, o := range options {
		if _, err := pool.Exec(context.Background(),
			`insert into acl_user_grants (user_id, forum_id, option, setting) values ($1, $2, $3, $4)`,
			user, forum, o, setting); err != nil {
			t.Fatal(err)
		}
	}
}

func grantOpts(t *testing.T, pool *pgxpool.Pool, user, forum int64, options ...string) {
	t.Helper()
	setGrant(t, pool, user, forum, 3, options...)
}

func denyOpts(t *testing.T, pool *pgxpool.Pool, user, forum int64, options ...string) {
	t.Helper()
	setGrant(t, pool, user, forum, 2, options...)
}

// allowGuests gives the guests group the options in a forum.
func allowGuests(t *testing.T, pool *pgxpool.Pool, forum int64, options ...string) {
	t.Helper()
	for _, o := range options {
		if _, err := pool.Exec(context.Background(),
			`insert into acl_group_grants (group_id, forum_id, option, setting)
			 select id, $1, $2, 3 from groups where builtin = 'guests'`, forum, o); err != nil {
			t.Fatal(err)
		}
	}
}

// as returns the viewer for a member with the rights that the database holds now.
func viewerFor(t *testing.T, pool *pgxpool.Pool, id int64) board.Viewer {
	t.Helper()
	var name string
	if err := pool.QueryRow(context.Background(), `select username from users where id = $1`, id).Scan(&name); err != nil {
		t.Fatal(err)
	}
	return memberOn(t, pool, id, name)
}

// asGuest returns the guest with the rights that the database holds now.
func asGuest(t *testing.T, pool *pgxpool.Pool) board.Viewer {
	t.Helper()
	return guestOn(t, pool)
}

// modOn gives a member every moderator right in forum 2 and returns the viewer.
func moderator(t *testing.T, pool *pgxpool.Pool, id int64) board.Viewer {
	t.Helper()
	grantOpts(t, pool, id, 2, "mod.edit", "mod.delete", "mod.approve", "mod.lock")
	return viewerFor(t, pool, id)
}

// openMessage reads message id as a person who opens its page does: it peeks, and
// then marks the message read for the viewer (see board.PeekMessage and
// board.MarkMessageRead).
func openMessage(ctx context.Context, pool *pgxpool.Pool, v board.Viewer, id int64) (board.Message, error) {
	m, err := board.PeekMessage(ctx, pool, v, id)
	if err != nil {
		return board.Message{}, err
	}
	if err := board.MarkMessageRead(ctx, pool, v, id); err != nil {
		return board.Message{}, err
	}
	return m, nil
}

func wantIs(t *testing.T, err, target error, what string) {
	t.Helper()
	if !errors.Is(err, target) {
		t.Errorf("%s: error = %v, want %v", what, err, target)
	}
}

func wantInput(t *testing.T, err error, field, message string) {
	t.Helper()
	var ie *board.InputError
	if !errors.As(err, &ie) {
		t.Fatalf("error = %v, want an *InputError on %q", err, field)
	}
	if ie.Field != field {
		t.Errorf("InputError.Field = %q, want %q (message %q)", ie.Field, field, ie.Message)
	}
	if message != "" && ie.Message != message {
		t.Errorf("InputError.Message = %q, want %q", ie.Message, message)
	}
}

type postRow struct {
	ID, TopicID, ForumID                int64
	AuthorID                            int64 // 0 for none
	AuthorName, IP, Vis                 string
	Subject, Source, Text               string
	Created                             time.Time
	EditCount                           int
	EditedBy                            int64
	EditedAt                            *time.Time
	EditReason                          string
	Markdown, Smilies, Autolink, ShowSg bool
}

func readPost(t *testing.T, pool *pgxpool.Pool, id int64) postRow {
	t.Helper()
	var p postRow
	err := pool.QueryRow(context.Background(), `
select id, topic_id, forum_id, coalesce(author_id, 0), author_name, coalesce(host(author_ip), ''), visibility,
       subject, body_source, body_text, created_at, edit_count, coalesce(edited_by, 0), edited_at, edit_reason,
       enable_markdown, enable_smilies, enable_autolink, enable_sig
  from posts where id = $1`, id).
		Scan(&p.ID, &p.TopicID, &p.ForumID, &p.AuthorID, &p.AuthorName, &p.IP, &p.Vis,
			&p.Subject, &p.Source, &p.Text, &p.Created, &p.EditCount, &p.EditedBy, &p.EditedAt, &p.EditReason,
			&p.Markdown, &p.Smilies, &p.Autolink, &p.ShowSg)
	if err != nil {
		t.Fatalf("read post %d: %v", id, err)
	}
	return p
}

type topicRow struct {
	ID, ForumID, AuthorID int64
	Title, AuthorName     string
	Vis                   string
	Locked                bool
	Replies               int
	Last                  int64
	LastAt, Created       time.Time
}

func readTopic(t *testing.T, pool *pgxpool.Pool, id int64) topicRow {
	t.Helper()
	var r topicRow
	err := pool.QueryRow(context.Background(), `
select id, forum_id, coalesce(author_id, 0), title, author_name, visibility, locked, reply_count,
       coalesce(last_post_id, 0), last_post_at, created_at
  from topics where id = $1`, id).
		Scan(&r.ID, &r.ForumID, &r.AuthorID, &r.Title, &r.AuthorName, &r.Vis, &r.Locked, &r.Replies, &r.Last, &r.LastAt, &r.Created)
	if err != nil {
		t.Fatalf("read topic %d: %v", id, err)
	}
	return r
}

type forumRow struct {
	Topics, Posts int
	Last          int64
}

func readForum(t *testing.T, pool *pgxpool.Pool, id int64) forumRow {
	t.Helper()
	var r forumRow
	if err := pool.QueryRow(context.Background(),
		`select topic_count, post_count, coalesce(last_post_id, 0) from forums where id = $1`, id).
		Scan(&r.Topics, &r.Posts, &r.Last); err != nil {
		t.Fatalf("read forum %d: %v", id, err)
	}
	return r
}

func userPostCount(t *testing.T, pool *pgxpool.Pool, id int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select post_count from users where id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func countPosts(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from posts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

type logRow struct {
	Actor              int64
	Action, TargetType string
	Target             int64
	Details            string
	At                 time.Time
}

func readLog(t *testing.T, pool *pgxpool.Pool) []logRow {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
select coalesce(actor_id, 0), action, target_type, target_id, details::text, created_at from mod_log order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []logRow
	for rows.Next() {
		var r logRow
		if err := rows.Scan(&r.Actor, &r.Action, &r.TargetType, &r.Target, &r.Details, &r.At); err != nil {
			t.Fatal(err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// counterSnapshot lists the counter columns of every topic and forum.
func counterSnapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	var sb strings.Builder
	rows, err := pool.Query(context.Background(), `
select 'topic ' || id || ' last=' || coalesce(last_post_id, 0) || ' at=' || last_post_at || ' replies=' || reply_count from topics
union all
select 'forum ' || id || ' topics=' || topic_count || ' posts=' || post_count || ' last=' || coalesce(last_post_id, 0) from forums
order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			t.Fatal(err)
		}
		sb.WriteString(s + "\n")
	}
	return sb.String()
}

// wantCountersExact fails when the full recompute would change any counter.
func wantCountersExact(t *testing.T, pool *pgxpool.Pool, after string) {
	t.Helper()
	before := counterSnapshot(t, pool)
	if err := counters.All(context.Background(), pool); err != nil {
		t.Fatal(err)
	}
	if got := counterSnapshot(t, pool); got != before {
		t.Fatalf("after %s, counters.All changes a counter\n--- stored\n%s--- recomputed\n%s", after, before, got)
	}
}

func netipAddr(s string) netip.Addr { return netip.MustParseAddr(s) }
