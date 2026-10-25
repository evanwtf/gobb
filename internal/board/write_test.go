package board_test

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

const msgImages = "You may not post images in this forum."

func hasElement(n *markup.Node, name string) bool {
	stack := []*markup.Node{n}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if c.Kind == markup.Element && c.Name == name {
			return true
		}
		stack = append(stack, c.Children...)
	}
	return false
}

func TestParseBodyRules(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "post.images")
	withImages := viewerFor(t, pool, alice)
	plain := viewerFor(t, pool, carol)

	t.Run("limits", func(t *testing.T) {
		_, err := board.ParseBody(ctx, pool, plain, 2, "", nil)
		wantInput(t, err, "body", "")
		_, err = board.ParseBody(ctx, pool, plain, 2, strings.Repeat("a", 60001), nil)
		wantInput(t, err, "body", "")
		if _, err := board.ParseBody(ctx, pool, plain, 2, strings.Repeat("a", 60000), nil); err != nil {
			t.Errorf("60,000 bytes: %v", err)
		}
		if _, err := board.ParseBody(ctx, pool, plain, 2, "x", nil); err != nil {
			t.Errorf("1 byte: %v", err)
		}
		// A body of white space is empty to a reader.
		_, err = board.ParseBody(ctx, pool, plain, 2, " \n\t \r\n", nil)
		wantInput(t, err, "body", "")
		// The limit counts bytes, not characters: 30,001 two-byte characters are 60,002.
		_, err = board.ParseBody(ctx, pool, plain, 2, strings.Repeat("é", 30001), nil)
		wantInput(t, err, "body", "")
	})

	t.Run("text the database cannot hold", func(t *testing.T) {
		_, err := board.ParseBody(ctx, pool, plain, 2, "a\x00b", nil)
		wantInput(t, err, "body", "")
		_, err = board.ParseBody(ctx, pool, plain, 2, "a\xffb", nil)
		wantInput(t, err, "body", "")
	})

	t.Run("images", func(t *testing.T) {
		for _, src := range []string{"![x](https://example.com/a.png)", "![x](/a/1)", "text\n\n![x](https://example.com/a.png)"} {
			_, err := board.ParseBody(ctx, pool, plain, 2, src, nil)
			wantInput(t, err, "body", msgImages)
			b, err := board.ParseBody(ctx, pool, withImages, 2, src, nil)
			if err != nil {
				t.Fatalf("%q with post.images: %v", src, err)
			}
			if !hasElement(b.Tree, "img") {
				t.Errorf("%q: the tree has no img", src)
			}
		}
		// The right is for one forum.
		_, err := board.ParseBody(ctx, pool, withImages, 3, "![x](https://example.com/a.png)",
			&markup.Options{Markdown: true, Autolink: true})
		wantInput(t, err, "body", msgImages)
		// A stored flag set does not skip the check.
		_, err = board.ParseBody(ctx, pool, plain, 2, "![x](https://example.com/a.png)",
			&markup.Options{Markdown: true, Smilies: true, Autolink: true})
		wantInput(t, err, "body", msgImages)
	})

	t.Run("flags from the rights of the writer", func(t *testing.T) {
		// carol holds post.markdown and post.smilies through the standard role.
		b, err := board.ParseBody(ctx, pool, plain, 2, "**x** :)", nil)
		if err != nil {
			t.Fatal(err)
		}
		if !b.Flags.Markdown || !b.Flags.Smilies || !b.Flags.Autolink {
			t.Errorf("flags = %+v, want all three on", b.Flags)
		}
		if !hasElement(b.Tree, "b") || !hasElement(b.Tree, "smiley") {
			t.Errorf("tree = %s, want bold and a smiley", b.JSON)
		}
		denyOpts(t, pool, carol, 2, "post.markdown", "post.smilies")
		off := viewerFor(t, pool, carol)
		b, err = board.ParseBody(ctx, pool, off, 2, "**x** :)", nil)
		if err != nil {
			t.Fatal(err)
		}
		if b.Flags.Markdown || b.Flags.Smilies || !b.Flags.Autolink {
			t.Errorf("flags = %+v, want markdown and smilies off, autolink on", b.Flags)
		}
		if !strings.Contains(b.Text, "**x**") || hasElement(b.Tree, "b") || hasElement(b.Tree, "smiley") {
			t.Errorf("text = %q, tree = %s, want literal text", b.Text, b.JSON)
		}
	})

	t.Run("an edit keeps the stored flags", func(t *testing.T) {
		// The viewer holds markdown and smilies, but the post was stored with them off.
		b, err := board.ParseBody(ctx, pool, plain, 2, "**x** :)", &markup.Options{Autolink: true})
		if err != nil {
			t.Fatal(err)
		}
		if b.Flags.Markdown || b.Flags.Smilies || hasElement(b.Tree, "b") || hasElement(b.Tree, "smiley") {
			t.Errorf("flags = %+v, tree = %s: the stored flags did not win", b.Flags, b.JSON)
		}
		// And the other way: a stored flag stays on for a viewer without the right.
		denyOpts(t, pool, carol, 2, "post.markdown")
		off := viewerFor(t, pool, carol)
		b, err = board.ParseBody(ctx, pool, off, 2, "**x**", &markup.Options{Markdown: true, Autolink: true})
		if err != nil {
			t.Fatal(err)
		}
		if !b.Flags.Markdown || !hasElement(b.Tree, "b") {
			t.Errorf("flags = %+v, tree = %s: the stored markdown flag did not win", b.Flags, b.JSON)
		}
	})

	t.Run("script text stays text", func(t *testing.T) {
		src := "<script>alert(1)</script> <img src=x onerror=alert(2)> [url=javascript:alert(3)]x[/url] <b onclick=y>z</b>\n\n[x](javascript:alert(4))"
		for _, flags := range []*markup.Options{nil, {Autolink: true}, {Markdown: true, Smilies: true, Autolink: true}} {
			b, err := board.ParseBody(ctx, pool, plain, 2, src, flags)
			if err != nil {
				t.Fatal(err)
			}
			if err := markup.Validate(b.Tree); err != nil {
				t.Errorf("Validate: %v", err)
			}
			out := string(markup.Render(b.Tree, markup.RenderContext{}).HTML)
			htmlcheck.Check(t, out)
			if strings.Contains(out, "<script") || strings.Contains(out, "<img") {
				t.Errorf("rendered HTML holds a tag from the source: %s", out)
			}
		}
	})

	t.Run("a signature", func(t *testing.T) {
		flags := &markup.Options{Markdown: true, Smilies: true, Autolink: true}
		if _, err := board.ParseBody(ctx, pool, plain, 0, "", flags); err != nil {
			t.Errorf("empty signature: %v", err)
		}
		if _, err := board.ParseBody(ctx, pool, plain, 0, strings.Repeat("a", 2000), flags); err != nil {
			t.Errorf("2,000 bytes: %v", err)
		}
		_, err := board.ParseBody(ctx, pool, plain, 0, strings.Repeat("a", 2001), flags)
		wantInput(t, err, "body", "")
		if _, err := board.ParseBody(ctx, pool, plain, 0, "x", nil); err == nil {
			t.Error("a signature with no flags was accepted")
		}
		// A signature may hold an image when the member holds post.images in some forum.
		_, err = board.ParseBody(ctx, pool, plain, 0, "![x](https://example.com/a.png)", flags)
		wantInput(t, err, "body", "")
		b, err := board.ParseBody(ctx, pool, withImages, 0, "![x](https://example.com/a.png)", flags)
		if err != nil || !hasElement(b.Tree, "img") {
			t.Errorf("signature with post.images in forum 2: %v", err)
		}
		// The forum rights do not decide the flags of a signature: the member's flags do.
		b, err = board.ParseBody(ctx, pool, plain, 0, "**x**", &markup.Options{Autolink: true})
		if err != nil {
			t.Fatal(err)
		}
		if hasElement(b.Tree, "b") {
			t.Errorf("tree = %s, want no bold: the stored flags are off", b.JSON)
		}
	})
}

