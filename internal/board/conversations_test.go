package board_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
)

func conversationOf(t *testing.T, pool *pgxpool.Pool, id int64) int64 {
	t.Helper()
	var cid int64
	if err := pool.QueryRow(context.Background(), `select conversation_id from pms where id = $1`, id).Scan(&cid); err != nil {
		t.Fatal(err)
	}
	return cid
}

func messageIDsOf(c board.Conversation) []int64 {
	out := []int64{}
	for _, m := range c.Messages {
		out = append(out, m.ID)
	}
	return out
}

func TestThreadMessages(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	// Message 2 answers message 1 ("Re: Welcome aboard", between the same two
	// people). Each starts its own conversation after the import.
	if c := conversationOf(t, pool, 2); c != 2 {
		t.Fatalf("message 2 is in conversation %d before the thread, want 2", c)
	}
	n, err := board.ThreadMessages(ctx, pool, true)
	if err != nil || n != 1 {
		t.Fatalf("dry run = %d, %v; want 1", n, err)
	}
	if c := conversationOf(t, pool, 2); c != 2 {
		t.Fatalf("the dry run joined message 2 to %d", c)
	}
	if n, err = board.ThreadMessages(ctx, pool, false); err != nil || n != 1 {
		t.Fatalf("thread = %d, %v; want 1", n, err)
	}
	if c := conversationOf(t, pool, 2); c != 1 {
		t.Fatalf("message 2 is in conversation %d, want 1", c)
	}
	if n, err = board.ThreadMessages(ctx, pool, false); err != nil || n != 0 {
		t.Fatalf("second thread = %d, %v; want 0", n, err)
	}
}

func TestThreadMessagesNeedsSamePeopleAndReply(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	// 60: carol to bob, "Re: Welcome aboard": other people, so it stays alone.
	// 61: bob to alice, "welcome  ABOARD" with no "Re:": a new talk, alone.
	// 62: alice to bob, "RE: re: Welcome aboard": joins 61, the newest match.
	for _, m := range []struct {
		id, author, to int64
		subject        string
	}{
		{60, carol, bob, "Re: Welcome aboard"},
		{61, bob, alice, "Welcome ABOARD"},
		{62, alice, bob, "RE: re: Welcome aboard"},
	} {
		if _, err := pool.Exec(ctx, `insert into pms (id, author_id, author_name, created_at, subject, body_tree, conversation_id)
			values ($1, $2, 'x', $3, $4, '{}'::jsonb, $1)`, m.id, m.author, t0, m.subject); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `insert into pm_recipients (pm_id, user_id, kind) values ($1, $2, 'to')`, m.id, m.to); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := board.ThreadMessages(ctx, pool, false); err != nil || n != 2 {
		t.Fatalf("thread = %d, %v; want 2", n, err)
	}
	for id, want := range map[int64]int64{1: 1, 2: 1, 60: 60, 61: 61, 62: 61} {
		if c := conversationOf(t, pool, id); c != want {
			t.Errorf("message %d is in conversation %d, want %d", id, c, want)
		}
	}
}

func TestConversations(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	if _, err := board.ThreadMessages(ctx, pool, false); err != nil {
		t.Fatal(err)
	}
	list := func(id int64) board.ConversationRow {
		t.Helper()
		l, err := board.Conversations(ctx, pool, viewerFor(t, pool, id), 1)
		if err != nil {
			t.Fatal(err)
		}
		if len(l.Rows) != 1 {
			t.Fatalf("user %d has %d conversations, want 1", id, len(l.Rows))
		}
		return l.Rows[0]
	}
	// alice wrote 1 and received 2: both sides, one row.
	a := list(alice)
	if a.ID != 1 || a.Messages != 2 || a.LastID != 2 || a.LastFrom.ID != bob || a.Subject != "Welcome aboard" {
		t.Errorf("alice row = %+v", a)
	}
	if got := authorIDs(a.With); !slices.Equal(got, []int64{bob}) {
		t.Errorf("alice talks with %v, want [bob]", got)
	}
	// bob deleted his copy of 2, so he sees 1 only, unread.
	b := list(bob)
	if b.Messages != 1 || b.Unread != 1 || b.LastID != 1 {
		t.Errorf("bob row = %+v", b)
	}
	// carol got a blind copy of 1. She sees the author and the "to" recipient.
	c := list(carol)
	if c.Messages != 1 {
		t.Errorf("carol row = %+v", c)
	}
	if got := authorIDs(c.With); !slices.Equal(got, []int64{alice, bob}) {
		t.Errorf("carol talks with %v, want [alice bob]", got)
	}
	// A member with no messages has one empty page; page 2 does not exist.
	l, err := board.Conversations(ctx, pool, viewerFor(t, pool, dave), 1)
	if err != nil || len(l.Rows) != 0 {
		t.Errorf("dave = %+v, %v", l, err)
	}
	if _, err := board.Conversations(ctx, pool, viewerFor(t, pool, alice), 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2 = %v, want ErrNotFound", err)
	}
	if _, err := board.Conversations(ctx, pool, board.Viewer{}, 1); !errors.Is(err, board.ErrForbidden) {
		t.Errorf("guest = %v, want ErrForbidden", err)
	}
}

