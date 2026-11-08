package importer

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
)

// The users of bundletest.Small.
const (
	alice int64 = 1
	bob   int64 = 2
	carol int64 = 3
)

// convPM is a private message from one user to another. link is its
// conversation_id, or 0 for none.
func convPM(id, from, to int64, subject string, link int64) bundle.PM {
	m := bundle.PM{
		ID: id, AuthorID: &from, AuthorName: "someone",
		CreatedAt: bundletest.Small().PMs[0].CreatedAt,
		Subject:   subject, Body: bundle.Body{Source: "Text.", Markdown: true},
		Recipients: []bundle.Recipient{{UserID: to, Kind: "to"}},
	}
	if link != 0 {
		m.ConversationID = &link
	}
	return m
}

// conversationMessages are the messages of the conversation tests. The comment
// on each line gives its conversation without and with --thread-messages.
func conversationMessages() []bundle.PM {
	return []bundle.PM{
		convPM(1, alice, bob, "Lunch", 0),           // 1, 1: starts one
		convPM(2, bob, alice, "Re: Lunch", 1),       // 1, 1: a linked reply
		convPM(3, alice, bob, "Re: Re: lunch", 2),   // 1, 1: links to a reply; gobb follows the chain
		convPM(4, bob, alice, "Re: Lunch", 0),       // 4, 1: no link; the fallback joins it
		convPM(5, carol, bob, "Re: Lunch", 0),       // 5, 5: other people, never joined
		convPM(6, alice, bob, "Weekend", 6),         // 6, 6: names itself
		convPM(7, alice, carol, "Lunch", 0),         // 7, 7: other people
		convPM(8, bob, alice, "  RE: re:LUNCH ", 0), // 8, 1: prefixes in any case, repeated
		convPM(9, alice, bob, "Re: Lunch", 4),       // 4, 1: follows message 4 when it joins
		convPM(10, bob, alice, "Something else", 0), // 10, 10: another subject
		convPM(11, carol, alice, "Re: Weekend", 0),  // 11, 11: other people than message 6
		convPM(12, bob, alice, "Re: Weekend", 0),    // 12, 6: same people and subject as 6
	}
}

// conversations returns the conversation of every message, by message ID.
func conversations(t *testing.T, pool *pgxpool.Pool) map[int64]int64 {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select id, conversation_id from pms order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, cid int64
		if err := rows.Scan(&id, &cid); err != nil {
			t.Fatal(err)
		}
		out[id] = cid
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func importConversations(t *testing.T, thread bool) map[int64]int64 {
	t.Helper()
	pool := dbtest.New(t)
	dir := writeBoard(t, func(b *bundletest.Board) { b.PMs = conversationMessages() })
	opt := Options{DataDir: t.TempDir(), ThreadMessages: thread}
	if _, err := Run(context.Background(), pool, dir, opt); err != nil {
		t.Fatalf("Run: %v", err)
	}
	return conversations(t, pool)
}

func sameConversations(t *testing.T, got, want map[int64]int64) {
	t.Helper()
	for _, id := range slices.Sorted(maps.Keys(want)) {
		if got[id] != want[id] {
			t.Errorf("message %d is in conversation %d, want %d", id, got[id], want[id])
		}
	}
	if len(got) != len(want) {
		t.Errorf("got %d messages, want %d", len(got), len(want))
	}
}

// Without the flag, only the bundle's links join messages: a linked reply joins
// its parent, a link to a reply joins the first message of the chain, and a
// message without a link starts its own conversation, even when its subject and
// people match an earlier one.
func TestImportConversationLinks(t *testing.T) {
	got := importConversations(t, false)
	sameConversations(t, got, map[int64]int64{
		1: 1, 2: 1, 3: 1, 4: 4, 5: 5, 6: 6, 7: 7, 8: 8, 9: 4, 10: 10, 11: 11, 12: 12,
	})
}

// With the flag, a reply with no link joins the conversation of an earlier
// message with the same people and subject. It never joins one with other
// people, and the linked messages follow the message they name.
func TestImportConversationFallback(t *testing.T) {
	got := importConversations(t, true)
	sameConversations(t, got, map[int64]int64{
		1: 1, 2: 1, 3: 1, 4: 1, 5: 5, 6: 6, 7: 7, 8: 1, 9: 1, 10: 10, 11: 11, 12: 6,
	})
}

// A bundle without the conversation_id key imports with every message in its
// own conversation.
func TestImportConversationKeyAbsent(t *testing.T) {
	pool := dbtest.New(t)
	dir := writeBoard(t, nil)
	editPMLines(t, dir, func(m map[string]any) { delete(m, "conversation_id") })
	mustRun(t, pool, dir, t.TempDir(), false)
	sameConversations(t, conversations(t, pool), map[int64]int64{1: 1, 2: 2})
}

// A bad link stops the import with the message of bundle.Check, and nothing
// lands.
func TestImportConversationRefused(t *testing.T) {
	cases := []struct {
		name string
		pms  []bundle.PM
		want string
	}{
		{"link to a missing message", []bundle.PM{
			convPM(1, alice, bob, "Lunch", 0),
			convPM(2, bob, alice, "Re: Lunch", 99),
		}, "pms.jsonl:2: conversation_id: no row in pms.jsonl has id 99"},
		{"link between messages with no party in common", []bundle.PM{
			convPM(1, alice, bob, "Lunch", 0),
			convPM(2, carol, 4, "Re: Lunch", 1),
		}, "pms.jsonl:2: conversation_id: message 1 shares no party with this one"},
		{"link to a later message", []bundle.PM{
			convPM(1, alice, bob, "Lunch", 2),
			convPM(2, bob, alice, "Re: Lunch", 0),
		}, "pms.jsonl:1: conversation_id: names message 2, which has a higher id"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pool := dbtest.New(t)
			dir := writeBoard(t, func(b *bundletest.Board) { b.PMs = tc.pms })
			_, err := Run(context.Background(), pool, dir, Options{DataDir: t.TempDir(), ThreadMessages: true})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Run = %v, want an error with %q", err, tc.want)
			}
			if n := count(t, pool, "pms"); n != 0 {
				t.Errorf("pms has %d rows after a refused import", n)
			}
		})
	}
}

// resolveLinks refuses a cycle that bundle.Check would have refused first.
func TestResolveLinksRefusesCycle(t *testing.T) {
	pool := dbtest.New(t)
	tx, err := pool.Begin(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	err = resolveLinks(context.Background(), tx, map[int64]int64{1: 2, 2: 1})
	if err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("resolveLinks = %v, want a cycle error", err)
	}
}

// editPMLines rewrites each row of pms.jsonl in dir with f.
func editPMLines(t *testing.T, dir string, f func(m map[string]any)) {
	t.Helper()
	path := filepath.Join(dir, "pms.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []byte
	for line := range bytes.SplitSeq(bytes.TrimSuffix(data, []byte("\n")), []byte("\n")) {
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatal(err)
		}
		f(m)
		b, err := json.Marshal(m)
		if err != nil {
			t.Fatal(err)
		}
		out = append(append(out, b...), '\n')
	}
	if err := os.WriteFile(path, out, 0o644); err != nil {
		t.Fatal(err)
	}
}