// TestParseBodyMatchesImport pins the write path to the import path: the same
// source with the same flags gives the same tree and the same plain text.
func TestParseBodyMatchesImport(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "post.images")
	grantOpts(t, pool, alice, 3, "post.images")
	v := viewerFor(t, pool, alice)
	rows, err := pool.Query(ctx, `select id, forum_id, body_source, enable_markdown, enable_smilies, enable_autolink from posts order by id`)
	if err != nil {
		t.Fatal(err)
	}
	type row struct {
		id, forum    int64
		src          string
		md, sm, auto bool
	}
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.forum, &r.src, &r.md, &r.sm, &r.auto); err != nil {
			t.Fatal(err)
		}
		all = append(all, r)
	}
	rows.Close()
	if len(all) != 6 {
		t.Fatalf("fixture has %d posts, want 6", len(all))
	}
	for _, r := range all {
		b, err := board.ParseBody(ctx, pool, v, r.forum, r.src, &markup.Options{Markdown: r.md, Smilies: r.sm, Autolink: r.auto})
		if err != nil {
			t.Fatalf("post %d: %v", r.id, err)
		}
		var same bool
		if err := pool.QueryRow(ctx, `select body_tree = $2::jsonb and body_text = $3 from posts where id = $1`,
			r.id, string(b.JSON), b.Text).Scan(&same); err != nil {
			t.Fatal(err)
		}
		if !same {
			t.Errorf("post %d: ParseBody gives a different tree or text than the import stored\njson: %s\ntext: %q", r.id, b.JSON, b.Text)
		}
	}
}