func TestReadConversation(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	if _, err := board.ThreadMessages(ctx, pool, false); err != nil {
		t.Fatal(err)
	}
	read := func(user, id int64) (board.Conversation, error) {
		return board.ReadConversation(ctx, pool, viewerFor(t, pool, user), id, 0)
	}
	// From either message, alice gets both, oldest first.
	for _, id := range []int64{1, 2} {
		c, err := read(alice, id)
		if err != nil {
			t.Fatal(err)
		}
		if got := messageIDsOf(c); !slices.Equal(got, []int64{1, 2}) || c.ID != 1 || c.LastID != 2 || c.Subject != "Welcome aboard" {
			t.Errorf("alice from %d = %v %+v", id, got, c)
		}
		if len(c.Unread) != 0 {
			t.Errorf("alice unread = %v", c.Unread)
		}
	}
	// bob deleted 2: he cannot open it, and from 1 he sees only 1, unread.
	if _, err := read(bob, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("bob opens 2: %v, want ErrNotFound", err)
	}
	c, err := read(bob, 1)
	if err != nil {
		t.Fatal(err)
	}
	if got := messageIDsOf(c); !slices.Equal(got, []int64{1}) || !slices.Equal(c.Unread, []int64{1}) {
		t.Errorf("bob = %v unread %v", got, c.Unread)
	}
	// carol, a blind copy of 1, sees 1 and not bob's reply to alice.
	if c, err = read(carol, 1); err != nil || !slices.Equal(messageIDsOf(c), []int64{1}) {
		t.Errorf("carol = %v, %v", messageIDsOf(c), err)
	}
	if _, err := read(dave, 1); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("dave: %v, want ErrNotFound", err)
	}
	if _, err := board.ReadConversation(ctx, pool, viewerFor(t, pool, alice), 1, 2); !errors.Is(err, board.ErrNotFound) {
		t.Errorf("page 2: %v, want ErrNotFound", err)
	}
	if err := board.MarkMessagesRead(ctx, pool, viewerFor(t, pool, bob), []int64{1, 2}); err != nil {
		t.Fatal(err)
	}
	if c, _ = read(bob, 1); len(c.Unread) != 0 {
		t.Errorf("bob unread after the mark = %v", c.Unread)
	}
}

func TestReplyJoinsConversation(t *testing.T) {
	ctx := context.Background()
	pool := loadMessages(t)
	b := viewerFor(t, pool, bob)
	send := func(replyTo int64, subject string) int64 {
		t.Helper()
		id, err := board.SendMessage(ctx, pool, b, board.MessageDraft{To: "alice", Subject: subject, Source: "x " + subject, IP: ip1, ReplyTo: replyTo}, t0)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	// A reply to message 1, which bob received, joins conversation 1.
	if id := send(1, "Re: Welcome aboard"); conversationOf(t, pool, id) != 1 {
		t.Errorf("reply to 1 is in conversation %d, want 1", conversationOf(t, pool, id))
	}
	// bob deleted his copy of 2, and message 999 does not exist: each reply starts
	// its own conversation.
	for _, to := range []int64{2, 999} {
		if id := send(to, "Re: other "+string(rune('a'+to%26))); conversationOf(t, pool, id) != id {
			t.Errorf("reply to %d is in conversation %d, want its own", to, conversationOf(t, pool, id))
		}
	}
	// A message that is not a reply starts its own.
	if id := send(0, "Hello"); conversationOf(t, pool, id) != id {
		t.Errorf("new message is in conversation %d, want its own", conversationOf(t, pool, id))
	}
	// Message 1 went to bob and carol (a blind copy). carol is not a party to bob's
	// reply, so she still sees one message.
	c, err := board.ReadConversation(ctx, pool, viewerFor(t, pool, carol), 1, 0)
	if err != nil || len(c.Messages) != 1 {
		t.Errorf("carol = %v, %v", messageIDsOf(c), err)
	}
}
