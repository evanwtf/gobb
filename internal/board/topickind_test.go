package board_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// topicKind reads the stored type of a topic.
func topicKind(t *testing.T, pool *pgxpool.Pool, id int64) string {
	t.Helper()
	var k string
	if err := pool.QueryRow(context.Background(), `select kind from topics where id = $1`, id).Scan(&k); err != nil {
		t.Fatalf("read the type of topic %d: %v", id, err)
	}
	return k
}

// topicOfPost returns the topic of post id.
func topicOfPost(t *testing.T, pool *pgxpool.Pool, id int64) int64 {
	t.Helper()
	var topic int64
	if err := pool.QueryRow(context.Background(), `select topic_id from posts where id = $1`, id).Scan(&topic); err != nil {
		t.Fatal(err)
	}
	return topic
}

// rowIDs lists the IDs of topic rows in order.
func rowIDs(rows []board.TopicRow) []int64 {
	var out []int64
	for _, r := range rows {
		out = append(out, r.ID)
	}
	return out
}

// wantKindLog checks that log row r is a topic.kind row by actor for topic
// target, from one type to another.
func wantKindLog(t *testing.T, r logRow, actor, target int64, from, to string) {
	t.Helper()
	var d map[string]string
	if err := json.Unmarshal([]byte(r.Details), &d); err != nil {
		t.Fatalf("details %q: %v", r.Details, err)
	}
	if r.Actor != actor || r.Action != "topic.kind" || r.TargetType != "topic" || r.Target != target ||
		len(d) != 2 || d["from"] != from || d["to"] != to || !r.At.Equal(t0) {
		t.Errorf("log row = %+v, want topic.kind by %d on topic %d from %s to %s at %v", r, actor, target, from, to, t0)
	}
}

func TestNewTopicKinds(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	three := []string{"normal", "sticky", "announce"}
	four := []string{"normal", "sticky", "announce", "global"}

	// carol is a plain member; bob moderates with mod.edit and mod.approve.
	if got := board.NewTopicKinds(viewerFor(t, pool, carol), 2); got != nil {
		t.Errorf("plain member: %v, want none", got)
	}
	grantOpts(t, pool, bob, 2, "mod.lock")
	if got := board.NewTopicKinds(viewerFor(t, pool, bob), 2); got != nil {
		t.Errorf("mod.lock without mod.pin: %v, want none", got)
	}
	// topic.sticky and topic.announce each add their own type, per forum.
	grantOpts(t, pool, carol, 2, "topic.sticky")
	if got, want := board.NewTopicKinds(viewerFor(t, pool, carol), 2), []string{"normal", "sticky"}; !slices.Equal(got, want) {
		t.Errorf("topic.sticky: %v, want %v", got, want)
	}
	grantOpts(t, pool, carol, 2, "topic.announce")
	if got := board.NewTopicKinds(viewerFor(t, pool, carol), 2); !slices.Equal(got, three) {
		t.Errorf("topic.sticky and topic.announce: %v, want %v", got, three)
	}
	if got := board.NewTopicKinds(viewerFor(t, pool, carol), 3); got != nil {
		t.Errorf("rights in forum 2, asked for forum 3: %v, want none", got)
	}
	// topic.global is reserved: it gives nothing.
	grantOpts(t, pool, carol, 3, "topic.global")
	if got := board.NewTopicKinds(viewerFor(t, pool, carol), 3); got != nil {
		t.Errorf("topic.global: %v, want none", got)
	}
	// mod.pin board-wide gives three types in every forum, never global.
	grantOpts(t, pool, bob, 0, "mod.pin")
	for _, f := range []int64{2, 3} {
		if got := board.NewTopicKinds(viewerFor(t, pool, bob), f); !slices.Equal(got, three) {
			t.Errorf("board-wide mod.pin, forum %d: %v, want %v", f, got, three)
		}
	}
	// An admin gets global, even though the example role sets topic.global
	// Never. Sticky and announce still need their rights.
	if got, want := board.NewTopicKinds(viewerFor(t, pool, alice), 2), []string{"normal", "global"}; !slices.Equal(got, want) {
		t.Errorf("admin: %v, want %v", got, want)
	}
	grantOpts(t, pool, alice, 2, "mod.pin")
	if got := board.NewTopicKinds(viewerFor(t, pool, alice), 2); !slices.Equal(got, four) {
		t.Errorf("admin with mod.pin: %v, want %v", got, four)
	}
	// A guest gets none, whatever the guests group holds.
	allowGuests(t, pool, 2, "mod.pin", "topic.sticky", "topic.announce")
	allowGuests(t, pool, 0, "mod.pin")
	if got := board.NewTopicKinds(asGuest(t, pool), 2); got != nil {
		t.Errorf("guest: %v, want none", got)
	}
	// The registry lists the four types in the order of the select.
	if !slices.Equal(board.TopicKinds, four) {
		t.Errorf("TopicKinds = %v, want %v", board.TopicKinds, four)
	}
}

