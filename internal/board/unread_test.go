package board_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

func utc(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

// findForum walks a forum tree for the forum with the given ID.
func findForum(tree []*board.Forum, id int64) *board.Forum {
	for _, f := range tree {
		if f.ID == id {
			return f
		}
		if c := findForum(f.Children, id); c != nil {
			return c
		}
	}
	return nil
}

// forumNew returns the New flag of forum id in the index of v, and whether the
// index lists the forum.
func forumNew(t *testing.T, pool *pgxpool.Pool, v board.Viewer, id int64) (isNew, listed bool) {
	t.Helper()
	tree, err := board.Index(context.Background(), pool, v)
	if err != nil {
		t.Fatal(err)
	}
	f := findForum(tree, id)
	if f == nil {
		return false, false
	}
	return f.New, true
}

// topicNew returns the New flag of topic id on page 1 of forum forumID.
func topicNew(t *testing.T, pool *pgxpool.Pool, v board.Viewer, forumID, id int64) bool {
	t.Helper()
	fp, err := board.ForumView(context.Background(), pool, v, forumID, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range append(slices.Clone(fp.Pinned), fp.Topics...) {
		if r.ID == id {
			return r.New
		}
	}
	t.Fatalf("topic %d is not on page 1 of forum %d", id, forumID)
	return false
}

// memberFor loads the viewer of user id the way a request does.
func memberFor(t *testing.T, pool *pgxpool.Pool, id int64) board.Viewer {
	t.Helper()
	ctx := context.Background()
	s, err := board.LoadSettings(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	v, err := board.Member(ctx, pool, id, s)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestRecordVisit(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	imported := utc(2026, 9, 30, 19, 15)
	T := utc(2026, 10, 24, 12, 0)

	read := func() (visit time.Time, active *time.Time) {
		t.Helper()
		err := pool.QueryRow(ctx, `select last_visit_at, last_active_at from users where id = 2`).Scan(&visit, &active)
		if err != nil {
			t.Fatal(err)
		}
		return visit.UTC(), active
	}
	step := func(now time.Time, wantVisit time.Time, wantActive time.Time) {
		t.Helper()
		if err := board.RecordVisit(ctx, pool, 2, now); err != nil {
			t.Fatal(err)
		}
		visit, active := read()
		if !visit.Equal(wantVisit) {
			t.Errorf("at %v: last_visit_at = %v, want %v", now, visit, wantVisit)
		}
		if active == nil || !active.Equal(wantActive) {
			t.Errorf("at %v: last_active_at = %v, want %v", now, active, wantActive)
		}
	}

	if v, a := read(); !v.Equal(imported) || a != nil {
		t.Fatalf("start: last_visit_at %v, last_active_at %v", v, a)
	}
	// The first request keeps the imported visit and starts the clock.
	step(T, imported, T)
	// Three minutes later nothing changes.
	step(T.Add(3*time.Minute), imported, T)
	// Ten minutes later only last_active_at moves.
	step(T.Add(10*time.Minute), imported, T.Add(10*time.Minute))
	// Exactly 30 minutes of silence is still the same visit.
	step(T.Add(40*time.Minute), imported, T.Add(40*time.Minute))
	// Two hours later a new visit starts: it begins where the last one ended.
	step(T.Add(3*time.Hour), T.Add(40*time.Minute), T.Add(3*time.Hour))
}

func TestStartVisit(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	imported := utc(2026, 9, 30, 19, 15)
	T := utc(2026, 10, 24, 12, 0)
	var visit time.Time
	var active *time.Time
	read := func() {
		t.Helper()
		if err := pool.QueryRow(ctx, `select last_visit_at, last_active_at from users where id = 2`).Scan(&visit, &active); err != nil {
			t.Fatal(err)
		}
	}
	// With no last_active_at, a sign-in keeps the imported visit.
	if err := board.StartVisit(ctx, pool, 2, T); err != nil {
		t.Fatal(err)
	}
	read()
	if !visit.Equal(imported) || active == nil || !active.Equal(T) {
		t.Errorf("first sign-in: visit %v, active %v", visit, active)
	}
	// A later sign-in starts a visit at once, even 2 minutes after the last request.
	if err := board.StartVisit(ctx, pool, 2, T.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	read()
	if !visit.Equal(T) || active == nil || !active.Equal(T.Add(2*time.Minute)) {
		t.Errorf("second sign-in: visit %v, active %v", visit, active)
	}
}

func TestMemberViewerFloors(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	carol := memberFor(t, pool, 3)
	// Carol has no imported marks and no visit: both fall back to registered_at.
	reg := utc(2020, 1, 15, 9, 0)
	if !carol.LastVisit.Equal(reg) || !carol.MarkFloor.Equal(reg) {
		t.Errorf("carol: LastVisit %v, MarkFloor %v, want %v", carol.LastVisit, carol.MarkFloor, reg)
	}
	bob := memberFor(t, pool, 2)
	if !bob.LastVisit.Equal(utc(2026, 9, 30, 19, 15)) || !bob.MarkFloor.Equal(utc(2026, 9, 30, 19, 0)) {
		t.Errorf("bob: LastVisit %v, MarkFloor %v", bob.LastVisit, bob.MarkFloor)
	}
	guest := guestOn(t, pool)
	if !guest.LastVisit.IsZero() || !guest.MarkFloor.IsZero() {
		t.Errorf("guest: LastVisit %v, MarkFloor %v, want zero", guest.LastVisit, guest.MarkFloor)
	}
}

func TestUnreadRule(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	carol := memberFor(t, pool, 3)
	bob := memberFor(t, pool, 2)
	guest := guestOn(t, pool)

	// Carol registered in January 2020 and topic 1 has a post from February.
	if !topicNew(t, pool, carol, 2, 1) {
		t.Error("carol: topic 1 is not New")
	}
	if n, _ := forumNew(t, pool, carol, 2); !n {
		t.Error("carol: forum 2 is not New")
	}
	if n, _ := forumNew(t, pool, carol, 1); !n {
		t.Error("carol: the category does not take New from its forum")
	}
	// Bob marked everything read on 2026-09-30.
	if topicNew(t, pool, bob, 2, 1) {
		t.Error("bob: topic 1 is New")
	}
	if n, _ := forumNew(t, pool, bob, 2); n {
		t.Error("bob: forum 2 is New")
	}
	// A guest sees no marks.
	if topicNew(t, pool, guest, 2, 1) {
		t.Error("guest: topic 1 is New")
	}
	if n, _ := forumNew(t, pool, guest, 2); n {
		t.Error("guest: forum 2 is New")
	}

	// A mark before the last post leaves the topic unread.
	if err := board.MarkTopic(ctx, pool, 3, 1, utc(2020, 1, 20, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if !topicNew(t, pool, carol, 2, 1) {
		t.Error("carol: topic 1 is read after a mark that is too early")
	}
	// A mark at the last post reads it. A later, lower mark cannot undo that.
	if err := board.MarkTopic(ctx, pool, 3, 1, utc(2020, 2, 1, 11, 0)); err != nil {
		t.Fatal(err)
	}
	if err := board.MarkTopic(ctx, pool, 3, 1, utc(2020, 1, 1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if topicNew(t, pool, carol, 2, 1) {
		t.Error("carol: topic 1 is New after MarkTopic")
	}
	var at time.Time
	if err := pool.QueryRow(ctx, `select marked_at from topic_marks where user_id = 3 and topic_id = 1`).Scan(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Equal(utc(2020, 2, 1, 11, 0)) {
		t.Errorf("topic_marks.marked_at = %v, want the later time", at)
	}
}

func TestMarkForumAndAll(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	carol := memberFor(t, pool, 3)
	now := utc(2026, 10, 24, 12, 0)

	if err := board.MarkForum(ctx, pool, carol, 2, now); err != nil {
		t.Fatal(err)
	}
	if n, _ := forumNew(t, pool, carol, 2); n {
		t.Error("forum 2 is New after MarkForum")
	}
	if topicNew(t, pool, carol, 2, 1) {
		t.Error("topic 1 is New after MarkForum")
	}
	// Carol cannot read forum 3, so it gets no mark.
	var ids []int64
	rows, err := pool.Query(ctx, `select forum_id from forum_marks where user_id = 3 order by forum_id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	rows.Close()
	if !slices.Equal(ids, []int64{2}) {
		t.Errorf("forum_marks of carol = %v, want [2]", ids)
	}
	// A forum that carol cannot see is the same as a missing one.
	if err := board.MarkForum(ctx, pool, carol, 3, now); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("MarkForum of a hidden forum = %v, want ErrNotFound", err)
	}
	if err := board.MarkForum(ctx, pool, carol, 99, now); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("MarkForum of a missing forum = %v, want ErrNotFound", err)
	}

	// A mark on a category reaches the forums below it, and the ones that the
	// viewer may read only.
	alice := memberFor(t, pool, 1)
	if err := board.MarkForum(ctx, pool, alice, 1, now); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from forum_marks where user_id = 1 and forum_id in (2, 3)`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Errorf("alice has %d marks on forums 2 and 3, want 2", n)
	}

	// MarkAll moves last_mark_at, and nothing is New afterward.
	bob := memberFor(t, pool, 2)
	_ = bob
	carolAll := memberFor(t, pool, 3)
	if _, err := pool.Exec(ctx, `delete from forum_marks where user_id = 3`); err != nil {
		t.Fatal(err)
	}
	if err := board.MarkAll(ctx, pool, 3, now); err != nil {
		t.Fatal(err)
	}
	if n, _ := forumNew(t, pool, carolAll, 2); n {
		t.Error("forum 2 is New after MarkAll")
	}
	if topicNew(t, pool, carolAll, 2, 1) {
		t.Error("topic 1 is New after MarkAll")
	}
	var mark time.Time
	if err := pool.QueryRow(ctx, `select last_mark_at from users where id = 3`).Scan(&mark); err != nil {
		t.Fatal(err)
	}
	if !mark.Equal(now) {
		t.Errorf("last_mark_at = %v, want %v", mark, now)
	}
}

// A topic in a forum that the viewer may not read never makes a forum New.
func TestForumNewIgnoresUnreadableDescendant(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Topic 2 lives in forum 3, a child of forum 2. Give it a fresh post.
	if _, err := pool.Exec(ctx, `update topics set last_post_at = '2026-10-20T00:00:00Z' where id = 2`); err != nil {
		t.Fatal(err)
	}
	// Carol read forum 2 on 2026-10-01 and cannot read forum 3.
	carol := memberFor(t, pool, 3)
	if err := board.MarkForum(ctx, pool, carol, 2, utc(2026, 10, 1, 0, 0)); err != nil {
		t.Fatal(err)
	}
	if n, _ := forumNew(t, pool, carol, 2); n {
		t.Error("carol: forum 2 is New because of a topic she cannot read")
	}
	// Alice reads forum 3, so the topic makes forum 2 New for her.
	alice := memberFor(t, pool, 1)
	if n, _ := forumNew(t, pool, alice, 3); !n {
		t.Error("alice: forum 3 is not New")
	}
	if n, _ := forumNew(t, pool, alice, 2); !n {
		t.Error("alice: forum 2 does not take New from forum 3")
	}
}

// A moved-topic marker is never New.
func TestMarkerIsNeverNew(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(ctx, `update topics set last_post_at = '2026-10-20T00:00:00Z' where id = 3`); err != nil {
		t.Fatal(err)
	}
	alice := memberFor(t, pool, 1)
	fp, err := board.ForumView(ctx, pool, alice, 3, 1)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range fp.Topics {
		if r.ID == 3 && r.New {
			t.Error("the marker is New")
		}
	}
}

func TestNewSince(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	since := utc(2019, 6, 1, 0, 0)
	if _, err := pool.Exec(ctx, `update users set last_visit_at = $1 where id in (1, 2)`, since); err != nil {
		t.Fatal(err)
	}

	// Carol cannot read forum 3 (topic 2). Topic 3 is a marker. Her visit
	// time is the same date.
	if _, err := pool.Exec(ctx, `update users set last_visit_at = $1 where id = 3`, since); err != nil {
		t.Fatal(err)
	}
	carol := memberFor(t, pool, 3)
	np, err := board.NewSince(ctx, pool, carol, 1)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for _, r := range np.Topics {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []int64{1}) {
		t.Errorf("carol: NewSince = %v, want [1]", ids)
	}
	if np.Page != 1 || np.Pages != 1 || np.Total != 1 {
		t.Errorf("carol: page %d of %d, total %d", np.Page, np.Pages, np.Total)
	}
	if np.Topics[0].Forum == nil || np.Topics[0].Forum.ID != 2 || np.Topics[0].Forum.Name != "Main Discussion" {
		t.Errorf("carol: forum of topic 1 = %+v", np.Topics[0].Forum)
	}
	if !np.Topics[0].New {
		t.Error("carol: topic 1 is not New")
	}

	// Bob may read forum 3 as a member of Beta testers, so he sees both. He
	// marked everything read on 2026-09-30, so neither is New.
	bob := memberFor(t, pool, 2)
	np, err = board.NewSince(ctx, pool, bob, 1)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, r := range np.Topics {
		ids = append(ids, r.ID)
		if r.New {
			t.Errorf("bob: topic %d is New", r.ID)
		}
	}
	if !slices.Equal(ids, []int64{1, 2}) {
		t.Errorf("bob: NewSince = %v, want [1 2]", ids)
	}

	// Alice reads both forums. The list is newest first.
	alice := memberFor(t, pool, 1)
	np, err = board.NewSince(ctx, pool, alice, 1)
	if err != nil {
		t.Fatal(err)
	}
	ids = nil
	for _, r := range np.Topics {
		ids = append(ids, r.ID)
	}
	if !slices.Equal(ids, []int64{1, 2}) {
		t.Errorf("alice: NewSince = %v, want [1 2]", ids)
	}
	for _, r := range np.Topics {
		if !r.New {
			t.Errorf("alice: topic %d is not New", r.ID)
		}
	}
	if n, err := board.CountNew(ctx, pool, alice); err != nil || n != 2 {
		t.Errorf("CountNew(alice) = %d, %v; want 2", n, err)
	}

	// A guest has no list.
	g, err := board.NewSince(ctx, pool, guestOn(t, pool), 1)
	if err != nil || len(g.Topics) != 0 || g.Pages != 1 {
		t.Errorf("guest: %+v, %v", g, err)
	}
	// A page past the end is not found.
	if _, err := board.NewSince(ctx, pool, alice, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 = %v, want ErrNotFound", err)
	}
}

// A member away for years gets a long list, in pages of 50.
func TestNewSincePages(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(ctx, `
insert into topics (forum_id, title, author_id, author_name, created_at, last_post_at, kind, visibility)
select 2, 'Bulk ' || n, 1, 'alice', timestamptz '2024-01-01' + n * interval '1 hour',
       timestamptz '2024-01-01' + n * interval '1 hour', 'normal', 'approved'
  from generate_series(1, 120) n`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update users set last_visit_at = null where id = 1`); err != nil {
		t.Fatal(err)
	}
	alice := memberFor(t, pool, 1)
	var total int
	for page, want := range map[int]int{1: 50, 2: 50, 3: 22} {
		np, err := board.NewSince(ctx, pool, alice, page)
		if err != nil {
			t.Fatalf("page %d: %v", page, err)
		}
		if len(np.Topics) != want || np.Pages != 3 || np.Total != 122 {
			t.Errorf("page %d: %d rows, %d pages, total %d; want %d rows, 3 pages, 122", page, len(np.Topics), np.Pages, np.Total, want)
		}
		total += len(np.Topics)
	}
	if total != 122 {
		t.Errorf("pages hold %d topics, want 122", total)
	}
	if _, err := board.NewSince(ctx, pool, alice, 4); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 4 = %v, want ErrNotFound", err)
	}
	// The newest topic comes first.
	np, _ := board.NewSince(ctx, pool, alice, 1)
	if np.Topics[0].Title != "Bulk 120" {
		t.Errorf("first row = %q, want Bulk 120", np.Topics[0].Title)
	}
}

func TestFirstUnread(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	carol := memberFor(t, pool, 3)

	// Carol registered on 2020-01-15. Post 3 (her own, waiting for approval) is
	// the first one after that. She sees it, so it counts.
	id, err := board.FirstUnread(ctx, pool, carol, 1)
	if err != nil || id != 3 {
		t.Fatalf("FirstUnread = %d, %v; want 3", id, err)
	}
	// A mark after post 3 moves the answer to post 4.
	if err := board.MarkTopic(ctx, pool, 3, 1, utc(2020, 1, 16, 10, 0)); err != nil {
		t.Fatal(err)
	}
	if id, err = board.FirstUnread(ctx, pool, carol, 1); err != nil || id != 4 {
		t.Errorf("after a mark on post 3: %d, %v; want 4", id, err)
	}
	// Bob cannot see post 3, so for him the first unread post after the same
	// time is post 4 as well.
	// After MarkAll nothing is newer, so the answer is the last post.
	if err := board.MarkAll(ctx, pool, 3, utc(2026, 10, 24, 12, 0)); err != nil {
		t.Fatal(err)
	}
	if id, err = board.FirstUnread(ctx, pool, carol, 1); err != nil || id != 4 {
		t.Errorf("after MarkAll: %d, %v; want 4", id, err)
	}

	// Alice does not see post 3 the way carol does: it is unapproved and she
	// has no mod.approve. Her first unread post differs.
	if _, err := pool.Exec(ctx, `update users set last_mark_at = '2019-05-02T09:10:00Z' where id = 1`); err != nil {
		t.Fatal(err)
	}
	alice := memberFor(t, pool, 1)
	if id, err = board.FirstUnread(ctx, pool, alice, 1); err != nil || id != 2 {
		t.Errorf("alice: %d, %v; want 2", id, err)
	}

	// A guest gets the first visible post.
	guest := guestOn(t, pool)
	if id, err = board.FirstUnread(ctx, pool, guest, 1); err != nil || id != 1 {
		t.Errorf("guest: %d, %v; want 1", id, err)
	}
	// A hidden topic, a missing topic and a marker are all not found.
	for _, tid := range []int64{2, 3, 99} {
		if _, err := board.FirstUnread(ctx, pool, guest, tid); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("guest: topic %d = %v, want ErrNotFound", tid, err)
		}
	}
}

func TestCountView(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if err := board.CountView(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select view_count from topics where id = 1`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 43 {
		t.Errorf("view_count = %d, want 43", n)
	}
}

// counting wraps a pool and counts the statements that go through it.
type counting struct {
	pool *pgxpool.Pool
	n    int
}

func (c *counting) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	c.n++
	return c.pool.Exec(ctx, sql, args...)
}

func (c *counting) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	c.n++
	return c.pool.Query(ctx, sql, args...)
}

func (c *counting) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	c.n++
	return c.pool.QueryRow(ctx, sql, args...)
}

// The badges cost a fixed number of statements: a page of 50 topics needs the
// same count as a page of 3.
func TestBadgesCostAFixedNumberOfQueries(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	carol := memberFor(t, pool, 3)
	count := func() (index, forum, newsince int) {
		t.Helper()
		var c counting
		c.pool = pool
		if _, err := board.Index(ctx, &c, carol); err != nil {
			t.Fatal(err)
		}
		index = c.n
		c.n = 0
		if _, err := board.ForumView(ctx, &c, carol, 2, 1); err != nil {
			t.Fatal(err)
		}
		forum = c.n
		c.n = 0
		if _, err := board.NewSince(ctx, &c, carol, 1); err != nil {
			t.Fatal(err)
		}
		return index, forum, c.n
	}
	i0, f0, n0 := count()
	if _, err := pool.Exec(ctx, `
insert into topics (forum_id, title, author_id, author_name, created_at, last_post_at, kind, visibility)
select 2, 'Bulk ' || n, 1, 'alice', timestamptz '2024-01-01' + n * interval '1 hour',
       timestamptz '2024-01-01' + n * interval '1 hour', 'normal', 'approved'
  from generate_series(1, 120) n`); err != nil {
		t.Fatal(err)
	}
	i1, f1, n1 := count()
	if i0 != i1 || f0 != f1 || n0 != n1 {
		t.Errorf("statements before and after 120 topics: index %d/%d, forum %d/%d, new %d/%d", i0, i1, f0, f1, n0, n1)
	}
}
