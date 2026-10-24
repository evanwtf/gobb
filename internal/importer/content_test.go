package importer

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/markup"
)

// walk calls fn for every node of a tree.
func walk(n *markup.Node, fn func(*markup.Node)) {
	if n == nil {
		return
	}
	fn(n)
	for _, c := range n.Children {
		walk(c, fn)
	}
}

// storedTree reads the tree that the import stored for post id.
func storedTree(t *testing.T, pool *pgxpool.Pool, id int64) *markup.Node {
	t.Helper()
	var raw string
	if err := pool.QueryRow(context.Background(), `select body_tree::text from posts where id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("post %d: %v", id, err)
	}
	tree, err := markup.Decode([]byte(raw))
	if err != nil {
		t.Fatalf("post %d: %v", id, err)
	}
	if err := markup.Validate(tree); err != nil {
		t.Fatalf("post %d: stored tree is invalid: %v", id, err)
	}
	return tree
}

// elements returns the names of the elements in a tree.
func elements(tree *markup.Node) map[string]bool {
	names := map[string]bool{}
	walk(tree, func(n *markup.Node) {
		if n.Kind == markup.Element {
			names[n.Name] = true
		}
	})
	return names
}

func TestImportSmallBoard(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	counts := mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)

	// Every data file reports its row count.
	want := map[string]int{
		"topics": 3, "posts": 6, "polls": 1, "poll_votes": 2, "attachments": 2,
		"pms": 2, "topic_watches": 1, "bookmarks": 1, "topic_marks": 1, "forum_marks": 1,
	}
	for f, n := range want {
		if counts[f] != n {
			t.Errorf("counts[%s] = %d, want %d", f, counts[f], n)
		}
	}
	// Every table holds the rows of the bundle. A poll has 3 options and a
	// message has one row for each recipient: 2 + 1 = 3 in all.
	for table, n := range map[string]int{
		"topics": 3, "posts": 6, "polls": 1, "poll_options": 3, "poll_votes": 2,
		"attachments": 2, "pms": 2, "pm_recipients": 3, "topic_watches": 1,
		"bookmarks": 1, "topic_marks": 1, "forum_marks": 1,
	} {
		if got := count(t, pool, table); got != n {
			t.Errorf("%s has %d rows, want %d", table, got, n)
		}
	}
	sameIDs(t, "topics", ids(t, pool, "select id from topics order by id"), 1, 2, 3)
	sameIDs(t, "posts", ids(t, pool, "select id from posts order by id"), 1, 2, 3, 4, 5, 6)

	// The guest post has no author ID and keeps its name.
	var authorID *int64
	var authorName, ip string
	if err := pool.QueryRow(ctx,
		`select author_id, author_name, host(author_ip) from posts where id = 4`).Scan(&authorID, &authorName, &ip); err != nil {
		t.Fatal(err)
	}
	if authorID != nil || authorName != "Visitor" || ip != "2001:db8::7" {
		t.Errorf("post 4 = %v %q %q", authorID, authorName, ip)
	}

	// A post takes its forum from its topic.
	var forumOf string
	if err := pool.QueryRow(ctx,
		`select string_agg(id || ':' || forum_id, ',' order by id) from posts`).Scan(&forumOf); err != nil {
		t.Fatal(err)
	}
	if forumOf != "1:2,2:2,3:2,4:2,5:3,6:3" {
		t.Errorf("post forums = %s", forumOf)
	}

	// Post 2 shows attachment 1 inline.
	var img bool
	walk(storedTree(t, pool, 2), func(n *markup.Node) {
		if n.Kind == markup.Element && n.Name == "img" && n.Attrs["src"] == "/a/1" {
			img = true
		}
	})
	if !img {
		t.Error("post 2 has no img with src /a/1")
	}

	// Post 5 holds a smiley.
	if !elements(storedTree(t, pool, 5))["smiley"] {
		t.Error("post 5 has no smiley element")
	}

	// body_text holds the plain words, and the search finds the post.
	var bodyText, source string
	var edits int
	var editedBy *int64
	if err := pool.QueryRow(ctx,
		`select body_text, body_source, edit_count, edited_by from posts where id = 1`).Scan(&bodyText, &source, &edits, &editedBy); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bodyText, "Welcome to the example board") || strings.Contains(bodyText, "**") {
		t.Errorf("body_text = %q", bodyText)
	}
	if source != "Welcome to the **example** board." || edits != 1 || editedBy == nil || *editedBy != 1 {
		t.Errorf("post 1 = %q %d %v", source, edits, editedBy)
	}
	sameIDs(t, "search", ids(t, pool,
		`select id from posts where search @@ plainto_tsquery('english', 'moderator') order by id`), 3)

	// The attachment keeps its path, size and hash, and its post.
	var postID *int64
	var stored, mime, sum string
	var size int64
	var dl int
	if err := pool.QueryRow(ctx,
		`select post_id, stored_path, mime, size, sha256, download_count from attachments where id = 1`).
		Scan(&postID, &stored, &mime, &size, &sum, &dl); err != nil {
		t.Fatal(err)
	}
	if postID == nil || *postID != 2 || stored != "attachments/1.png" || mime != "image/png" ||
		size != int64(len(bundletest.Small().Files["attachments/1.png"])) ||
		sum != sha(bundletest.Small().Files["attachments/1.png"]) || dl != 7 {
		t.Errorf("attachment 1 = %v %q %q %d %q %d", postID, stored, mime, size, sum, dl)
	}
	if err := pool.QueryRow(ctx, `select post_id from attachments where id = 2`).Scan(&postID); err != nil {
		t.Fatal(err)
	}
	if postID != nil {
		t.Errorf("attachment 2 has post %d, want none", *postID)
	}

	// Poll options keep their votes. Poll votes keep their IP.
	var opts string
	if err := pool.QueryRow(ctx,
		`select string_agg(option_id || ':' || text || ':' || vote_count, ',' order by option_id)
		 from poll_options where topic_id = 2`).Scan(&opts); err != nil {
		t.Fatal(err)
	}
	if opts != "1:Green:3,2:Blue:2,3:Red:0" {
		t.Errorf("options = %s", opts)
	}
	var maxOpts int
	var canChange bool
	if err := pool.QueryRow(ctx, `select max_options, can_change_vote from polls where topic_id = 2`).Scan(&maxOpts, &canChange); err != nil {
		t.Fatal(err)
	}
	if maxOpts != 2 || !canChange {
		t.Errorf("poll = %d %v", maxOpts, canChange)
	}
	if err := pool.QueryRow(ctx,
		`select host(voter_ip) from poll_votes where user_id = 2`).Scan(&ip); err != nil {
		t.Fatal(err)
	}
	if ip != "2001:db8::6" {
		t.Errorf("voter_ip = %s", ip)
	}

	// Messages: the body has a tree and text, and the recipients keep their flags.
	var pmText string
	var deleted bool
	if err := pool.QueryRow(ctx,
		`select body_text, author_deleted from pms where id = 2`).Scan(&pmText, &deleted); err != nil {
		t.Fatal(err)
	}
	if pmText != "Thanks, alice." || !deleted {
		t.Errorf("pm 2 = %q %v", pmText, deleted)
	}
	var rcpt string
	if err := pool.QueryRow(ctx,
		`select string_agg(pm_id || ':' || user_id || ':' || kind || ':' || is_read || ':' || is_deleted, ',' order by pm_id, user_id)
		 from pm_recipients`).Scan(&rcpt); err != nil {
		t.Fatal(err)
	}
	if rcpt != "1:2:to:true:false,1:3:bcc:false:false,2:1:to:true:true" {
		t.Errorf("recipients = %s", rcpt)
	}

	// The small tables.
	var w bool
	if err := pool.QueryRow(ctx, `select notify_pending from topic_watches where user_id = 2 and topic_id = 1`).Scan(&w); err != nil {
		t.Fatal(err)
	}
	var marked time.Time
	if err := pool.QueryRow(ctx, `select marked_at from topic_marks where user_id = 2 and topic_id = 1`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if !marked.Equal(time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("topic mark = %s", marked)
	}
	if err := pool.QueryRow(ctx, `select marked_at from forum_marks where user_id = 2 and forum_id = 2`).Scan(&marked); err != nil {
		t.Fatal(err)
	}
	if !marked.Equal(time.Date(2026, 9, 30, 8, 0, 0, 0, time.UTC)) {
		t.Errorf("forum mark = %s", marked)
	}

	// The next insert without an ID follows the largest imported ID.
	var next int64
	if err := pool.QueryRow(ctx,
		`insert into topics (forum_id, created_at, last_post_at, kind, visibility)
		 values (2, now(), now(), 'normal', 'approved') returning id`).Scan(&next); err != nil {
		t.Fatal(err)
	}
	if next != 4 {
		t.Errorf("next topic ID = %d, want 4", next)
	}
}

// TestImportCounters checks the counters of Small by hand.
//
// Topic 1 (forum 2) has posts 1, 2, 3, 4. Post 3 is unapproved, so 3 are
// approved: reply_count = 3 - 1 = 2. The newest approved post is 4 (created
// 2020-02-01), and the unapproved post 3 (2020-01-16) is older anyway, so
// last_post_id = 4.
//
// Topic 2 (forum 3) has approved posts 5 and 6: reply_count = 2 - 1 = 1. The
// newest is 6 (2019-07-01 19:00).
//
// Topic 3 is a moved-topic marker in forum 3: no last_post_id, last_post_at =
// created_at (2019-05-02 09:00), reply_count 0.
//
// Forum 1 is a category with no topic: topic_count 0, post_count 0, no last
// post. Counts do not roll up.
// Forum 2 holds topic 1: topic_count 1, post_count 3 (posts 1, 2, 4),
// last_post_id 4.
// Forum 3 holds topic 2 and the marker: topic_count 1 (the marker does not
// count), post_count 2 (posts 5, 6), last_post_id 6.
func TestImportCounters(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	mustRun(t, pool, writeBoard(t, nil), t.TempDir(), false)

	type topic struct {
		last    *int64
		lastAt  time.Time
		replies int
	}
	wantTopic := map[int64]struct {
		last    int64 // 0 means NULL
		lastAt  time.Time
		replies int
	}{
		1: {4, time.Date(2020, 2, 1, 11, 0, 0, 0, time.UTC), 2},
		2: {6, time.Date(2019, 7, 1, 19, 0, 0, 0, time.UTC), 1},
		3: {0, time.Date(2019, 5, 2, 9, 0, 0, 0, time.UTC), 0},
	}
	for id, w := range wantTopic {
		var got topic
		if err := pool.QueryRow(ctx,
			`select last_post_id, last_post_at, reply_count from topics where id = $1`, id).
			Scan(&got.last, &got.lastAt, &got.replies); err != nil {
			t.Fatal(err)
		}
		switch {
		case w.last == 0 && got.last != nil:
			t.Errorf("topic %d last_post_id = %d, want NULL", id, *got.last)
		case w.last != 0 && (got.last == nil || *got.last != w.last):
			t.Errorf("topic %d last_post_id = %v, want %d", id, got.last, w.last)
		}
		if !got.lastAt.Equal(w.lastAt) || got.replies != w.replies {
			t.Errorf("topic %d = %s %d, want %s %d", id, got.lastAt, got.replies, w.lastAt, w.replies)
		}
	}

	wantForum := map[int64]struct {
		topics, posts int
		last          int64
	}{
		1: {0, 0, 0},
		2: {1, 3, 4},
		3: {1, 2, 6},
	}
	for id, w := range wantForum {
		var topics, posts int
		var last *int64
		if err := pool.QueryRow(ctx,
			`select topic_count, post_count, last_post_id from forums where id = $1`, id).
			Scan(&topics, &posts, &last); err != nil {
			t.Fatal(err)
		}
		if topics != w.topics || posts != w.posts {
			t.Errorf("forum %d = %d topics, %d posts; want %d, %d", id, topics, posts, w.topics, w.posts)
		}
		if (w.last == 0) != (last == nil) || (last != nil && *last != w.last) {
			t.Errorf("forum %d last_post_id = %v, want %d", id, last, w.last)
		}
	}
}

// TestImportCountersEdgeCases covers a topic with no approved post, an
// unapproved post that is newer than the approved ones, and an unapproved topic.
//
// The test sets the visibility as follows: topic 1 unapproved; post 1 approved,
// post 2 approved, post 3 unapproved, post 4 unapproved and dated 2030 (newer
// than all others); post 5 unapproved, post 6 deleted.
//
// Topic 1: approved posts are 1 and 2, so reply_count = 2 - 1 = 1. The newest
// approved post is 2 (the newer post 4 is not approved), so last_post_id = 2.
// Topic 2: no approved post, so reply_count = max(0 - 1, 0) = 0. The last post
// is the newest of any visibility, post 6 (2019-07-01 19:00).
// Forum 2: its only topic is unapproved, so topic_count 0, post_count 0 and no
// last post, though topic 1 has approved posts.
// Forum 3: topic 2 is approved (topic_count 1; the marker does not count), but
// it has no approved post, so post_count 0 and no last post.
func TestImportCountersEdgeCases(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Topics[0].Visibility = "unapproved"
		b.Posts[3].Visibility = "unapproved"
		b.Posts[3].CreatedAt = time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
		b.Posts[4].Visibility = "unapproved"
		b.Posts[5].Visibility = "deleted"
	})
	mustRun(t, pool, dir, t.TempDir(), false)

	var last int64
	var replies int
	if err := pool.QueryRow(ctx, `select last_post_id, reply_count from topics where id = 1`).Scan(&last, &replies); err != nil {
		t.Fatal(err)
	}
	if last != 2 || replies != 1 {
		t.Errorf("topic 1 = last %d, replies %d; want 2, 1", last, replies)
	}
	var lastAt time.Time
	if err := pool.QueryRow(ctx, `select last_post_id, last_post_at, reply_count from topics where id = 2`).Scan(&last, &lastAt, &replies); err != nil {
		t.Fatal(err)
	}
	if last != 6 || replies != 0 || !lastAt.Equal(time.Date(2019, 7, 1, 19, 0, 0, 0, time.UTC)) {
		t.Errorf("topic 2 = last %d at %s, replies %d; want 6, 2019-07-01 19:00, 0", last, lastAt, replies)
	}
	var topics, posts int
	var flast *int64
	for _, c := range []struct {
		id            int64
		topics, posts int
	}{{2, 0, 0}, {3, 1, 0}} {
		if err := pool.QueryRow(ctx, `select topic_count, post_count, last_post_id from forums where id = $1`, c.id).
			Scan(&topics, &posts, &flast); err != nil {
			t.Fatal(err)
		}
		if topics != c.topics || posts != c.posts || flast != nil {
			t.Errorf("forum %d = %d topics, %d posts, last %v; want %d, %d, NULL", c.id, topics, posts, flast, c.topics, c.posts)
		}
	}
}

func TestImportFlagsRespected(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Posts[0].Body.Source = "**x** here"
		b.Posts[0].Body.Markdown = false
		b.Posts[5].Body.Source = "see http://example.com/page now"
		b.Posts[5].Body.Autolink = false
	})
	mustRun(t, pool, dir, t.TempDir(), false)

	// Post 1: markdown off, so the emphasis marks stay in the text.
	if elements(storedTree(t, pool, 1))["strong"] {
		t.Error("post 1 has a strong element with markdown off")
	}
	var text string
	var md, sm, al bool
	if err := pool.QueryRow(ctx,
		`select body_text, enable_markdown, enable_smilies, enable_autolink from posts where id = 1`).
		Scan(&text, &md, &sm, &al); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "**x** here") || md || !sm || !al {
		t.Errorf("post 1 = %q %v %v %v", text, md, sm, al)
	}

	// Post 6: autolink off, so the address is text and not a url element.
	if elements(storedTree(t, pool, 6))["url"] {
		t.Error("post 6 has a url element with autolink off")
	}
	if err := pool.QueryRow(ctx,
		`select body_text, enable_markdown, enable_smilies, enable_autolink from posts where id = 6`).
		Scan(&text, &md, &sm, &al); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "see http://example.com/page now") || !md || !sm || al {
		t.Errorf("post 6 = %q %v %v %v", text, md, sm, al)
	}

	// The same address with autolink on is a url element. This proves the
	// first check can fail.
	dir = writeBoard(t, func(b *bundletest.Board) {
		b.Posts[5].Body.Source = "see http://example.com/page now"
	})
	pool2 := dbtest.New(t)
	mustRun(t, pool2, dir, t.TempDir(), false)
	if !elements(storedTree(t, pool2, 6))["url"] {
		t.Error("post 6 has no url element with autolink on")
	}
}

func TestImportIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.New(t)
	data := t.TempDir()
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.Posts[5].Body.Source = "BREAK"
	})
	old := parseBody
	t.Cleanup(func() { parseBody = old })
	parseBody = func(src string, opt markup.Options) *markup.Node {
		if src == "BREAK" {
			return &markup.Node{Kind: markup.Element, Name: "no-such-element"}
		}
		return old(src, opt)
	}
	_, err := run(t, pool, dir, data, false)
	if err == nil {
		t.Fatal("import succeeded")
	}
	if !strings.HasPrefix(err.Error(), "posts.jsonl:6 (id 6): body: ") {
		t.Errorf("error = %v", err)
	}

	tables, err := db.DataTables(ctx, pool)
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range tables {
		if n := count(t, pool, `"`+table+`"`); n != 0 {
			t.Errorf("table %s has %d rows, want 0", table, n)
		}
	}
	entries, err := os.ReadDir(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the data directory holds %s", e.Name())
	}
	noStaging(t, data)
}

func TestImportNamesMessageOnTreeError(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) {
		b.PMs[1].Body.Source = "BREAK"
	})
	old := parseBody
	t.Cleanup(func() { parseBody = old })
	parseBody = func(src string, opt markup.Options) *markup.Node {
		if src == "BREAK" {
			return &markup.Node{Kind: markup.Element, Name: "no-such-element"}
		}
		return old(src, opt)
	}
	_, err := run(t, pool, dir, t.TempDir(), false)
	if err == nil || !strings.HasPrefix(err.Error(), "pms.jsonl:2 (id 2): body: ") {
		t.Errorf("error = %v", err)
	}
}

// TestImportBatchBoundaries loads more rows than one batch holds, to cover the
// flush of a full batch and the flush of the rest.
func TestImportBatchBoundaries(t *testing.T) {
	pool := dbtest.New(t)
	b := bundletest.Large(3, 667) // 2,001 posts: two full batches and 1 row
	dir := bundletest.Write(t, b)
	counts := mustRun(t, pool, dir, t.TempDir(), false)
	if counts["posts"] != 2001 || count(t, pool, "posts") != 2001 {
		t.Errorf("posts = %d / %d, want 2001", counts["posts"], count(t, pool, "posts"))
	}
}

func TestImportScale(t *testing.T) {
	if testing.Short() {
		t.Skip("scale test")
	}
	ctx := context.Background()
	pool := dbtest.New(t)
	const topics, perTopic = 2500, 14 // 35,000 posts
	dir := bundletest.Write(t, bundletest.Large(topics, perTopic))

	start := time.Now()
	counts := mustRun(t, pool, dir, t.TempDir(), false)
	elapsed := time.Since(start)
	t.Logf("imported %d topics and %d posts in %s", counts["topics"], counts["posts"], elapsed)
	if elapsed > 120*time.Second {
		t.Errorf("the import took %s, want under 120s", elapsed)
	}

	if counts["posts"] != topics*perTopic || count(t, pool, "posts") != topics*perTopic {
		t.Errorf("posts = %d / %d", counts["posts"], count(t, pool, "posts"))
	}
	// Every topic has 14 approved posts, so 13 replies. Odd topics are in forum
	// 2 (1,250 of them) and even topics in forum 3 (1,250).
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from topics where reply_count = 13`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != topics {
		t.Errorf("%d topics have 13 replies, want %d", n, topics)
	}
	for _, f := range []int64{2, 3} {
		var tc, pc int
		if err := pool.QueryRow(ctx, `select topic_count, post_count from forums where id = $1`, f).Scan(&tc, &pc); err != nil {
			t.Fatal(err)
		}
		if tc != 1250 || pc != 1250*14 {
			t.Errorf("forum %d = %d topics, %d posts; want 1250, %d", f, tc, pc, 1250*14)
		}
	}
}