func TestSetTopicKindRights(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Topic 1 is sticky, in forum 2. Topic 2 is in forum 3, which carol cannot
	// read. Topic 3 is a moved-topic marker in forum 3.
	if k := topicKind(t, pool, 1); k != "sticky" {
		t.Fatalf("fixture: topic 1 is %q, want sticky", k)
	}
	snapshot := counterSnapshot(t, pool)

	// Refused, with nothing written.
	wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, carol), 1, "normal", t0), board.ErrForbidden, "plain member")
	grantOpts(t, pool, carol, 3, "mod.pin")
	wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, carol), 1, "normal", t0), board.ErrForbidden, "mod.pin in another forum")
	grantOpts(t, pool, bob, 2, "mod.lock", "mod.edit", "mod.delete", "mod.approve")
	wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, bob), 1, "normal", t0), board.ErrForbidden, "every other moderator right")
	allowGuests(t, pool, 2, "mod.pin")
	allowGuests(t, pool, 0, "mod.pin")
	wantIs(t, board.SetTopicKind(ctx, pool, asGuest(t, pool), 1, "normal", t0), board.ErrForbidden, "guest")
	wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, alice), 9999, "normal", t0), board.ErrNotFound, "missing topic")
	grantOpts(t, pool, carol, 0, "mod.pin")
	wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, carol), 2, "normal", t0), board.ErrNotFound, "unreadable forum")
	// A bad kind: a viewer who may change the type gets ErrBadKind, and one who
	// may not gets the refusal first.
	for _, bad := range []string{"", "pinned", "Normal", "global "} {
		wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, alice), 1, bad, t0), board.ErrBadKind, "kind "+bad)
		wantIs(t, board.SetTopicKind(ctx, pool, asGuest(t, pool), 1, bad, t0), board.ErrForbidden, "guest, kind "+bad)
		wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, alice), 9999, bad, t0), board.ErrNotFound, "missing topic, kind "+bad)
	}
	if k := topicKind(t, pool, 1); k != "sticky" {
		t.Fatalf("a refused change set topic 1 to %q", k)
	}
	if n := len(readLog(t, pool)); n != 0 {
		t.Fatalf("%d log rows after refused changes, want 0", n)
	}

	// A forum moderator with mod.pin sets normal, sticky and announce, and not global.
	m := viewerFor(t, pool, carol) // mod.pin board-wide and in forum 3
	for _, k := range []string{"normal", "announce", "sticky"} {
		if err := board.SetTopicKind(ctx, pool, m, 1, k, t0); err != nil {
			t.Fatalf("mod.pin, %s: %v", k, err)
		}
		if got := topicKind(t, pool, 1); got != k {
			t.Errorf("after setting %s, topic 1 is %q", k, got)
		}
	}
	wantIs(t, board.SetTopicKind(ctx, pool, m, 1, "global", t0), board.ErrForbidden, "moderator sets global")
	if got := topicKind(t, pool, 1); got != "sticky" {
		t.Errorf("a refused global left topic 1 %q, want sticky", got)
	}

	// An admin without mod.pin may not make the other changes.
	a := viewerFor(t, pool, alice)
	wantIs(t, board.SetTopicKind(ctx, pool, a, 1, "normal", t0), board.ErrForbidden, "admin without mod.pin")

	// An admin sets global. The moderator may not undo it: global is the admin's.
	if err := board.SetTopicKind(ctx, pool, a, 1, "global", t0); err != nil {
		t.Fatalf("admin sets global: %v", err)
	}
	if got := topicKind(t, pool, 1); got != "global" {
		t.Errorf("topic 1 is %q, want global", got)
	}
	wantIs(t, board.SetTopicKind(ctx, pool, m, 1, "normal", t0), board.ErrForbidden, "moderator undoes global")
	if err := board.SetTopicKind(ctx, pool, a, 1, "normal", t0); err != nil {
		t.Fatalf("admin undoes global: %v", err)
	}
	if got := topicKind(t, pool, 1); got != "normal" {
		t.Errorf("topic 1 is %q, want normal", got)
	}

	// The same type again writes nothing.
	n := len(readLog(t, pool))
	if err := board.SetTopicKind(ctx, pool, a, 1, "normal", t0); err != nil {
		t.Fatal(err)
	}
	if got := len(readLog(t, pool)); got != n {
		t.Errorf("a change to the same type wrote %d log rows", got-n)
	}

	// One log row for each change, in order, with the old and the new type.
	log := readLog(t, pool)
	want := []struct {
		actor    int64
		from, to string
	}{
		{carol, "sticky", "normal"}, {carol, "normal", "announce"}, {carol, "announce", "sticky"},
		{alice, "sticky", "global"}, {alice, "global", "normal"},
	}
	if len(log) != len(want) {
		t.Fatalf("log holds %d rows, want %d: %+v", len(log), len(want), log)
	}
	for i, w := range want {
		wantKindLog(t, log[i], w.actor, 1, w.from, w.to)
	}

	// The type is not a counter: the counters are as they were, and exact.
	if got := counterSnapshot(t, pool); got != snapshot {
		t.Errorf("a change of type changed the counters\n%s\n%s", snapshot, got)
	}
	wantCountersExact(t, pool, "changes of type")
}

