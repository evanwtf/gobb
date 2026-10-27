package board_test

import (
	"bufio"
	"context"
	"errors"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/htmlcheck"
	"github.com/evanwtf/gobb/markup"
)

const dave = int64(4) // inactive

// loadMessages loads the small board and shapes the two fixture messages for the
// tests. The fixture's message 1 is read by bob, and its message 2 is deleted by
// alice, who is its recipient. The tests start with bob's copy of message 1
// unread and alice's copy of message 2 in her inbox.
func loadMessages(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `update pm_recipients set is_read = false where pm_id = 1 and user_id = $1`, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update pm_recipients set is_deleted = false where pm_id = 2 and user_id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	return pool
}

func messageIDs(l board.MessageList) []int64 {
	out := []int64{}
	for _, r := range l.Rows {
		out = append(out, r.ID)
	}
	return out
}

func authorIDs(as []board.Author) []int64 {
	out := []int64{}
	for _, a := range as {
		out = append(out, a.ID)
	}
	return out
}

func TestInboxAndSent(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	b, a := viewerFor(t, pool, bob), viewerFor(t, pool, alice)

	in, err := board.Inbox(ctx, pool, b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(in); !slices.Equal(got, []int64{1}) {
		t.Fatalf("bob inbox = %v, want [1]", got)
	}
	r := in.Rows[0]
	if !r.Unread || r.Subject != "Welcome aboard" || r.From != (board.Author{ID: alice, Name: "alice"}) {
		t.Errorf("row = %+v", r)
	}
	// The blind copy is not in the list, and the recipient of kind "to" is.
	if got := authorIDs(r.To); !slices.Equal(got, []int64{bob}) {
		t.Errorf("To = %v, want [bob]", got)
	}
	if in.Page != 1 || in.Pages != 1 {
		t.Errorf("page = %d of %d", in.Page, in.Pages)
	}

	in, err = board.Inbox(ctx, pool, a, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(in); !slices.Equal(got, []int64{2}) {
		t.Errorf("alice inbox = %v, want [2]", got)
	}
	if in.Rows[0].Unread {
		t.Error("alice read message 2 already")
	}

	// Carol got message 1 as a blind copy: it is in her inbox, and unread.
	in, err = board.Inbox(ctx, pool, viewerFor(t, pool, carol), 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(in); !slices.Equal(got, []int64{1}) || !in.Rows[0].Unread {
		t.Errorf("carol inbox = %v %+v", got, in.Rows)
	}
	if got := authorIDs(in.Rows[0].To); !slices.Equal(got, []int64{bob}) {
		t.Errorf("carol sees To = %v, want [bob] and no blind copy", got)
	}

	sent, err := board.Sent(ctx, pool, b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(sent); len(got) != 0 {
		t.Errorf("bob sent = %v, want none: he deleted message 2", got)
	}
	sent, err = board.Sent(ctx, pool, a, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(sent); !slices.Equal(got, []int64{1}) {
		t.Errorf("alice sent = %v, want [1]", got)
	}
	if sent.Rows[0].Unread {
		t.Error("a sent row is unread")
	}
	if _, err := board.Sent(ctx, pool, a, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 = %v, want ErrNotFound", err)
	}
	if _, err := board.Inbox(ctx, pool, a, 0); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 0 = %v, want ErrNotFound", err)
	}
}

func TestListsNeedPMRead(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	denyOpts(t, pool, bob, 0, "pm.read")
	b := viewerFor(t, pool, bob)
	if _, err := board.Inbox(ctx, pool, b, 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("Inbox = %v, want ErrForbidden", err)
	}
	if _, err := board.Sent(ctx, pool, b, 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("Sent = %v, want ErrForbidden", err)
	}
	if _, err := openMessage(ctx, pool, b, 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("openMessage = %v, want ErrForbidden", err)
	}
	if _, err := board.ReplyDraft(ctx, pool, b, 1, false); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("ReplyDraft = %v, want ErrForbidden", err)
	}
	if n, err := board.UnreadMessages(ctx, pool, b); err != nil || n != 0 {
		t.Errorf("UnreadMessages = %d, %v, want 0", n, err)
	}
	// A guest has no messages.
	g := asGuest(t, pool)
	if _, err := board.Inbox(ctx, pool, g, 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest Inbox = %v, want ErrForbidden", err)
	}
	if n, err := board.UnreadMessages(ctx, pool, g); err != nil || n != 0 {
		t.Errorf("guest UnreadMessages = %d, %v, want 0", n, err)
	}
}

func TestReadMarksRead(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	b := viewerFor(t, pool, bob)

	unread := func() int {
		t.Helper()
		n, err := board.UnreadMessages(ctx, pool, b)
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := unread(); n != 1 {
		t.Fatalf("unread before = %d, want 1", n)
	}
	// A list does not mark anything read.
	if _, err := board.Inbox(ctx, pool, b, 1); err != nil {
		t.Fatal(err)
	}
	if n := unread(); n != 1 {
		t.Fatalf("unread after the inbox = %d, want 1", n)
	}
	m, err := openMessage(ctx, pool, b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Welcome aboard" || m.From.Name != "alice" || m.From.ID != alice || m.Mine {
		t.Errorf("message = %+v", m)
	}
	if m.Tree == nil || !strings.Contains(string(markup.Render(m.Tree, markup.RenderContext{}).HTML), "Glad you joined") {
		t.Errorf("tree = %+v", m.Tree)
	}
	if n := unread(); n != 0 {
		t.Errorf("unread after reading = %d, want 0", n)
	}
	// The author's read does not touch the recipients' marks.
	if _, err := openMessage(ctx, pool, viewerFor(t, pool, alice), 1); err != nil {
		t.Fatal(err)
	}
	var read bool
	if err := pool.QueryRow(ctx, `select is_read from pm_recipients where pm_id = 1 and user_id = $1`, carol).Scan(&read); err != nil {
		t.Fatal(err)
	}
	if read {
		t.Error("alice reading message 1 marked carol's copy read")
	}
}

func TestMessageNotFoundForOthers(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	grantOpts(t, pool, carol, 0, "pm.delete")
	grantOpts(t, pool, bob, 0, "pm.delete")
	c, b := viewerFor(t, pool, carol), viewerFor(t, pool, bob)

	for _, who := range []struct {
		name string
		v    board.Viewer
	}{{"carol", c}, {"bob, the author who deleted it", b}} {
		m, err := openMessage(ctx, pool, who.v, 2)
		if !errors.Is(err, board.ErrNotFound) {
			t.Errorf("%s: openMessage = %v, want ErrNotFound", who.name, err)
		}
		if m.Subject != "" || m.Tree != nil {
			t.Errorf("%s: openMessage returned text: %+v", who.name, m)
		}
		if err := board.DeleteMessage(ctx, pool, who.v, 2); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("%s: DeleteMessage = %v, want ErrNotFound", who.name, err)
		}
		d, err := board.ReplyDraft(ctx, pool, who.v, 2, true)
		if !errors.Is(err, board.ErrNotFound) {
			t.Errorf("%s: ReplyDraft = %v, want ErrNotFound", who.name, err)
		}
		if d.Subject != "" || d.Source != "" || d.To != "" {
			t.Errorf("%s: ReplyDraft returned text: %+v", who.name, d)
		}
	}
	// A message that does not exist gives the same answer.
	if _, err := openMessage(ctx, pool, c, 999); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("missing message: %v, want ErrNotFound", err)
	}
	// Dave is not active, but he is no party to message 1 either.
	if _, err := openMessage(ctx, pool, viewerFor(t, pool, dave), 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("dave: %v, want ErrNotFound", err)
	}
	// The failed deletes changed nothing.
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from pms where author_deleted`).Scan(&n); err != nil || n != 1 {
		t.Errorf("author_deleted rows = %d, %v, want 1", n, err)
	}
}

func TestBccHidden(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	// Dave is a stranger who got the message as a visible recipient, to try the
	// "to" side: a third "to" recipient must not see the blind copy.
	if _, err := pool.Exec(ctx, `insert into pm_recipients (pm_id, user_id, kind) values (1, $1, 'to')`, dave); err != nil {
		t.Fatal(err)
	}
	read := func(id int64) board.Message {
		t.Helper()
		m, err := openMessage(ctx, pool, viewerFor(t, pool, id), 1)
		if err != nil {
			t.Fatalf("user %d: %v", id, err)
		}
		return m
	}
	if got := authorIDs(read(bob).Bcc); len(got) != 0 {
		t.Errorf("bob sees Bcc %v, want none", got)
	}
	if got := authorIDs(read(carol).Bcc); !slices.Equal(got, []int64{carol}) {
		t.Errorf("carol sees Bcc %v, want [carol]", got)
	}
	m := read(alice)
	if got := authorIDs(m.Bcc); !slices.Equal(got, []int64{carol}) || !m.Mine {
		t.Errorf("alice sees Bcc %v, Mine %v, want [carol], true", got, m.Mine)
	}
	// Nobody sees the blind copy among the visible recipients.
	for _, id := range []int64{alice, bob, carol, dave} {
		if got := authorIDs(read(id).To); !slices.Equal(got, []int64{bob, dave}) {
			t.Errorf("user %d sees To %v, want [bob dave]", id, got)
		}
	}
	// A second blind copy stays private to its owner too.
	if _, err := pool.Exec(ctx, `update pm_recipients set kind = 'bcc' where pm_id = 1 and user_id = $1`, dave); err != nil {
		t.Fatal(err)
	}
	if got := authorIDs(read(carol).Bcc); !slices.Equal(got, []int64{carol}) {
		t.Errorf("carol sees Bcc %v with two blind copies, want [carol]", got)
	}
	if got := authorIDs(read(alice).Bcc); !slices.Equal(got, []int64{carol, dave}) {
		t.Errorf("alice sees Bcc %v, want [carol dave]", got)
	}
	// An author who deleted the message is no author any more.
	if _, err := pool.Exec(ctx, `update pms set author_deleted = true where id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := openMessage(ctx, pool, viewerFor(t, pool, alice), 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("alice after deleting: %v, want ErrNotFound", err)
	}
	if got := authorIDs(read(carol).Bcc); !slices.Equal(got, []int64{carol}) {
		t.Errorf("carol sees Bcc %v after the author left, want [carol]", got)
	}
}

func TestGoneAuthorHasNoLink(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	if _, err := pool.Exec(ctx, `update pms set author_id = null where id = 2`); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	m, err := openMessage(ctx, pool, a, 2)
	if err != nil {
		t.Fatal(err)
	}
	if m.From != (board.Author{Name: "bob"}) || m.Mine {
		t.Errorf("From = %+v, Mine = %v, want the name with no ID", m.From, m.Mine)
	}
	in, err := board.Inbox(ctx, pool, a, 1)
	if err != nil {
		t.Fatal(err)
	}
	if in.Rows[0].From != (board.Author{Name: "bob"}) {
		t.Errorf("row From = %+v", in.Rows[0].From)
	}
	d, err := board.ReplyDraft(ctx, pool, a, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.To != "" {
		t.Errorf("reply To = %q, want empty for a gone author", d.To)
	}
}

func TestSendRules(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	grantOpts(t, pool, alice, 0, "pm.send_many")
	a := viewerFor(t, pool, alice)
	send := func(v board.Viewer, to, subject, src string) error {
		_, err := board.SendMessage(ctx, pool, v, board.MessageDraft{To: to, Subject: subject, Source: src, IP: ip1}, t0)
		return err
	}
	count := func() int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from pms`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	before := count()

	t.Run("none", func(t *testing.T) {
		for _, to := range []string{"", "  ", " , ,"} {
			wantInput(t, send(a, to, "S", "x"), "to", "Add at least one member.")
		}
	})
	t.Run("unknown", func(t *testing.T) {
		wantInput(t, send(a, "nobody", "S", "x"), "to", "No member named “nobody”.")
		wantInput(t, send(a, "bob, nobody", "S", "x"), "to", "No member named “nobody”.")
		wantInput(t, send(a, strings.Repeat("n", 300), "S", "x"), "to", "No member named “"+strings.Repeat("n", 40)+"…”.")
	})
	t.Run("not active", func(t *testing.T) {
		wantInput(t, send(a, "dave", "S", "x"), "to", "No member named “dave”.")
	})
	t.Run("self", func(t *testing.T) {
		wantInput(t, send(a, "Alice", "S", "x"), "to", "You cannot send a message to yourself.")
		wantInput(t, send(a, "bob, ALICE", "S", "x"), "to", "You cannot send a message to yourself.")
	})
	t.Run("too many", func(t *testing.T) {
		names := make([]string, 11)
		for i := range names {
			names[i] = "member" + strconv.Itoa(i)
		}
		wantInput(t, send(a, strings.Join(names, ","), "S", "x"), "to", "Send to at most 10 members.")
	})
	t.Run("many without the right", func(t *testing.T) {
		err := send(viewerFor(t, pool, carol), "alice, bob", "S", "x")
		wantIs(t, err, board.ErrForbidden, "carol to two")
		// The refusal says why, so a caller can tell it from the other refusals.
		wantIs(t, err, board.ErrSendMany, "carol to two")
		if err := send(viewerFor(t, pool, carol), "alice", "One recipient", "x"); err != nil {
			t.Errorf("one recipient: %v", err)
		}
		before++
	})
	t.Run("the same name twice is one", func(t *testing.T) {
		// Carol lacks pm.send_many, and "Bob" and "bob" are one member.
		if err := send(viewerFor(t, pool, carol), "Bob, bob , BOB", "Twice", "x"); err != nil {
			t.Errorf("one member named three ways: %v", err)
		}
		before++
	})
	t.Run("subject", func(t *testing.T) {
		wantInput(t, send(a, "bob", "", "x"), "subject", "")
		wantInput(t, send(a, "bob", "   ", "x"), "subject", "")
		wantInput(t, send(a, "bob", strings.Repeat("s", 121), "x"), "subject", "")
		wantInput(t, send(a, "bob", "two\nlines", "x"), "subject", "")
		if err := send(a, "bob", " "+strings.Repeat("é", 120)+" ", "x"); err != nil {
			t.Errorf("120 characters: %v", err)
		}
		before++
	})
	t.Run("body", func(t *testing.T) {
		wantInput(t, send(a, "bob", "S", ""), "body", "")
		wantInput(t, send(a, "bob", "S", "  \n "), "body", "")
		wantInput(t, send(a, "bob", "S", strings.Repeat("a", 60_001)), "body", "")
		wantInput(t, send(a, "bob", "S", "bad \x00 text"), "body", "")
		wantInput(t, send(a, "bob", "S", "![x](https://example.com/a.png)"), "body", "You may not put images in messages.")
	})
	t.Run("no pm.send", func(t *testing.T) {
		denyOpts(t, pool, carol, 0, "pm.send")
		wantIs(t, send(viewerFor(t, pool, carol), "alice", "S", "x"), board.ErrForbidden, "carol without pm.send")
		wantIs(t, send(asGuest(t, pool), "alice", "S", "x"), board.ErrForbidden, "guest")
	})
	t.Run("images with pm.images", func(t *testing.T) {
		grantOpts(t, pool, alice, 0, "pm.images")
		if err := send(viewerFor(t, pool, alice), "bob", "Pic", "![x](https://example.com/a.png)"); err != nil {
			t.Errorf("image with pm.images: %v", err)
		}
		before++
	})
	t.Run("no address", func(t *testing.T) {
		_, err := board.SendMessage(ctx, pool, a, board.MessageDraft{To: "bob", Subject: "S", Source: "x"}, t0)
		if err == nil {
			t.Error("a send without an address was accepted")
		}
	})
	if got := count(); got != before {
		t.Errorf("pms = %d, want %d: a refused send stored a row", got, before)
	}
	if t.Failed() {
		return
	}
	// 10 names are allowed with the right: the 11th member would not exist, so
	// ten unknown names fail on the first, not on the count.
	names := make([]string, 10)
	for i := range names {
		names[i] = "member" + strconv.Itoa(i)
	}
	wantInput(t, send(a, strings.Join(names, ","), "S", "x"), "to", "No member named “member0”.")
}

func TestSendStores(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	grantOpts(t, pool, alice, 0, "pm.send_many")
	a := viewerFor(t, pool, alice)

	id, err := board.SendMessage(ctx, pool, a, board.MessageDraft{To: "bob, Carol", Subject: " Lunch? ", Source: "**Noon**, at the usual place.", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	var (
		author                         int64
		name, ip, subject, source      string
		sig, md, smilies, auto, delete bool
	)
	err = pool.QueryRow(ctx, `select author_id, author_name, host(author_ip), subject, body_source, enable_sig,
		enable_markdown, enable_smilies, enable_autolink, author_deleted from pms where id = $1`, id).
		Scan(&author, &name, &ip, &subject, &source, &sig, &md, &smilies, &auto, &delete)
	if err != nil {
		t.Fatal(err)
	}
	if author != alice || name != "alice" || ip != "10.1.2.3" || subject != "Lunch?" || source != "**Noon**, at the usual place." {
		t.Errorf("stored = %d %q %q %q %q", author, name, ip, subject, source)
	}
	if !sig || !md || !smilies || !auto || delete {
		t.Errorf("flags: sig %v markdown %v smilies %v autolink %v author_deleted %v", sig, md, smilies, auto, delete)
	}
	rows, err := pool.Query(ctx, `select user_id, kind, is_read, is_deleted from pm_recipients where pm_id = $1 order by user_id`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var got []int64
	for rows.Next() {
		var u int64
		var kind string
		var read, deleted bool
		if err := rows.Scan(&u, &kind, &read, &deleted); err != nil {
			t.Fatal(err)
		}
		if kind != "to" || read || deleted {
			t.Errorf("recipient %d: kind %q read %v deleted %v", u, kind, read, deleted)
		}
		got = append(got, u)
	}
	if !slices.Equal(got, []int64{bob, carol}) {
		t.Errorf("recipients = %v, want [bob carol]", got)
	}
	rows.Close()

	for _, id2 := range []int64{bob, carol} {
		v := viewerFor(t, pool, id2)
		in, err := board.Inbox(ctx, pool, v, 1)
		if err != nil {
			t.Fatal(err)
		}
		if in.Rows[0].ID != id || in.Rows[0].Subject != "Lunch?" || !in.Rows[0].Unread {
			t.Errorf("user %d inbox top = %+v", id2, in.Rows[0])
		}
		if got := authorIDs(in.Rows[0].To); !slices.Equal(got, []int64{bob, carol}) {
			t.Errorf("user %d sees To %v, want [bob carol]", id2, got)
		}
	}
	m, err := openMessage(ctx, pool, a, id)
	if err != nil {
		t.Fatal(err)
	}
	if !m.Mine || len(m.Bcc) != 0 || !m.Flags.Markdown || !strings.Contains(string(markup.Render(m.Tree, markup.RenderContext{}).HTML), "<strong>Noon</strong>") {
		t.Errorf("message = %+v", m)
	}
}

func TestMessageFlagsFollowPMRights(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	denyOpts(t, pool, carol, 0, "pm.markdown", "pm.smilies")
	c := viewerFor(t, pool, carol)
	b, err := board.ParseMessage(ctx, pool, c, "**x** :)")
	if err != nil {
		t.Fatal(err)
	}
	if b.Flags.Markdown || b.Flags.Smilies || !b.Flags.Autolink {
		t.Errorf("flags = %+v, want markdown and smilies off, autolink on", b.Flags)
	}
	// The post rights do not count for a message.
	grantOpts(t, pool, carol, 2, "post.markdown", "post.smilies", "post.images")
	c = viewerFor(t, pool, carol)
	b, err = board.ParseMessage(ctx, pool, c, "**x**")
	if err != nil || b.Flags.Markdown {
		t.Errorf("post.markdown changed a message: %+v, %v", b.Flags, err)
	}
	// The default member holds both pm flags, and no right to images: post.images
	// does not count either.
	grantOpts(t, pool, bob, 2, "post.images")
	bv := viewerFor(t, pool, bob)
	b, err = board.ParseMessage(ctx, pool, bv, "**x** :)")
	if err != nil || !b.Flags.Markdown || !b.Flags.Smilies {
		t.Errorf("bob flags = %+v, %v", b.Flags, err)
	}
	_, err = board.ParseMessage(ctx, pool, bv, "![x](https://example.com/a.png)")
	wantInput(t, err, "body", "You may not put images in messages.")
	// The limit is 60,000 bytes, and 60,000 passes.
	if _, err := board.ParseMessage(ctx, pool, c, strings.Repeat("a", 60_000)); err != nil {
		t.Errorf("60,000 bytes: %v", err)
	}
	_, err = board.ParseMessage(ctx, pool, c, strings.Repeat("a", 60_001))
	wantInput(t, err, "body", "")
}

var payloadEscape = regexp.MustCompile(`\{(?:U\+([0-9A-Fa-f]{4,6})|X:([0-9A-Fa-f]{2}))\}`)

// loadPayloads reads the XSS corpus of the markup package, one payload a line.
func loadPayloads(t *testing.T) []string {
	t.Helper()
	f, err := os.Open("../../markup/testdata/xss/payloads.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	var out []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "%%") || line == "" {
			continue
		}
		out = append(out, payloadEscape.ReplaceAllStringFunc(line, func(m string) string {
			sub := payloadEscape.FindStringSubmatch(m)
			if sub[1] != "" {
				v, _ := strconv.ParseUint(sub[1], 16, 32)
				return string(rune(v))
			}
			v, _ := strconv.ParseUint(sub[2], 16, 8)
			return string([]byte{byte(v)})
		}))
	}
	if err := sc.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestMessagePayloadsSafe(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	grantOpts(t, pool, alice, 0, "pm.images")
	for _, v := range []board.Viewer{viewerFor(t, pool, alice), viewerFor(t, pool, carol)} {
		stored := 0
		for _, p := range loadPayloads(t) {
			b, err := board.ParseMessage(ctx, pool, v, p)
			if err != nil {
				// A payload may be refused (no text, a null byte, an image). A refusal
				// stores nothing, which is safe too.
				var ie *board.InputError
				if !errors.As(err, &ie) {
					t.Fatalf("payload %q: %v", p, err)
				}
				continue
			}
			stored++
			if err := markup.Validate(b.Tree); err != nil {
				t.Errorf("payload %q: Validate: %v", p, err)
			}
			htmlcheck.Check(t, string(markup.Render(b.Tree, markup.RenderContext{}).HTML))
		}
		if stored < 500 {
			t.Errorf("user %d: only %d payloads parsed", v.UserID, stored)
		}
	}
	// The same through a send and a read.
	a := viewerFor(t, pool, alice)
	id, err := board.SendMessage(ctx, pool, a, board.MessageDraft{To: "bob", Subject: "XSS", Source: "<script>alert(1)</script> [x](javascript:alert(2))", IP: ip1}, t0)
	if err != nil {
		t.Fatal(err)
	}
	m, err := openMessage(ctx, pool, viewerFor(t, pool, bob), id)
	if err != nil {
		t.Fatal(err)
	}
	out := string(markup.Render(m.Tree, markup.RenderContext{}).HTML)
	htmlcheck.Check(t, out)
	if strings.Contains(out, "<script") || strings.Contains(out, "href=\"javascript") {
		t.Errorf("rendered HTML holds script: %s", out)
	}
}

func TestDeleteOwnView(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	// A member needs pm.delete.
	b := viewerFor(t, pool, bob)
	wantIs(t, board.DeleteMessage(ctx, pool, b, 1), board.ErrForbidden, "bob without pm.delete")
	in, err := board.Inbox(ctx, pool, b, 1)
	if err != nil || len(in.Rows) != 1 {
		t.Fatalf("the refused delete changed the inbox: %v %v", in.Rows, err)
	}

	grantOpts(t, pool, bob, 0, "pm.delete")
	b = viewerFor(t, pool, bob)
	if err := board.DeleteMessage(ctx, pool, b, 1); err != nil {
		t.Fatal(err)
	}
	in, err = board.Inbox(ctx, pool, b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(in.Rows) != 0 {
		t.Errorf("bob inbox = %v, want empty", messageIDs(in))
	}
	if _, err := openMessage(ctx, pool, b, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("bob reads a deleted message: %v, want ErrNotFound", err)
	}
	if n, err := board.UnreadMessages(ctx, pool, b); err != nil || n != 0 {
		t.Errorf("bob unread = %d, %v", n, err)
	}
	sent, err := board.Sent(ctx, pool, viewerFor(t, pool, alice), 1)
	if err != nil || !slices.Equal(messageIDs(sent), []int64{1}) {
		t.Errorf("alice sent = %v, %v, want [1]", messageIDs(sent), err)
	}
	in, err = board.Inbox(ctx, pool, viewerFor(t, pool, carol), 1)
	if err != nil || !slices.Equal(messageIDs(in), []int64{1}) {
		t.Errorf("carol inbox = %v, %v, want [1]", messageIDs(in), err)
	}
	// No row went away.
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from pm_recipients where pm_id = 1`).Scan(&n); err != nil || n != 2 {
		t.Errorf("recipient rows = %d, %v, want 2", n, err)
	}

	// The author deletes: the sent list loses it, the recipients keep it.
	grantOpts(t, pool, alice, 0, "pm.delete")
	a := viewerFor(t, pool, alice)
	if err := board.DeleteMessage(ctx, pool, a, 1); err != nil {
		t.Fatal(err)
	}
	if sent, err = board.Sent(ctx, pool, a, 1); err != nil || len(sent.Rows) != 0 {
		t.Errorf("alice sent = %v, %v, want empty", messageIDs(sent), err)
	}
	if in, err = board.Inbox(ctx, pool, viewerFor(t, pool, carol), 1); err != nil || !slices.Equal(messageIDs(in), []int64{1}) {
		t.Errorf("carol inbox = %v, %v, want [1]", messageIDs(in), err)
	}
	// A second delete finds nothing.
	if err := board.DeleteMessage(ctx, pool, a, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
	// A message that the viewer wrote and received is hidden in both views.
	if _, err := pool.Exec(ctx, `insert into pms (id, author_id, author_name, created_at, subject, body_tree, conversation_id)
		values (50, $1, 'carol', $2, 'Note to self', '{}'::jsonb, 50)`, carol, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `insert into pm_recipients (pm_id, user_id, kind) values (50, $1, 'to')`, carol); err != nil {
		t.Fatal(err)
	}
	grantOpts(t, pool, carol, 0, "pm.delete")
	if err := board.DeleteMessage(ctx, pool, viewerFor(t, pool, carol), 50); err != nil {
		t.Fatal(err)
	}
	var ad, rd bool
	if err := pool.QueryRow(ctx, `select p.author_deleted, r.is_deleted from pms p join pm_recipients r on r.pm_id = p.id where p.id = 50`).Scan(&ad, &rd); err != nil || !ad || !rd {
		t.Errorf("author_deleted %v, is_deleted %v, %v: want both true", ad, rd, err)
	}
}

func TestDuplicateMessageIsOne(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	grantOpts(t, pool, alice, 0, "pm.send_many")
	a := viewerFor(t, pool, alice)
	d := board.MessageDraft{To: "bob, carol", Subject: "Same", Source: "same text", IP: ip1}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from pms where subject = 'Same'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	first, err := board.SendMessage(ctx, pool, a, d, t0)
	if err != nil {
		t.Fatal(err)
	}
	// The same message with the names in another order and case is the same.
	d2 := d
	d2.To = "CAROL,bob"
	second, err := board.SendMessage(ctx, pool, a, d2, t0.Add(30*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if second != first || count() != 1 {
		t.Errorf("second = %d, first = %d, rows = %d, want one row", second, first, count())
	}
	var rcpts int
	if err := pool.QueryRow(ctx, `select count(*) from pm_recipients where pm_id = $1`, first).Scan(&rcpts); err != nil || rcpts != 2 {
		t.Errorf("recipient rows = %d, %v, want 2", rcpts, err)
	}
	// Each field breaks the tie: recipients, subject, text.
	for name, mod := range map[string]func(*board.MessageDraft){
		"recipients": func(x *board.MessageDraft) { x.To = "bob" },
		"subject":    func(x *board.MessageDraft) { x.Subject = "Same!" },
		"text":       func(x *board.MessageDraft) { x.Source = "same text." },
	} {
		x := d
		mod(&x)
		id, err := board.SendMessage(ctx, pool, a, x, t0.Add(31*time.Second))
		if err != nil || id == first {
			t.Errorf("a different %s: id %d, %v, want a new message", name, id, err)
		}
	}
	// After 60 seconds it is a new message.
	third, err := board.SendMessage(ctx, pool, a, d, t0.Add(61*time.Second))
	if err != nil || third == first {
		t.Errorf("after 61 s: id %d, %v, want a new message", third, err)
	}
	// Another author's equal message is a message of its own.
	grantOpts(t, pool, carol, 0, "pm.send_many")
	other, err := board.SendMessage(ctx, pool, viewerFor(t, pool, carol), board.MessageDraft{To: "alice, bob", Subject: "Same", Source: "same text", IP: ip1}, t0)
	if err != nil || other == first {
		t.Errorf("another author: id %d, %v", other, err)
	}
	// A message that the author deleted does not count: the author sends it again.
	grantOpts(t, pool, alice, 0, "pm.delete")
	a = viewerFor(t, pool, alice)
	if err := board.DeleteMessage(ctx, pool, a, third); err != nil {
		t.Fatal(err)
	}
	again, err := board.SendMessage(ctx, pool, a, d, t0.Add(62*time.Second))
	if err != nil || again == third {
		t.Errorf("after deleting: id %d, %v, want a new message", again, err)
	}
}

func TestDuplicateMessageRace(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	a := viewerFor(t, pool, alice)
	const n = 8
	ids := make(chan int64, n)
	errs := make(chan error, n)
	for range n {
		go func() {
			id, err := board.SendMessage(ctx, pool, a, board.MessageDraft{To: "bob", Subject: "Race", Source: "once", IP: ip1}, t0)
			ids <- id
			errs <- err
		}()
	}
	seen := map[int64]bool{}
	for range n {
		if err := <-errs; err != nil {
			t.Error(err)
		}
		seen[<-ids] = true
	}
	var rows int
	if err := pool.QueryRow(ctx, `select count(*) from pms where subject = 'Race'`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || len(seen) != 1 {
		t.Errorf("rows = %d, distinct IDs = %d, want 1 and 1", rows, len(seen))
	}
}

func TestReplyDraft(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	b := viewerFor(t, pool, bob)

	d, err := board.ReplyDraft(ctx, pool, b, 1, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.To != "alice" || d.Subject != "Re: Welcome aboard" || d.Source != "" {
		t.Errorf("draft = %+v", d)
	}
	q, err := board.ReplyDraft(ctx, pool, b, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q.Source, "> **alice** wrote:\n") || !strings.Contains(q.Source, "\n> Glad you joined, bob.\n") {
		t.Errorf("quote = %q", q.Source)
	}
	// A reply to a reply keeps one "Re: ", whatever its case.
	for _, subject := range []string{"Re: Welcome aboard", "RE: Welcome aboard", "re: Welcome aboard"} {
		if _, err := pool.Exec(ctx, `update pms set subject = $1 where id = 2`, subject); err != nil {
			t.Fatal(err)
		}
		d, err := board.ReplyDraft(ctx, pool, viewerFor(t, pool, alice), 2, false)
		if err != nil {
			t.Fatal(err)
		}
		if d.Subject != subject || d.To != "bob" {
			t.Errorf("reply to %q: draft = %+v", subject, d)
		}
	}
	// A long subject is cut to the limit, so the draft can be sent.
	if _, err := pool.Exec(ctx, `update pms set subject = $1 where id = 1`, strings.Repeat("s", 120)); err != nil {
		t.Fatal(err)
	}
	d, err = board.ReplyDraft(ctx, pool, b, 1, false)
	if err != nil || len([]rune(d.Subject)) != 120 || !strings.HasPrefix(d.Subject, "Re: ss") {
		t.Errorf("long subject: %d runes, %v", len([]rune(d.Subject)), err)
	}
	// Blank lines in the quote keep the quote going. A reply needs pm.send.
	if _, err := pool.Exec(ctx, `update pms set body_source = E'one\n\ntwo\r\nthree' where id = 1`); err != nil {
		t.Fatal(err)
	}
	q, err = board.ReplyDraft(ctx, pool, b, 1, true)
	if err != nil || !strings.Contains(q.Source, ">\n> one\n>\n> two\n> three\n") {
		t.Errorf("quote = %q, %v", q.Source, err)
	}
	denyOpts(t, pool, bob, 0, "pm.send")
	if _, err := board.ReplyDraft(ctx, pool, viewerFor(t, pool, bob), 1, false); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("without pm.send: %v, want ErrForbidden", err)
	}
}

// A draft that ReplyDraft makes can be sent as it is.
func TestReplyDraftSends(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	b := viewerFor(t, pool, bob)
	d, err := board.ReplyDraft(ctx, pool, b, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	d.Source += "Thanks!"
	d.IP = ip2
	id, err := board.SendMessage(ctx, pool, b, d, t0)
	if err != nil {
		t.Fatal(err)
	}
	m, err := openMessage(ctx, pool, viewerFor(t, pool, alice), id)
	if err != nil {
		t.Fatal(err)
	}
	if m.Subject != "Re: Welcome aboard" || m.From.ID != bob || !strings.Contains(string(markup.Render(m.Tree, markup.RenderContext{}).HTML), "<blockquote>") {
		t.Errorf("reply = %+v", m)
	}
}

func TestSendRefusesBannedMember(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	if _, err := pool.Exec(ctx, `insert into bans (kind, user_id, reason, created_at) values ('user', $1, 'x', $2)`, carol, t0); err != nil {
		t.Fatal(err)
	}
	_, err := board.SendMessage(ctx, pool, viewerFor(t, pool, carol), board.MessageDraft{To: "bob", Subject: "S", Source: "x", IP: ip1}, t0)
	wantIs(t, err, board.ErrBanned, "banned member")
}

// The name of the author goes into the quote as text. A name with link syntax or
// emphasis marks must not make a link or an emphasis in the reply.
func TestQuotedNamesAreEscaped(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	name := "[a](https://evil.example) *b* _c_"
	if _, err := pool.Exec(ctx, `update pms set author_name = $1, author_id = null where id = 2`, name); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	d, err := board.ReplyDraft(ctx, pool, a, 2, true)
	if err != nil {
		t.Fatal(err)
	}
	want := `> **\[a\]\(https\:\/\/evil\.example\) \*b\* \_c\_** wrote:` + "\n"
	if !strings.HasPrefix(d.Source, want) {
		t.Errorf("quote = %q, want prefix %q", d.Source, want)
	}
	b, err := board.ParseMessage(ctx, pool, a, d.Source)
	if err != nil {
		t.Fatal(err)
	}
	out := string(markup.Render(b.Tree, markup.RenderContext{}).HTML)
	if strings.Contains(out, "<a ") || strings.Contains(out, "evil.example\"") || strings.Contains(out, "<em>") {
		t.Errorf("the name made markup: %s", out)
	}
	// The same helper quotes a post.
	if _, err := pool.Exec(ctx, `update users set username = $1 where id = $2`, name, bob); err != nil {
		t.Fatal(err)
	}
	q, err := board.QuoteSource(ctx, pool, a, 2) // post 2 is by bob
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(q, `> **\[a\]\(https\:\/\/evil\.example\) \*b\* \_c\_** [wrote]`) {
		t.Errorf("post quote = %q", q)
	}
}

// A name with a comma is one name when the whole field names a member.
func TestCommaInMemberName(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	if _, err := pool.Exec(ctx, `update users set username = 'Smith, Alice', username_clean = 'smith, alice' where id = $1`, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update pms set author_name = 'Smith, Alice' where id = 2`); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	// The reply draft names the member, and the draft sends.
	d, err := board.ReplyDraft(ctx, pool, a, 2, false)
	if err != nil {
		t.Fatal(err)
	}
	if d.To != "Smith, Alice" {
		t.Fatalf("To = %q", d.To)
	}
	d.Source, d.IP = "Hello", ip1
	id, err := board.SendMessage(ctx, pool, a, d, t0)
	if err != nil {
		t.Fatalf("send to a name with a comma: %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from pm_recipients where pm_id = $1 and user_id = $2`, id, bob).Scan(&n); err != nil || n != 1 {
		t.Errorf("recipient rows = %d, %v, want 1", n, err)
	}
	// It needs no pm.send_many: it is one member. Case and spaces do not matter.
	c := viewerFor(t, pool, carol)
	if _, err := board.SendMessage(ctx, pool, c, board.MessageDraft{To: "  SMITH, alice ", Subject: "S", Source: "x", IP: ip1}, t0); err != nil {
		t.Errorf("one member with a comma, no pm.send_many: %v", err)
	}
	// A list that is not one name still splits at the commas. "Smith, Alice, carol"
	// is no member's name as a whole, so it reads as three names, and the first
	// two are unknown.
	grantOpts(t, pool, alice, 0, "pm.send_many")
	_, err = board.SendMessage(ctx, pool, viewerFor(t, pool, alice), board.MessageDraft{To: "Smith, Alice, carol", Subject: "S", Source: "x", IP: ip1}, t0)
	wantInput(t, err, "to", "No member named “Smith”.")
}

// A reply to a message that the viewer wrote goes to its "to" recipients.
func TestReplyToOwnMessage(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	if _, err := pool.Exec(ctx, `insert into pm_recipients (pm_id, user_id, kind) values (1, $1, 'to')`, dave); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	d, err := board.ReplyDraft(ctx, pool, a, 1, true)
	if err != nil {
		t.Fatal(err)
	}
	// Bob and dave, in the order that the message shows. Carol is a blind copy.
	if d.To != "bob, dave" {
		t.Errorf("To = %q, want %q", d.To, "bob, dave")
	}
	if d.Subject != "Re: Welcome aboard" || !strings.HasPrefix(d.Source, "> **alice** wrote:\n") {
		t.Errorf("draft = %+v", d)
	}
	// Bob replies to alice, not to the other recipient.
	d, err = board.ReplyDraft(ctx, pool, viewerFor(t, pool, bob), 1, false)
	if err != nil || d.To != "alice" {
		t.Errorf("bob's draft = %+v, %v", d, err)
	}
}

func TestMessageListPages(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	// 29 more messages to bob: 30 in his inbox, ids 1 and 100 to 128.
	for i := range 29 {
		id := 100 + i
		if _, err := pool.Exec(ctx, `insert into pms (id, author_id, author_name, created_at, subject, body_tree, conversation_id)
			values ($1, $2, 'alice', $3, 'Bulk', '{}'::jsonb, $1)`, id, alice, t0); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `insert into pm_recipients (pm_id, user_id, kind) values ($1, $2, 'to')`, id, bob); err != nil {
			t.Fatal(err)
		}
	}
	b := viewerFor(t, pool, bob)
	p1, err := board.Inbox(ctx, pool, b, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(p1.Rows) != 25 || p1.Pages != 2 || p1.Rows[0].ID != 128 || p1.Rows[24].ID != 104 {
		t.Errorf("page 1: %d rows, %d pages, first %d, last %d", len(p1.Rows), p1.Pages, p1.Rows[0].ID, p1.Rows[len(p1.Rows)-1].ID)
	}
	p2, err := board.Inbox(ctx, pool, b, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDs(p2); !slices.Equal(got, []int64{103, 102, 101, 100, 1}) || p2.Page != 2 {
		t.Errorf("page 2 = %v, page %d", got, p2.Page)
	}
	if got := authorIDs(p2.Rows[0].To); !slices.Equal(got, []int64{bob}) {
		t.Errorf("page 2 To = %v", got)
	}
	for _, page := range []int{-1, 3} {
		if _, err := board.Inbox(ctx, pool, b, page); !errors.Is(err, board.ErrNotFound) {
			t.Errorf("page %d = %v, want ErrNotFound", page, err)
		}
	}
	// The sent list pages the same way.
	s2, err := board.Sent(ctx, pool, viewerFor(t, pool, alice), 2)
	if err != nil || !slices.Equal(messageIDs(s2), []int64{103, 102, 101, 100, 1}) {
		t.Errorf("sent page 2 = %v, %v", messageIDs(s2), err)
	}
	if _, err := board.Sent(ctx, pool, viewerFor(t, pool, alice), -1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("sent page -1 = %v, want ErrNotFound", err)
	}
}

// ParseMessage checks the right to send, because a preview calls it alone.
func TestParseMessageNeedsPMSend(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	denyOpts(t, pool, carol, 0, "pm.send")
	if _, err := board.ParseMessage(ctx, pool, viewerFor(t, pool, carol), "x"); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("without pm.send: %v, want ErrForbidden", err)
	}
	if _, err := board.ParseMessage(ctx, pool, asGuest(t, pool), "x"); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest: %v, want ErrForbidden", err)
	}
}

// A name of 255 bytes is a possible name, so the To field takes it.
func TestLongestMemberName(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	long := strings.Repeat("x", 255)
	if _, err := pool.Exec(ctx, `update users set username = $1, username_clean = $1 where id = $2`, long, bob); err != nil {
		t.Fatal(err)
	}
	if _, err := board.SendMessage(ctx, pool, viewerFor(t, pool, alice), board.MessageDraft{To: long, Subject: "S", Source: "x", IP: ip1}, t0); err != nil {
		t.Errorf("255-byte name: %v", err)
	}
	_, err := board.SendMessage(ctx, pool, viewerFor(t, pool, alice), board.MessageDraft{To: long + "y", Subject: "S", Source: "x", IP: ip1}, t0)
	wantInput(t, err, "to", "")
}

func TestPeekMessageWritesNothing(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	b := viewerFor(t, pool, bob)
	// bob's copy of message 1 is unread after loadMessages.
	m, err := board.PeekMessage(ctx, pool, b, 1)
	if err != nil || m.Subject != "Welcome aboard" {
		t.Fatalf("PeekMessage = %q, %v", m.Subject, err)
	}
	if n, _ := board.UnreadMessages(ctx, pool, b); n != 1 {
		t.Errorf("unread after a peek = %d, want 1", n)
	}
	// A non-party gets ErrNotFound, and a member without pm.read ErrForbidden.
	_, err = board.PeekMessage(ctx, pool, viewerFor(t, pool, carol), 2)
	wantIs(t, err, board.ErrNotFound, "carol peeks at message 2")
	if err := board.MarkMessageRead(ctx, pool, viewerFor(t, pool, carol), 2); err != nil {
		t.Errorf("MarkMessageRead by a non-party: %v", err)
	}
	if n, _ := board.UnreadMessages(ctx, pool, viewerFor(t, pool, carol)); n != 1 {
		t.Errorf("carol unread = %d, want 1 (message 1, blind copy)", n)
	}
	if err := board.MarkMessageRead(ctx, pool, b, 1); err != nil {
		t.Fatal(err)
	}
	if n, _ := board.UnreadMessages(ctx, pool, b); n != 0 {
		t.Errorf("unread after the mark = %d, want 0", n)
	}
	denyOpts(t, pool, bob, 0, "pm.read")
	wantIs(t, board.MarkMessageRead(ctx, pool, viewerFor(t, pool, bob), 1), board.ErrForbidden, "mark without pm.read")
}

// A viewer who is not a party locks nothing: the party rule is in the query, so a
// message that another transaction holds with select ... for update does not make
// the delete of a stranger wait. A party does wait, which shows that the test would
// see a lock.
func TestDeleteByANonPartyLocksNothing(t *testing.T) {
	pool := loadMessages(t)
	grantOpts(t, pool, carol, 0, "pm.delete")
	grantOpts(t, pool, alice, 0, "pm.delete")
	var party int
	if err := pool.QueryRow(context.Background(), `select count(*) from pms p
		where p.id = 2 and (p.author_id = $1 or exists (select 1 from pm_recipients r where r.pm_id = 2 and r.user_id = $1))`, carol).Scan(&party); err != nil || party != 0 {
		t.Fatalf("carol is a party to message 2 (%d, %v): the test needs a stranger", party, err)
	}

	holder, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(context.Background()) }()
	if _, err := holder.Exec(context.Background(), `select id from pms where id = 2 for update`); err != nil {
		t.Fatal(err)
	}

	// Build the viewer before the timer starts: viewerFor reads the database.
	stranger := viewerFor(t, pool, carol)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	err = board.DeleteMessage(ctx, pool, stranger, 2)
	if !errors.Is(err, board.ErrNotFound) {
		t.Errorf("delete by a non-party = %v, want ErrNotFound", err)
	}
	if took := time.Since(start); took > 500*time.Millisecond {
		t.Errorf("delete by a non-party took %v: it waited for the lock", took)
	}

	// A party waits for the lock until the deadline.
	ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	if err := board.DeleteMessage(ctx, pool, viewerFor(t, pool, alice), 2); err == nil || errors.Is(err, board.ErrNotFound) {
		t.Errorf("delete by a party under a lock = %v, want a deadline error", err)
	}
}

// Ruling R46: a message subject refuses the hidden formatting characters of
// ruling R40, with a message that says why.
func TestSendSubjectRefusesHiddenFormat(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	a := viewerFor(t, pool, alice)
	for _, subject := range []string{"a\u202eb", "a\u2066b"} {
		_, err := board.SendMessage(ctx, pool, a, board.MessageDraft{To: "bob", Subject: subject, Source: "x", IP: ip1}, t0)
		wantInput(t, err, "subject", "Remove the hidden formatting character.")
	}
}

// Ruling R52: a message subject refuses the line and paragraph separators.
func TestSendSubjectRefusesLineSeparators(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	a := viewerFor(t, pool, alice)
	for _, subject := range []string{"a\u2028b", "a\u2029b"} {
		_, err := board.SendMessage(ctx, pool, a, board.MessageDraft{To: "bob", Subject: subject, Source: "x", IP: ip1}, t0)
		wantInput(t, err, "subject", "The subject may not hold control characters or line breaks.")
	}
}
