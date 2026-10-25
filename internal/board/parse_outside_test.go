package board_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// holdFirstParse makes the first parse of the test wait: the hook sends on
// entered, then waits for release. Later parses pass.
func holdFirstParse(t *testing.T) (entered <-chan struct{}, release func()) {
	t.Helper()
	in, out := make(chan struct{}), make(chan struct{})
	var n atomic.Int64
	restore := board.SetParseHook(func() {
		if n.Add(1) == 1 {
			in <- struct{}{}
			<-out
		}
	})
	var once atomic.Bool
	free := func() {
		if once.CompareAndSwap(false, true) {
			close(out)
		}
	}
	t.Cleanup(func() { free(); restore() })
	return in, free
}

// wantNoLocks fails when the database holds a transaction open or a row lock on
// the rows that a posting write locks: the topic, its forum, the post and the
// member. It tries FOR UPDATE NOWAIT on each from another connection, which
// conflicts with every row lock that a write takes.
func wantNoLocks(t *testing.T, pool *pgxpool.Pool, what string) {
	t.Helper()
	ctx := context.Background()
	var open int
	if err := pool.QueryRow(ctx, `select count(*) from pg_stat_activity
 where datname = current_database() and pid <> pg_backend_pid() and state like 'idle in transaction%'`).Scan(&open); err != nil {
		t.Fatal(err)
	}
	if open != 0 {
		t.Errorf("%s: %d transactions are open while the text is parsed", what, open)
	}
	var advisory int
	if err := pool.QueryRow(ctx, `select count(*) from pg_locks l join pg_database d on d.oid = l.database
 where d.datname = current_database() and l.locktype = 'advisory'`).Scan(&advisory); err != nil {
		t.Fatal(err)
	}
	if advisory != 0 {
		t.Errorf("%s: %d advisory locks are held while the text is parsed", what, advisory)
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, q := range []string{
		`select 1 from topics where id = 1 for update nowait`,
		`select 1 from forums where id = 2 for update nowait`,
		`select 1 from posts where id = 1 for update nowait`,
		`select 1 from users where id = 1 for update nowait`,
	} {
		if _, err := tx.Exec(ctx, q); err != nil {
			t.Errorf("%s: %q: %v", what, q, err)
			return
		}
	}
}

// TestParseRunsBeforeAnyRowLock covers A-I1, ruling 2. NewTopic, Reply and
// EditPost parse the text before they lock a row, so a slow parse holds up no
// other writer. The hook holds each parse open; meanwhile no lock is held.
func TestParseRunsBeforeAnyRowLock(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(pool *pgxpool.Pool) error
	}{
		{"new topic", func(pool *pgxpool.Pool) error {
			grantOpts(t, pool, alice, 2, "post.no_approval")
			_, err := board.NewTopic(ctx, pool, viewerFor(t, pool, alice), 2, board.Draft{Title: "Slow", Source: "text", IP: ip1}, t0)
			return err
		}},
		{"reply", func(pool *pgxpool.Pool) error {
			_, err := board.Reply(ctx, pool, viewerFor(t, pool, alice), 1, board.Draft{Source: "text", IP: ip1}, t0)
			return err
		}},
		{"edit", func(pool *pgxpool.Pool) error {
			return board.EditPost(ctx, pool, viewerFor(t, pool, alice), 1, board.Draft{Title: "Welcome", Source: "text"}, "", t0)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pool, _ := boardtest.Load(t, bundletest.Small())
			entered, release := holdFirstParse(t)
			done := make(chan error, 1)
			go func() { done <- tc.run(pool) }()
			select {
			case <-entered:
			case err := <-done:
				t.Fatalf("the write ended without a parse: %v", err)
			case <-time.After(10 * time.Second):
				t.Fatal("the parse never started")
			}
			wantNoLocks(t, pool, tc.name)
			release()
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			wantCountersExact(t, pool, tc.name)
		})
	}
}