// TestSetTopicKindMovesLists checks the lists of the forum page after each change:
// the regular list (sticky first), the announcements of the forum, and the
// global topics of every forum.
func TestSetTopicKindMovesLists(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// alice is the admin, with mod.pin in forum 2 for the changes that are not global.
	grantOpts(t, pool, alice, 2, "mod.pin")
	a := viewerFor(t, pool, alice)
	// Two new topics in forum 2, newer than topic 1. Topic 1 is sticky.
	var newer []int64
	for i, title := range []string{"Newer one", "Newer two"} {
		post, err := board.NewTopic(ctx, pool, a, 2, board.Draft{Title: title, Source: "Text " + title, IP: ip1}, t0.Add(time.Duration(i)*time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		newer = append(newer, topicOfPost(t, pool, post))
	}
	n1, n2 := newer[0], newer[1]
	before2, before3 := readForum(t, pool, 2), readForum(t, pool, 3)

	check := func(step string, regular2, pinned2, pinned3 []int64) {
		t.Helper()
		f2, err := board.ForumView(ctx, pool, a, 2, 1)
		if err != nil {
			t.Fatal(err)
		}
		f3, err := board.ForumView(ctx, pool, a, 3, 1)
		if err != nil {
			t.Fatal(err)
		}
		if got := rowIDs(f2.Topics); !slices.Equal(got, regular2) {
			t.Errorf("%s: forum 2 lists %v, want %v", step, got, regular2)
		}
		if got := rowIDs(f2.Pinned); !slices.Equal(got, pinned2) {
			t.Errorf("%s: forum 2 pins %v, want %v", step, got, pinned2)
		}
		// Forum 3 lists topic 2 and the marker 3 (to topic 1) in its regular list.
		if got := rowIDs(f3.Pinned); !slices.Equal(got, pinned3) {
			t.Errorf("%s: forum 3 pins %v, want %v", step, got, pinned3)
		}
		if got2, got3 := readForum(t, pool, 2), readForum(t, pool, 3); got2 != before2 || got3 != before3 {
			t.Errorf("%s: forum counters %+v %+v, want %+v %+v", step, got2, got3, before2, before3)
		}
		wantCountersExact(t, pool, step)
	}
	set := func(id int64, kind string) {
		t.Helper()
		if err := board.SetTopicKind(ctx, pool, a, id, kind, t0); err != nil {
			t.Fatalf("set topic %d %s: %v", id, kind, err)
		}
	}

	check("start", []int64{1, n2, n1}, nil, nil)
	set(1, "normal")
	check("topic 1 normal", []int64{n2, n1, 1}, nil, nil)
	set(n1, "sticky")
	check("n1 sticky", []int64{n1, n2, 1}, nil, nil)
	set(1, "announce")
	check("topic 1 announce", []int64{n1, n2}, []int64{1}, nil)
	set(n2, "announce")
	check("n2 announce", []int64{n1}, []int64{n2, 1}, nil)
	set(1, "global")
	check("topic 1 global", []int64{n1}, []int64{1, n2}, []int64{1})
	set(1, "sticky")
	// Two sticky topics: the newer activity first.
	check("topic 1 sticky again", []int64{n1, 1}, []int64{n2}, nil)
	set(n2, "normal")
	check("n2 normal", []int64{n1, 1, n2}, nil, nil)
}

// TestTopicViewOffersKinds checks what the topic page offers.
func TestTopicViewOffersKinds(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	kinds := func(id int64) []string {
		t.Helper()
		tp, err := board.TopicView(ctx, pool, viewerFor(t, pool, id), 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		return tp.Kinds
	}
	if got := kinds(carol); got != nil {
		t.Errorf("plain member is offered %v", got)
	}
	grantOpts(t, pool, carol, 2, "mod.pin")
	if got := kinds(carol); !slices.Equal(got, []string{"normal", "sticky", "announce"}) {
		t.Errorf("moderator is offered %v", got)
	}
	// An admin without mod.pin may only make the sticky topic global.
	if got, want := kinds(alice), []string{"sticky", "global"}; !slices.Equal(got, want) {
		t.Errorf("admin without mod.pin is offered %v, want %v", got, want)
	}
	if err := board.SetTopicKind(ctx, pool, viewerFor(t, pool, alice), 1, "global", t0); err != nil {
		t.Fatal(err)
	}
	if got := kinds(carol); got != nil {
		t.Errorf("moderator is offered %v on a global topic, want nothing", got)
	}
	// From global, the admin may go to any type.
	if got := kinds(alice); !slices.Equal(got, board.TopicKinds) {
		t.Errorf("admin on a global topic is offered %v, want %v", got, board.TopicKinds)
	}
	// topic.sticky and topic.announce are rights to start a topic, not to change one.
	if err := board.SetTopicKind(ctx, pool, viewerFor(t, pool, alice), 1, "sticky", t0); err != nil {
		t.Fatal(err)
	}
	grantOpts(t, pool, bob, 2, "topic.sticky", "topic.announce")
	if got := kinds(bob); got != nil {
		t.Errorf("topic.sticky and topic.announce offer %v on the topic page, want nothing", got)
	}
}

// TestAdminFollowsModPin: the admin right replaces mod.pin only for a change to
// or from global. An admin with mod.pin Never makes no other change.
func TestAdminFollowsModPin(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, alice, 2, "mod.pin")
	denyOpts(t, pool, alice, 2, "mod.pin")
	a := viewerFor(t, pool, alice)
	for _, k := range []string{"normal", "announce"} {
		wantIs(t, board.SetTopicKind(ctx, pool, a, 1, k, t0), board.ErrForbidden, "admin with mod.pin Never, sticky to "+k)
	}
	if err := board.SetTopicKind(ctx, pool, a, 1, "global", t0); err != nil {
		t.Fatalf("admin, sticky to global: %v", err)
	}
	if err := board.SetTopicKind(ctx, pool, a, 1, "announce", t0); err != nil {
		t.Fatalf("admin, global to announce: %v", err)
	}
	wantIs(t, board.SetTopicKind(ctx, pool, a, 1, "normal", t0), board.ErrForbidden, "admin with mod.pin Never, announce to normal")
	if got := topicKind(t, pool, 1); got != "announce" {
		t.Errorf("topic 1 is %q, want announce", got)
	}
	log := readLog(t, pool)
	if len(log) != 2 {
		t.Fatalf("log holds %d rows, want 2: %+v", len(log), log)
	}
	wantKindLog(t, log[0], alice, 1, "sticky", "global")
	wantKindLog(t, log[1], alice, 1, "global", "announce")
}

// TestSetMarkerKind: an admin may set a moved-topic marker to normal, so that a
// pinned marker from an import can be unpinned. Every other change to a marker
// is refused.
func TestSetMarkerKind(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// Topic 3 is a marker in forum 3, moved to topic 1. Pin it, as an import may.
	if _, err := pool.Exec(ctx, `update topics set kind = 'announce' where id = 3 and moved_to_topic_id is not null`); err != nil {
		t.Fatal(err)
	}
	if topicKind(t, pool, 3) != "announce" {
		t.Fatal("fixture: topic 3 is not an announced marker")
	}
	grantOpts(t, pool, alice, 0, "mod.pin")
	a := viewerFor(t, pool, alice)
	for _, k := range []string{"sticky", "global"} {
		wantIs(t, board.SetTopicKind(ctx, pool, a, 3, k, t0), board.ErrForbidden, "admin sets the marker "+k)
	}
	// A moderator with mod.pin everywhere, who can read forum 3, may not touch it.
	grantOpts(t, pool, bob, 0, "mod.pin")
	grantOpts(t, pool, bob, 3, "forum.read")
	wantIs(t, board.SetTopicKind(ctx, pool, viewerFor(t, pool, bob), 3, "normal", t0), board.ErrForbidden, "moderator sets the marker normal")
	if n := len(readLog(t, pool)); n != 0 {
		t.Fatalf("%d log rows after refused changes", n)
	}
	if err := board.SetTopicKind(ctx, pool, a, 3, "normal", t0); err != nil {
		t.Fatalf("admin sets the marker normal: %v", err)
	}
	if got := topicKind(t, pool, 3); got != "normal" {
		t.Errorf("marker is %q, want normal", got)
	}
	log := readLog(t, pool)
	if len(log) != 1 {
		t.Fatalf("log holds %d rows, want 1", len(log))
	}
	wantKindLog(t, log[0], alice, 3, "announce", "normal")
	// Normal now, the marker offers the admin nothing more.
	wantIs(t, board.SetTopicKind(ctx, pool, a, 3, "announce", t0), board.ErrForbidden, "admin pins the marker again")
}

// TestTopicStickyRight: a member with topic.sticky and no mod.pin starts a sticky
// topic, cannot start an announcement, and cannot change a type afterwards.
func TestTopicStickyRight(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	grantOpts(t, pool, carol, 2, "topic.sticky")
	c := viewerFor(t, pool, carol)
	start := func(title, kind string) int64 {
		t.Helper()
		post, err := board.NewTopic(ctx, pool, c, 2, board.Draft{Title: title, Source: "Body of " + title, IP: ip1, Kind: kind}, t0)
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		return topicOfPost(t, pool, post)
	}
	sticky := start("Carol sticky", "sticky")
	if got := topicKind(t, pool, sticky); got != "sticky" {
		t.Errorf("carol's sticky topic is %q", got)
	}
	if got := topicKind(t, pool, start("Carol announce", "announce")); got != "normal" {
		t.Errorf("carol's forged announcement is %q, want normal", got)
	}
	// The topic page offers carol nothing, and the change is refused, even on
	// her own topic.
	tp, err := board.TopicView(ctx, pool, c, sticky, 1)
	if err != nil {
		t.Fatal(err)
	}
	if tp.Kinds != nil {
		t.Errorf("carol is offered %v on her topic", tp.Kinds)
	}
	for _, k := range []string{"normal", "announce"} {
		wantIs(t, board.SetTopicKind(ctx, pool, c, sticky, k, t0), board.ErrForbidden, "carol changes her sticky topic to "+k)
	}
	if got := topicKind(t, pool, sticky); got != "sticky" {
		t.Errorf("carol's topic is %q after refused changes", got)
	}
	// Starting the sticky topic wrote the one log row.
	log := readLog(t, pool)
	if len(log) != 1 {
		t.Fatalf("log holds %d rows, want 1: %+v", len(log), log)
	}
	wantKindLog(t, log[0], carol, sticky, "normal", "sticky")
}

// TestNewTopicKind: the type of a new topic applies only for a writer who may
// give it, and is ignored otherwise.
func TestNewTopicKind(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	start := func(who int64, title, kind string) string {
		t.Helper()
		post, err := board.NewTopic(ctx, pool, viewerFor(t, pool, who), 2,
			board.Draft{Title: title, Source: "Body of " + title, IP: ip1, Kind: kind}, t0)
		if err != nil {
			t.Fatalf("%s: %v", title, err)
		}
		return topicKind(t, pool, topicOfPost(t, pool, post))
	}

	// A plain member's forged type is ignored, and writes no log row.
	for _, k := range []string{"sticky", "announce", "global", "bogus"} {
		if got := start(carol, "Forged "+k, k); got != "normal" {
			t.Errorf("plain member sends %s: topic is %q, want normal", k, got)
		}
	}
	if n := len(readLog(t, pool)); n != 0 {
		t.Fatalf("forged types wrote %d log rows", n)
	}

	// A moderator with mod.pin gets sticky and announce, and not global.
	grantOpts(t, pool, carol, 2, "mod.pin")
	if got := start(carol, "Moderator default", ""); got != "normal" {
		t.Errorf("no type: %q, want normal", got)
	}
	if got := start(carol, "Moderator announce", "announce"); got != "announce" {
		t.Errorf("moderator announce: %q", got)
	}
	if got := start(carol, "Moderator global", "global"); got != "normal" {
		t.Errorf("moderator sends global: %q, want normal", got)
	}
	// An admin gets global.
	if got := start(alice, "Admin global", "global"); got != "global" {
		t.Errorf("admin global: %q", got)
	}

	// One log row for each topic that started with a type other than normal.
	log := readLog(t, pool)
	if len(log) != 2 {
		t.Fatalf("log holds %d rows, want 2: %+v", len(log), log)
	}
	var announceID, globalID int64
	if err := pool.QueryRow(ctx, `select id from topics where title = 'Moderator announce'`).Scan(&announceID); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select id from topics where title = 'Admin global'`).Scan(&globalID); err != nil {
		t.Fatal(err)
	}
	wantKindLog(t, log[0], carol, announceID, "normal", "announce")
	wantKindLog(t, log[1], alice, globalID, "normal", "global")
	wantCountersExact(t, pool, "new topics with a type")
}
