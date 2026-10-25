package board_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// TestRenameRemovesTheOldTitleEverywhere covers B-I1. A moderator may rename a
// topic to remove text from its title. The old title must then leave the replies
// that copied it ("Re: " and the title), the moved-topic markers, and search.
// A reply whose subject is its writer's own stays as it is.
func TestRenameRemovesTheOldTitleEverywhere(t *testing.T) {
	ctx := context.Background()
	b := searchBoard()
	pool, _ := boardtest.Load(t, b)
	a := viewerFor(t, pool, alice)
	m := moderator(t, pool, carol)

	// The first edit puts a word in the title; a reply copies it; an imported reply
	// has "Re: <that title>" as its subject; post 2 and 3 keep a subject of their own.
	if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Call me at zebraphone 5550100", Source: "Welcome to the **example** board."}, "", t0); err != nil {
		t.Fatal(err)
	}
	rid, err := board.Reply(ctx, pool, viewerFor(t, pool, bob), 1, board.Draft{Source: "Got it.", IP: ip1}, t0.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update posts set subject = 'Re: Call me at zebraphone 5550100' where id = 4`); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, rid).Subject; got != "Re: Call me at zebraphone 5550100" {
		t.Fatalf("the reply has the subject %q", got)
	}
	g := guestFor(t, pool)
	memberSearch := memberFor(t, pool, carol)
	if got := hitIDs(search(t, pool, memberSearch, board.SearchQuery{Text: "zebraphone"})); len(got) == 0 {
		t.Fatal("search finds nothing for the title word before the rename: the test proves nothing")
	}

	// A moderator removes the number from the title.
	if err := board.EditPost(ctx, pool, m, 1, board.Draft{Title: "Contact details removed", Source: "Welcome to the **example** board."}, "personal data", t0.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}

	if got := hitIDs(search(t, pool, memberSearch, board.SearchQuery{Text: "zebraphone"})); len(got) != 0 {
		t.Errorf("search for the old word finds posts %v after the rename", got)
	}
	if got := hitIDs(search(t, pool, memberSearch, board.SearchQuery{Text: "5550100"})); len(got) != 0 {
		t.Errorf("search for the old number finds posts %v after the rename", got)
	}
	page, err := board.TopicView(ctx, pool, g, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(page.Topic.Title, "zebraphone") {
		t.Errorf("topic title = %q", page.Topic.Title)
	}
	for _, p := range page.Posts {
		if strings.Contains(p.Subject, "zebraphone") || strings.Contains(p.Subject, "5550100") {
			t.Errorf("a guest sees the subject %q of post %d", p.Subject, p.ID)
		}
	}
	for id, want := range map[int64]string{
		1:   "Contact details removed",
		rid: "Re: Contact details removed", // a reply written on the board
		4:   "Re: Contact details removed", // an imported reply with the same subject
		2:   "Re: Welcome",                 // the writer's own subject
		3:   "Re: Welcome",                 // a held reply, also changed when it matches; here it does not
	} {
		if got := readPost(t, pool, id).Subject; got != want {
			t.Errorf("post %d subject = %q, want %q", id, got, want)
		}
	}
	if got := readTopic(t, pool, 3).Title; got != "Contact details removed" {
		t.Errorf("the marker of topic 1 has the title %q", got)
	}
	// A rename never touches another topic.
	if got := readTopic(t, pool, 2).Title; got != "Favorite color?" {
		t.Errorf("topic 2 title = %q", got)
	}
	if got := readPost(t, pool, 6).Subject; got != "Re: Favorite color?" {
		t.Errorf("post 6 subject = %q", got)
	}
	var stale int
	if err := pool.QueryRow(ctx, `select count(*) from posts where search::text like '%zebraphone%'`).Scan(&stale); err != nil {
		t.Fatal(err)
	}
	if stale != 0 {
		t.Errorf("%d posts still hold the old word in the search column", stale)
	}
}

// TestRenameToTheSameTitleChangesNoReply: an edit that keeps the title leaves the
// replies alone, also a reply whose subject is "Re: " and that title.
func TestRenameToTheSameTitleChangesNoReply(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(ctx, `update posts set subject = 'Re: Welcome to the example board' where id = 2`); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Welcome to the example board", Source: "same title"}, "", t0); err != nil {
		t.Fatal(err)
	}
	if got := readPost(t, pool, 2).Subject; got != "Re: Welcome to the example board" {
		t.Errorf("post 2 subject = %q", got)
	}
}

// TestModeratorLockOverAnAuthorLock covers B-M1: a holder of mod.lock who locks a
// topic that its author locked writes a topic.lock row with the moderator as
// actor, so the author's lock_own can no longer undo it. The topic stays locked.
func TestModeratorLockOverAnAuthorLock(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "topic.lock_own")
	grantOpts(t, pool, carol, 2, "mod.lock")
	a, m := viewerFor(t, pool, alice), viewerFor(t, pool, carol)

	if err := board.SetTopicLock(ctx, pool, a, 1, true, t0); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, m, 1, true, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if !readTopic(t, pool, 1).Locked {
		t.Error("the topic is not locked")
	}
	log := readLog(t, pool)
	if len(log) != 2 || log[1].Action != "topic.lock" || log[1].Actor != carol || log[1].Target != 1 {
		t.Fatalf("log = %+v, want the author's lock and then the moderator's", log)
	}
	wantIs(t, board.SetTopicLock(ctx, pool, a, 1, false, t0.Add(2*time.Minute)), board.ErrForbidden, "the author's unlock of the moderator's lock")
	if !readTopic(t, pool, 1).Locked {
		t.Error("the author undid the moderator's lock")
	}
	// A moderator whose lock is the latest one has nothing to add.
	if err := board.SetTopicLock(ctx, pool, m, 1, true, t0.Add(3*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(readLog(t, pool)); n != 2 {
		t.Errorf("%d log rows after the moderator locked twice, want 2", n)
	}
	// A lock by a command or an import has no actor: a moderator's lock still goes on record.
	if _, err := pool.Exec(ctx, `delete from mod_log`); err != nil {
		t.Fatal(err)
	}
	if err := board.SetTopicLock(ctx, pool, m, 1, true, t0.Add(4*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if log := readLog(t, pool); len(log) != 1 || log[0].Actor != carol {
		t.Errorf("log = %+v, want one row by the moderator", log)
	}
	// The author cannot do it: a locked topic stays as it is and writes nothing.
	if err := board.SetTopicLock(ctx, pool, a, 1, true, t0.Add(5*time.Minute)); err != nil {
		t.Fatal(err)
	}
	if n := len(readLog(t, pool)); n != 1 {
		t.Errorf("%d log rows after the author locked a locked topic, want 1", n)
	}
}