func TestNewTopicAndReply(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "post.no_approval", "post.counts")
	v := viewerFor(t, pool, alice)

	forum0 := readForum(t, pool, 2)
	posts0 := userPostCount(t, pool, alice)

	pid, err := board.NewTopic(ctx, pool, v, 2, board.Draft{Title: "  My new topic  ", Source: "Hello **world**", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	p := readPost(t, pool, pid)
	tp := readTopic(t, pool, p.TopicID)
	if tp.Title != "My new topic" || tp.Vis != "approved" || tp.ForumID != 2 || tp.AuthorID != alice || tp.AuthorName != "alice" ||
		tp.Replies != 0 || tp.Last != pid || !tp.LastAt.Equal(t0) || !tp.Created.Equal(t0) || tp.Locked {
		t.Errorf("topic = %+v", tp)
	}
	if p.Vis != "approved" || p.AuthorID != alice || p.AuthorName != "alice" || p.IP != "10.1.2.3" || p.ForumID != 2 ||
		p.Source != "Hello **world**" || !p.Created.Equal(t0) || p.EditCount != 0 || p.Subject != "My new topic" ||
		!p.Markdown || !p.Smilies || !p.Autolink || p.ShowSg {
		t.Errorf("post = %+v", p)
	}
	if p.Text != "Hello world" {
		t.Errorf("body_text = %q, want %q", p.Text, "Hello world")
	}
	var treeOK bool
	if err := pool.QueryRow(ctx, `select jsonb_path_exists(body_tree, '$.** ? (@.n == "b")') from posts where id = $1`, pid).Scan(&treeOK); err != nil || !treeOK {
		t.Errorf("body_tree has no bold element: %v", err)
	}
	f := readForum(t, pool, 2)
	if f.Topics != forum0.Topics+1 || f.Posts != forum0.Posts+1 || f.Last != pid {
		t.Errorf("forum = %+v, before %+v", f, forum0)
	}
	if got := userPostCount(t, pool, alice); got != posts0+1 {
		t.Errorf("alice post_count = %d, want %d", got, posts0+1)
	}
	wantCountersExact(t, pool, "NewTopic")

	t1 := t0.Add(5 * time.Minute)
	rid, err := board.Reply(ctx, pool, v, p.TopicID, board.Draft{Source: "A reply :)", IP: ip1}, t1)
	if err != nil {
		t.Fatal(err)
	}
	r := readPost(t, pool, rid)
	tp = readTopic(t, pool, p.TopicID)
	if tp.Replies != 1 || tp.Last != rid || !tp.LastAt.Equal(t1) {
		t.Errorf("topic after reply = %+v", tp)
	}
	if r.Vis != "approved" || r.TopicID != p.TopicID || r.ForumID != 2 || r.Subject != "Re: My new topic" ||
		r.AuthorID != alice || r.IP != "10.1.2.3" || !r.Created.Equal(t1) {
		t.Errorf("reply = %+v", r)
	}
	f2 := readForum(t, pool, 2)
	if f2.Topics != f.Topics || f2.Posts != f.Posts+1 || f2.Last != rid {
		t.Errorf("forum after reply = %+v, before %+v", f2, f)
	}
	if got := userPostCount(t, pool, alice); got != posts0+2 {
		t.Errorf("alice post_count = %d, want %d", got, posts0+2)
	}
	var mark time.Time
	if err := pool.QueryRow(ctx, `select marked_at from topic_marks where user_id = $1 and topic_id = $2`, alice, p.TopicID).Scan(&mark); err != nil {
		t.Fatalf("alice has no topic mark: %v", err)
	}
	if !mark.Equal(t1) {
		t.Errorf("topic mark = %v, want the reply's time %v", mark, t1)
	}
	wantCountersExact(t, pool, "Reply")

	t.Run("held without post.no_approval", func(t *testing.T) {
		// carol has the standard role, which does not hold post.no_approval or post.counts.
		grantOpts(t, pool, carol, 2, "post.counts")
		c := viewerFor(t, pool, carol)
		topic0, forum0 := readTopic(t, pool, 1), readForum(t, pool, 2)
		count0 := userPostCount(t, pool, carol)
		t2 := t0.Add(time.Hour)
		hid, err := board.Reply(ctx, pool, c, 1, board.Draft{Source: "Wait for me", IP: ip2}, t2)
		if err != nil {
			t.Fatal(err)
		}
		if h := readPost(t, pool, hid); h.Vis != "unapproved" || h.IP != "10.1.2.4" {
			t.Errorf("held reply = %+v", h)
		}
		if got := readTopic(t, pool, 1); got != topic0 {
			t.Errorf("topic 1 changed: %+v, before %+v", got, topic0)
		}
		if got := readForum(t, pool, 2); got != forum0 {
			t.Errorf("forum 2 changed: %+v, before %+v", got, forum0)
		}
		if got := userPostCount(t, pool, carol); got != count0 {
			t.Errorf("carol post_count = %d, want %d: a held post does not count", got, count0)
		}
		var mark time.Time
		if err := pool.QueryRow(ctx, `select marked_at from topic_marks where user_id = $1 and topic_id = 1`, carol).Scan(&mark); err != nil || !mark.Equal(t2) {
			t.Errorf("carol's topic mark = %v (%v), want %v", mark, err, t2)
		}
		wantCountersExact(t, pool, "a held Reply")

		// A held topic is held as a whole.
		nid, err := board.NewTopic(ctx, pool, c, 2, board.Draft{Title: "Held topic", Source: "text", IP: ip2}, t2)
		if err != nil {
			t.Fatal(err)
		}
		np := readPost(t, pool, nid)
		if np.Vis != "unapproved" || readTopic(t, pool, np.TopicID).Vis != "unapproved" {
			t.Errorf("held topic: post %q, topic %q", np.Vis, readTopic(t, pool, np.TopicID).Vis)
		}
		if got := readForum(t, pool, 2); got != forum0 {
			t.Errorf("forum 2 changed by a held topic: %+v, before %+v", got, forum0)
		}
		wantCountersExact(t, pool, "a held NewTopic")
	})
}

func TestGuestPost(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())

	t.Run("without the grant", func(t *testing.T) {
		g := asGuest(t, pool)
		_, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "hi", GuestName: "Visitor", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "guest reply without topic.reply")
		_, err = board.NewTopic(ctx, pool, g, 2, board.Draft{Title: "t", Source: "hi", GuestName: "Visitor", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "guest topic without topic.start")
		if n := countPosts(t, pool); n != 6 {
			t.Errorf("%d posts, want the fixture's 6", n)
		}
	})

	allowGuests(t, pool, 2, "topic.reply")
	g := asGuest(t, pool)

	t.Run("held", func(t *testing.T) {
		pid, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "Hello from outside", GuestName: " Visitor ", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		p := readPost(t, pool, pid)
		if p.AuthorID != 0 || p.AuthorName != "Visitor" || p.IP != "10.1.2.3" || p.Vis != "unapproved" {
			t.Errorf("guest post = %+v, want no author, name Visitor, address 10.1.2.3, held", p)
		}
		// A guest has no topic mark and no post count.
		var marks int
		if err := pool.QueryRow(ctx, `select count(*) from topic_marks where marked_at = $1`, t0).Scan(&marks); err != nil || marks != 0 {
			t.Errorf("topic marks at the guest's time = %d (%v), want 0", marks, err)
		}
		// The guest cannot start a topic: only topic.reply was granted.
		_, err = board.NewTopic(ctx, pool, g, 2, board.Draft{Title: "t", Source: "hi", GuestName: "Visitor", IP: ip1}, t0)
		wantIs(t, err, board.ErrForbidden, "guest topic")
		wantCountersExact(t, pool, "a guest Reply")
	})

	t.Run("name rules", func(t *testing.T) {
		const msg = "That name belongs to a member. Sign in, or choose another name."
		for _, name := range []string{"ALICE", "alice", " Alice ", "Bob", "dave"} {
			_, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "x " + name, GuestName: name, IP: ip1}, t0)
			wantInput(t, err, "guest_name", msg)
		}
		for _, name := range []string{"", "   ", "\u200b", strings.Repeat("v", 41), "line\nbreak", "tab\tname", "bad\xff"} {
			_, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "y", GuestName: name, IP: ip1}, t0)
			wantInput(t, err, "guest_name", "")
		}
		if _, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "ok 40", GuestName: strings.Repeat("v", 40), IP: ip1}, t0); err != nil {
			t.Errorf("a 40-character name: %v", err)
		}
		if _, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "ok name", GuestName: "Alicia", IP: ip1}, t0); err != nil {
			t.Errorf("a name that no member holds: %v", err)
		}
	})

	t.Run("a member ignores GuestName", func(t *testing.T) {
		m := viewerFor(t, pool, carol)
		pid, err := board.Reply(ctx, pool, m, 1, board.Draft{Source: "member text", GuestName: "alice", IP: ip1}, t0)
		if err != nil {
			t.Fatal(err)
		}
		if p := readPost(t, pool, pid); p.AuthorID != carol || p.AuthorName != "carol" {
			t.Errorf("member post = %+v", p)
		}
	})

	t.Run("no approval needed", func(t *testing.T) {
		allowGuests(t, pool, 2, "post.no_approval")
		g := asGuest(t, pool)
		pid, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "straight in", GuestName: "Visitor", IP: ip1}, t0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		if p := readPost(t, pool, pid); p.Vis != "approved" || p.AuthorID != 0 {
			t.Errorf("guest post = %+v", p)
		}
		wantCountersExact(t, pool, "an approved guest Reply")
	})

	t.Run("guest topic", func(t *testing.T) {
		allowGuests(t, pool, 2, "topic.start")
		g := asGuest(t, pool)
		pid, err := board.NewTopic(ctx, pool, g, 2, board.Draft{Title: "Locked out", Source: "Please help", GuestName: "Visitor", IP: ip1}, t0.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		p := readPost(t, pool, pid)
		tp := readTopic(t, pool, p.TopicID)
		if tp.AuthorID != 0 || tp.AuthorName != "Visitor" || p.AuthorName != "Visitor" || p.IP != "10.1.2.3" {
			t.Errorf("topic = %+v, post = %+v", tp, p)
		}
	})

	t.Run("a guest needs an address", func(t *testing.T) {
		_, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "no address", GuestName: "Visitor"}, t0)
		if err == nil {
			t.Error("a guest post without an address was saved")
		}
	})

	t.Run("a banned address", func(t *testing.T) {
		// The fixture bans 10.99.0.0/16.
		_, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "from a banned range", GuestName: "Visitor", IP: netipAddr("10.99.4.4")}, t0)
		wantIs(t, err, board.ErrForbidden, "guest from a banned address")
		if _, err := pool.Exec(ctx, `update bans set ends_at = $1 where kind = 'ip'`, t0.Add(-time.Hour)); err != nil {
			t.Fatal(err)
		}
		if _, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "ban ended", GuestName: "Visitor", IP: netipAddr("10.99.4.4")}, t0); err != nil {
			t.Errorf("after the ban ended: %v", err)
		}
	})
}