// TestParseOutsideKeepsTheOrderOfTheAnswers checks that a parse error still waits
// for its turn: a writer without the right gets the refusal, not the parse error,
// and a bad guest name comes before a bad body.
func TestParseOutsideKeepsTheOrderOfTheAnswers(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	allowGuests(t, pool, 2, "topic.start", "topic.reply")
	g := asGuest(t, pool)
	// A guest with a bad name and an empty body: the name is reported first.
	_, err := board.Reply(ctx, pool, g, 1, board.Draft{GuestName: "", Source: " ", IP: ip1}, t0)
	wantInput(t, err, "guest_name", "")
	_, err = board.Reply(ctx, pool, g, 1, board.Draft{GuestName: "Visitor Two", Source: " ", IP: ip1}, t0)
	wantInput(t, err, "body", "")
	// A viewer who may not reply gets the refusal, whatever the body is.
	c := viewerFor(t, pool, carol)
	denyOpts(t, pool, carol, 2, "topic.reply")
	_, err = board.Reply(ctx, pool, viewerFor(t, pool, carol), 1, board.Draft{Source: " ", IP: ip1}, t0)
	wantIs(t, err, board.ErrForbidden, "a reply without the right and with an empty body")
	_ = c
	_, err = board.Reply(ctx, pool, asGuest(t, pool), 3, board.Draft{GuestName: "x", Source: "a", IP: ip1}, t0)
	wantIs(t, err, board.ErrNotFound, "a reply to a marker")
}

// holdParses makes the parses whose number (counted from 1) is in hold wait. The
// hook sends the number on entered, then waits for a send on that parse's
// release channel.
func holdParses(t *testing.T, hold ...int) (entered chan int, release map[int]chan struct{}) {
	t.Helper()
	entered = make(chan int)
	release = map[int]chan struct{}{}
	for _, n := range hold {
		release[n] = make(chan struct{})
	}
	var n atomic.Int64
	t.Cleanup(board.SetParseHook(func() {
		i := int(n.Add(1))
		if ch, ok := release[i]; ok {
			entered <- i
			<-ch
		}
	}))
	return entered, release
}

func waitParse(t *testing.T, entered chan int, want int) {
	t.Helper()
	select {
	case got := <-entered:
		if got != want {
			t.Fatalf("parse %d started, want %d", got, want)
		}
	case <-time.After(10 * time.Second):
		t.Fatalf("parse %d never started", want)
	}
}

// TestEditChecksEditCountUnderTheLock covers A-I1, ruling 2: an edit parses with
// the stored values it read without a lock. Under the lock it compares edit_count.
// An edit that committed in between makes it parse again, once, with the fresh
// values; a second change makes it give up with an input error.
func TestEditChecksEditCountUnderTheLock(t *testing.T) {
	ctx := context.Background()

	t.Run("one change in between parses again", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		a := viewerFor(t, pool, alice)
		entered, release := holdParses(t, 1)
		done := make(chan error, 1)
		go func() {
			done <- board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Welcome", Source: "first writer"}, "", t0)
		}()
		waitParse(t, entered, 1)
		// Another edit of the same post commits while the first one parses.
		if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Welcome", Source: "second writer"}, "", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		close(release[1])
		if err := <-done; err != nil {
			t.Fatalf("the edit that was overtaken once: %v", err)
		}
		p := readPost(t, pool, 1)
		if p.Source != "first writer" || p.EditCount != 3 {
			t.Errorf("post = %q, edit_count %d, want the first writer's text and 3 edits (1 imported + 2)", p.Source, p.EditCount)
		}
	})

	t.Run("a second change gives up", func(t *testing.T) {
		pool, _ := boardtest.Load(t, bundletest.Small())
		a := viewerFor(t, pool, alice)
		// Parse 1 is the first writer's, parse 2 the first overtaking edit's, parse 3
		// the first writer's again, parse 4 the second overtaking edit's.
		entered, release := holdParses(t, 1, 3)
		done := make(chan error, 1)
		go func() {
			done <- board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Welcome", Source: "first writer"}, "", t0)
		}()
		waitParse(t, entered, 1)
		if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Welcome", Source: "overtaking one"}, "", t0.Add(time.Minute)); err != nil {
			t.Fatal(err)
		}
		close(release[1])
		waitParse(t, entered, 3)
		if err := board.EditPost(ctx, pool, a, 1, board.Draft{Title: "Welcome", Source: "overtaking two"}, "", t0.Add(2*time.Minute)); err != nil {
			t.Fatal(err)
		}
		close(release[3])
		err := <-done
		var ie *board.InputError
		if !errors.As(err, &ie) || ie.Field != "body" {
			t.Fatalf("error = %v, want an input error on body that asks to try again", err)
		}
		p := readPost(t, pool, 1)
		if p.Source != "overtaking two" || p.EditCount != 3 {
			t.Errorf("post = %q, edit_count %d: the refused edit changed the post", p.Source, p.EditCount)
		}
	})
}