func TestDuplicatePostIsOne(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	c := viewerFor(t, pool, carol)
	d := board.Draft{Source: "Same words", IP: ip1}

	first, err := board.Reply(ctx, pool, a, 1, d, t0)
	if err != nil {
		t.Fatal(err)
	}
	n := countPosts(t, pool)
	second, err := board.Reply(ctx, pool, a, 1, d, t0.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Errorf("second reply = post %d, want the first post %d", second, first)
	}
	if got := countPosts(t, pool); got != n {
		t.Errorf("%d posts after the duplicate, want %d", got, n)
	}

	// The window is 60 seconds from the earlier post.
	third, err := board.Reply(ctx, pool, a, 1, d, t0.Add(61*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if third == first || countPosts(t, pool) != n+1 {
		t.Errorf("reply after 61 s = post %d (first %d), %d posts, want a new post", third, first, countPosts(t, pool))
	}

	// What makes it another post: other text, another topic, another author.
	other, err := board.Reply(ctx, pool, a, 1, board.Draft{Source: "Other words", IP: ip1}, t0.Add(62*time.Second))
	if err != nil || other == third {
		t.Errorf("other text: post %d, %v", other, err)
	}
	byCarol, err := board.Reply(ctx, pool, c, 1, d, t0.Add(63*time.Second))
	if err != nil || byCarol == third {
		t.Errorf("another author: post %d, %v", byCarol, err)
	}
	grantOpts(t, pool, alice, 3, "topic.reply")
	a = viewerFor(t, pool, alice)
	elsewhere, err := board.Reply(ctx, pool, a, 2, d, t0.Add(64*time.Second))
	if err != nil || elsewhere == third {
		t.Errorf("another topic: post %d, %v", elsewhere, err)
	}

	// A deleted post is not an earlier post: the author may send the text again.
	m := moderator(t, pool, bob)
	if err := board.DeletePost(ctx, pool, m, other, t0.Add(65*time.Second)); err != nil {
		t.Fatal(err)
	}
	again, err := board.Reply(ctx, pool, a, 1, board.Draft{Source: "Other words", IP: ip1}, t0.Add(66*time.Second))
	if err != nil || again == other {
		t.Errorf("after the delete: post %d, %v, want a new post", again, err)
	}

	t.Run("new topic", func(t *testing.T) {
		nd := board.Draft{Title: "Twice", Source: "Only once", IP: ip1}
		p1, err := board.NewTopic(ctx, pool, a, 2, nd, t0.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		topics := 0
		if err := pool.QueryRow(ctx, `select count(*) from topics`).Scan(&topics); err != nil {
			t.Fatal(err)
		}
		p2, err := board.NewTopic(ctx, pool, a, 2, nd, t0.Add(time.Hour+10*time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if p2 != p1 {
			t.Errorf("second topic = post %d, want the first post %d of the earlier topic", p2, p1)
		}
		var after int
		if err := pool.QueryRow(ctx, `select count(*) from topics`).Scan(&after); err != nil || after != topics {
			t.Errorf("%d topics after the duplicate, want %d (%v)", after, topics, err)
		}
		// Another title is another topic.
		nd.Title = "Twice again"
		p3, err := board.NewTopic(ctx, pool, a, 2, nd, t0.Add(time.Hour+11*time.Second))
		if err != nil || p3 == p1 {
			t.Errorf("another title: post %d, %v", p3, err)
		}
		// After the window, the same topic is new.
		nd.Title = "Twice"
		p4, err := board.NewTopic(ctx, pool, a, 2, nd, t0.Add(time.Hour+61*time.Second))
		if err != nil || p4 == p1 {
			t.Errorf("after 61 s: post %d, %v", p4, err)
		}
		wantCountersExact(t, pool, "duplicate topics")
	})

	t.Run("guests", func(t *testing.T) {
		allowGuests(t, pool, 2, "topic.reply")
		g := asGuest(t, pool)
		gd := board.Draft{Source: "Guest words", GuestName: "Visitor", IP: ip1}
		g1, err := board.Reply(ctx, pool, g, 1, gd, t0.Add(2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		g2, err := board.Reply(ctx, pool, g, 1, gd, t0.Add(2*time.Hour+5*time.Second))
		if err != nil || g2 != g1 {
			t.Errorf("same address: post %d (first %d), %v", g2, g1, err)
		}
		gd.IP = ip2
		g3, err := board.Reply(ctx, pool, g, 1, gd, t0.Add(2*time.Hour+6*time.Second))
		if err != nil || g3 == g1 {
			t.Errorf("another address: post %d (first %d), %v", g3, g1, err)
		}
		// A guest with another name from the same address is still the same author.
		gd.IP = ip1
		gd.GuestName = "Someone"
		g4, err := board.Reply(ctx, pool, g, 1, gd, t0.Add(2*time.Hour+7*time.Second))
		if err != nil || g4 != g1 {
			t.Errorf("same address, other name: post %d (first %d), %v", g4, g1, err)
		}
	})
}

// TestDuplicatePostRace sends the same reply from two goroutines at once. The
// check for a duplicate and the insert are not one statement, so only a lock on
// the author makes the result one post.
func TestDuplicatePostRace(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	allowGuests(t, pool, 2, "topic.reply")
	cases := []struct {
		name string
		v    board.Viewer
		d    func(text string) board.Draft
	}{
		{"member", viewerFor(t, pool, alice), func(s string) board.Draft { return board.Draft{Source: s, IP: ip1} }},
		{"guest", asGuest(t, pool), func(s string) board.Draft { return board.Draft{Source: s, GuestName: "Visitor", IP: ip2} }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			for round := range 6 {
				text := c.name + " race " + string(rune('a'+round))
				before := countPosts(t, pool)
				ids := make([]int64, 2)
				errs := make([]error, 2)
				start := make(chan struct{})
				var wg sync.WaitGroup
				for i := range ids {
					wg.Add(1)
					go func() {
						defer wg.Done()
						<-start
						ids[i], errs[i] = board.Reply(ctx, pool, c.v, 1, c.d(text), t0.Add(time.Duration(round)*time.Hour))
					}()
				}
				close(start)
				wg.Wait()
				for i, err := range errs {
					if err != nil {
						t.Fatalf("round %d, call %d: %v", round, i, err)
					}
				}
				if ids[0] != ids[1] {
					t.Errorf("round %d: the two replies are posts %d and %d, want one post", round, ids[0], ids[1])
				}
				if got := countPosts(t, pool); got != before+1 {
					t.Errorf("round %d: %d posts, want %d", round, got, before+1)
				}
			}
			wantCountersExact(t, pool, "racing replies")
		})
	}
}

func TestDuplicateTopicRace(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	for round := range 6 {
		d := board.Draft{Title: "Race " + string(rune('a'+round)), Source: "same text", IP: ip1}
		var topicsBefore int
		if err := pool.QueryRow(ctx, `select count(*) from topics`).Scan(&topicsBefore); err != nil {
			t.Fatal(err)
		}
		ids := make([]int64, 2)
		errs := make([]error, 2)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				<-start
				ids[i], errs[i] = board.NewTopic(ctx, pool, a, 2, d, t0.Add(time.Duration(round)*time.Hour))
			}()
		}
		close(start)
		wg.Wait()
		for i, err := range errs {
			if err != nil {
				t.Fatalf("round %d, call %d: %v", round, i, err)
			}
		}
		var topicsAfter int
		if err := pool.QueryRow(ctx, `select count(*) from topics`).Scan(&topicsAfter); err != nil {
			t.Fatal(err)
		}
		if ids[0] != ids[1] || topicsAfter != topicsBefore+1 {
			t.Errorf("round %d: posts %d and %d, %d new topics, want one post and one topic", round, ids[0], ids[1], topicsAfter-topicsBefore)
		}
	}
	wantCountersExact(t, pool, "racing new topics")
}

func TestNewTopicAndReplyInput(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	v := viewerFor(t, pool, alice)
	n := countPosts(t, pool)

	for _, title := range []string{"", "   ", strings.Repeat("t", 121), "line\nbreak", "nul\x00", "bad\xff"} {
		_, err := board.NewTopic(ctx, pool, v, 2, board.Draft{Title: title, Source: "text", IP: ip1}, t0)
		wantInput(t, err, "title", "")
	}
	// Ruling R46: a title refuses the hidden formatting characters of ruling R40,
	// with a message that says why.
	for _, title := range []string{"a\u202eb", "a\u2066b", "a\u200bb"} {
		_, err := board.NewTopic(ctx, pool, v, 2, board.Draft{Title: title, Source: "text", IP: ip1}, t0)
		wantInput(t, err, "title", "Remove the hidden formatting character.")
	}
	// The joiners of emoji sequences pass.
	if _, err := board.NewTopic(ctx, pool, v, 2, board.Draft{Title: "Family \U0001F468\u200d\U0001F469", Source: "text", IP: ip1}, t0); err != nil {
		t.Errorf("a title with a zero-width joiner: %v", err)
	}
	// 120 characters are allowed, counted as characters and not bytes.
	if _, err := board.NewTopic(ctx, pool, v, 2, board.Draft{Title: strings.Repeat("é", 120), Source: "text", IP: ip1}, t0); err != nil {
		t.Errorf("a title of 120 characters: %v", err)
	}
	_, err := board.NewTopic(ctx, pool, v, 2, board.Draft{Title: "ok", Source: "", IP: ip1}, t0)
	wantInput(t, err, "body", "")
	_, err = board.Reply(ctx, pool, v, 1, board.Draft{Source: "", IP: ip1}, t0)
	wantInput(t, err, "body", "")
	_, err = board.Reply(ctx, pool, v, 1, board.Draft{Source: strings.Repeat("a", 60001), IP: ip1}, t0)
	wantInput(t, err, "body", "")
	_, err = board.Reply(ctx, pool, v, 1, board.Draft{Source: "![x](https://example.com/a.png)", IP: ip1}, t0)
	wantInput(t, err, "body", msgImages)
	if got := countPosts(t, pool); got != n+2 {
		t.Errorf("%d posts, want %d: only the 120-character title and the joiner title saved", got, n+2)
	}
	// A reply ignores a title.
	pid, err := board.Reply(ctx, pool, v, 1, board.Draft{Title: strings.Repeat("t", 500), Source: "title ignored", IP: ip1}, t0)
	if err != nil {
		t.Fatalf("a reply with a long Title: %v", err)
	}
	if p := readPost(t, pool, pid); p.Subject != "Re: Welcome to the example board" {
		t.Errorf("subject = %q", p.Subject)
	}
	wantCountersExact(t, pool, "input errors")
}

func TestQuoteSourceHidden(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	// A post in a forum the viewer cannot read: post 5 is in forum 3.
	_, err := board.QuoteSource(ctx, pool, c, 5)
	wantIs(t, err, board.ErrNotFound, "post in an unreadable forum")
	// An unapproved post of another member: post 3 is carol's, so use alice's view of it.
	a := viewerFor(t, pool, alice)
	_, err = board.QuoteSource(ctx, pool, a, 3)
	wantIs(t, err, board.ErrNotFound, "unapproved post of another member")
	// A deleted post.
	if _, err := pool.Exec(ctx, `update posts set visibility = 'deleted' where id = 2`); err != nil {
		t.Fatal(err)
	}
	_, err = board.QuoteSource(ctx, pool, a, 2)
	wantIs(t, err, board.ErrNotFound, "deleted post")
	// A moderator who can see the deleted and the held post may not quote them either.
	grantOpts(t, pool, bob, 2, "mod.delete")
	m := viewerFor(t, pool, bob)
	_, err = board.QuoteSource(ctx, pool, m, 2)
	wantIs(t, err, board.ErrNotFound, "deleted post, viewed by a moderator")
	_, err = board.QuoteSource(ctx, pool, m, 3)
	wantIs(t, err, board.ErrNotFound, "unapproved post, viewed by a moderator")
	// A missing post.
	_, err = board.QuoteSource(ctx, pool, a, 9999)
	wantIs(t, err, board.ErrNotFound, "missing post")
	// A post in a deleted topic.
	if _, err := pool.Exec(ctx, `update posts set visibility = 'approved' where id = 2; update topics set visibility = 'deleted' where id = 1`); err != nil {
		t.Fatal(err)
	}
	_, err = board.QuoteSource(ctx, pool, m, 2)
	wantIs(t, err, board.ErrNotFound, "post in a deleted topic")
}

func TestQuoteSource(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)

	got, err := board.QuoteSource(ctx, pool, a, 2)
	if err != nil {
		t.Fatal(err)
	}
	want := "> **bob** [wrote](/p/2):\n>\n> Thanks! Here is a diagram:\n>\n> ![diagram](/a/1)\n\n"
	if got != want {
		t.Errorf("QuoteSource =\n%q\nwant\n%q", got, want)
	}

	// A guest's name, a CRLF source and a quote inside a quote.
	if _, err := pool.Exec(ctx, `update posts set author_name = 'a*b_[c]`+"`"+`<d>', body_source = E'one\r\n\r\n> two\rthree' where id = 4`); err != nil {
		t.Fatal(err)
	}
	got, err = board.QuoteSource(ctx, pool, a, 4)
	if err != nil {
		t.Fatal(err)
	}
	want = "> **a\\*b\\_\\[c\\]\\`\\<d\\>** [wrote](/p/4):\n>\n> one\n>\n> > two\n> three\n\n"
	if got != want {
		t.Errorf("QuoteSource =\n%q\nwant\n%q", got, want)
	}

	// The quote parses to a quote element that holds the quoted text.
	b, err := board.ParseBody(ctx, pool, a, 2, got, nil)
	if err != nil {
		t.Fatal(err)
	}
	if !hasElement(b.Tree, "quote") {
		t.Errorf("tree = %s, want a quote", b.JSON)
	}

	// The reply rights belong to the topic that the reply goes into (ruling T3-C1):
	// a viewer who reads the post may quote it, whatever the rights in its forum.
	denyOpts(t, pool, carol, 2, "topic.reply")
	if _, err := board.QuoteSource(ctx, pool, viewerFor(t, pool, carol), 2); err != nil {
		t.Errorf("quote without topic.reply in the forum of the post: %v", err)
	}
}

// TestGuestNameShowsWhatTheCheckSaw: a guest name that CleanUsername changes
// for any reason but case is refused, so the page never shows a name other than
// the one that the member-name check compared.
func TestGuestNameShowsWhatTheCheckSaw(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	allowGuests(t, pool, 2, "topic.reply")
	g := asGuest(t, pool)
	const msg = "Use letters, digits, spaces and common punctuation only."
	names := []string{
		"\u202eecila",        // right-to-left override: shows as "alice"
		"\u202enimda",        // the same for "admin"
		"ali\u200dce",        // zero-width joiner
		"adm\u200din",        // zero-width joiner
		"ali\u00adce",        // soft hyphen
		"adm\u00adin",        // soft hyphen
		"ali\ufe0fce",        // variation selector
		"al\u200bice",        // zero-width space
		"ａｌｉｃｅ",              // full-width letters: NFKC changes them
		"Admin\u2066x\u2069", // isolate marks
	}
	for _, name := range names {
		_, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "x", GuestName: name, IP: ip1}, t0)
		wantInput(t, err, "guest_name", msg)
	}
	// Case alone is fine, and so are letters, digits, spaces and common punctuation.
	for _, name := range []string{"Visitor", "VISITOR", "Jo Smith", "Mary-Ann O'Neil", "user_42", "Zoë", "Ünal (guest)"} {
		if _, err := board.Reply(ctx, pool, g, 1, board.Draft{Source: "ok " + name, GuestName: name, IP: ip1}, t0); err != nil {
			t.Errorf("name %q: %v", name, err)
		}
	}
	if n := countPosts(t, pool); n != 6+7 {
		t.Errorf("%d posts, want 13: only the plain names saved", n)
	}
}

func TestWriteNeedsAnAddress(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a := viewerFor(t, pool, alice)
	n := countPosts(t, pool)
	if _, err := board.Reply(ctx, pool, a, 1, board.Draft{Source: "no address"}, t0); err == nil {
		t.Error("a member's reply without an address was saved")
	}
	if _, err := board.NewTopic(ctx, pool, a, 2, board.Draft{Title: "t", Source: "no address"}, t0); err == nil {
		t.Error("a member's topic without an address was saved")
	}
	if got := countPosts(t, pool); got != n {
		t.Errorf("%d posts, want %d", got, n)
	}
}

func TestQuoteSourceIgnoresTheLockOfTheQuotedPost(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	if _, err := board.QuoteSource(ctx, pool, c, 2); err != nil {
		t.Fatalf("unlocked: %v", err)
	}
	if _, err := pool.Exec(ctx, `update topics set locked = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	// The lock belongs to the topic that the reply goes into, which the caller
	// checks (board.CanReply). The topic and forum of the quoted post do not
	// stop a quote.
	if _, err := board.QuoteSource(ctx, pool, c, 2); err != nil {
		t.Errorf("locked topic of the quoted post: %v", err)
	}
	if _, err := pool.Exec(ctx, `update topics set locked = false where id = 1; update forums set locked = true where id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := board.QuoteSource(ctx, pool, viewerFor(t, pool, alice), 2); err != nil {
		t.Errorf("locked forum of the quoted post: %v", err)
	}
}

// TestEditKeepsStoredImages: a writer without post.images may keep the image
// URLs that the stored post holds, and may add none.
func TestEditKeepsStoredImages(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Post 2 is bob's reply and holds the image /a/1.
	grantOpts(t, pool, carol, 2, "mod.edit")
	m := viewerFor(t, pool, carol)
	edit := func(v board.Viewer, src string, at int) error {
		return board.EditPost(ctx, pool, v, 2, board.Draft{Source: src}, "r", t0.Add(time.Duration(at)*time.Minute))
	}
	if err := edit(m, "Reworded.\n\n![diagram](/a/1)", 1); err != nil {
		t.Errorf("keep the stored image: %v", err)
	}
	if err := edit(m, "Only text now.", 2); err != nil {
		t.Errorf("drop the image: %v", err)
	}
	// The stored post no longer holds an image, so none may come back.
	wantInput(t, edit(m, "Again.\n\n![diagram](/a/1)", 3), "body", msgImages)

	// Put the image back through the database, then try to add a second one.
	if _, err := pool.Exec(ctx, `
update posts set body_source = 'Thanks!' || E'\n\n' || '![diagram](/a/1)',
       body_tree = '{"k":0,"c":[{"k":2,"n":"p","c":[{"k":1,"t":"Thanks!"}]},{"k":2,"n":"p","c":[{"k":2,"n":"img","a":{"src":"/a/1","alt":"diagram"}}]}]}'
 where id = 2`); err != nil {
		t.Fatal(err)
	}
	wantInput(t, edit(m, "Thanks!\n\n![diagram](/a/1)\n\n![other](/a/2)", 4), "body", msgImages)
	wantInput(t, edit(m, "![other](https://example.com/b.png)", 5), "body", msgImages)
	if err := edit(m, "Thanks, edited!\n\n![diagram](/a/1)", 6); err != nil {
		t.Errorf("keep the image again: %v", err)
	}
	// With post.images, a new image is fine.
	grantOpts(t, pool, carol, 2, "post.images")
	if err := edit(viewerFor(t, pool, carol), "![other](/a/2)", 7); err != nil {
		t.Errorf("with post.images: %v", err)
	}

	// The same through ParseBodyKeeping, for a preview of an edit.
	stored := markup.Parse("![x](https://example.com/a.png)", markup.Options{Markdown: true, Autolink: true})
	plain := viewerFor(t, pool, alice)
	flags := &markup.Options{Markdown: true, Autolink: true}
	if _, err := board.ParseBodyKeeping(ctx, pool, plain, 2, "![x](https://example.com/a.png) more", flags, stored); err != nil {
		t.Errorf("ParseBodyKeeping, same image: %v", err)
	}
	_, err := board.ParseBodyKeeping(ctx, pool, plain, 2, "![x](https://example.com/c.png)", flags, stored)
	wantInput(t, err, "body", msgImages)
}
