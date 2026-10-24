package board_test

import (
	"context"
	"encoding/json"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
	"github.com/evanwtf/gobb/markup"
)

// userRow is the part of a users row that the account writes change.
type userRow struct {
	Hash, Scheme, Email, EmailClean string
	Verified                        bool
	SigSource                       string
	SigTree                         []byte
}

func readUser(t *testing.T, pool *pgxpool.Pool, id int64) userRow {
	t.Helper()
	var u userRow
	err := pool.QueryRow(context.Background(), `
select password_hash, password_scheme, email, email_clean, email_verified, signature_source, signature_tree
  from users where id = $1`, id).
		Scan(&u.Hash, &u.Scheme, &u.Email, &u.EmailClean, &u.Verified, &u.SigSource, &u.SigTree)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func sessionCount(t *testing.T, pool *pgxpool.Pool, id int64) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `select count(*) from sessions where user_id = $1`, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	ip := netip.MustParseAddr("10.1.2.3")
	for _, id := range []int64{alice, alice, bob} {
		if _, _, err := session.Create(ctx, pool, id, false, ip, "t", t0); err != nil {
			t.Fatal(err)
		}
	}

	// alice has a gobb hash. The change stores a new gobb hash and ends every
	// session of alice, and only hers.
	a := viewerFor(t, pool, alice)
	before := readUser(t, pool, alice)
	if err := board.ChangePassword(ctx, pool, a, "a brand new password", t0); err != nil {
		t.Fatal(err)
	}
	after := readUser(t, pool, alice)
	if after.Hash == before.Hash || after.Scheme != "gobb" {
		t.Errorf("hash changed = %v, scheme = %q", after.Hash != before.Hash, after.Scheme)
	}
	if ok, _ := password.Verify("a brand new password", after.Hash, after.Scheme); !ok {
		t.Error("the stored hash does not verify the new password")
	}
	if ok, _ := password.Verify("example-password", after.Hash, after.Scheme); ok {
		t.Error("the stored hash still verifies the old password")
	}
	if n := sessionCount(t, pool, alice); n != 0 {
		t.Errorf("alice has %d sessions after the change, want 0", n)
	}
	if n := sessionCount(t, pool, bob); n != 1 {
		t.Errorf("bob has %d sessions, want 1", n)
	}

	// bob has a legacy hash. The new hash is a gobb hash, and the scheme moves.
	b := viewerFor(t, pool, bob)
	if err := board.ChangePassword(ctx, pool, b, "bob picks a phrase", t0); err != nil {
		t.Fatal(err)
	}
	if u := readUser(t, pool, bob); u.Scheme != "gobb" || !strings.HasPrefix(u.Hash, "$argon2id$") {
		t.Errorf("bob's scheme = %q, hash = %.12q", u.Scheme, u.Hash)
	}
}

func TestChangePasswordNeedsTheRight(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	ip := netip.MustParseAddr("10.1.2.3")
	if _, _, err := session.Create(ctx, pool, carol, false, ip, "t", t0); err != nil {
		t.Fatal(err)
	}
	denyOpts(t, pool, carol, 0, "account.password", "account.email", "account.signature")
	before := readUser(t, pool, carol)

	c := viewerFor(t, pool, carol)
	wantIs(t, board.ChangePassword(ctx, pool, c, "a long enough one", t0), board.ErrForbidden, "ChangePassword without the right")
	wantIs(t, board.ChangePassword(ctx, pool, asGuest(t, pool), "a long enough one", t0), board.ErrForbidden, "ChangePassword by a guest")
	wantIs(t, board.ChangeEmail(ctx, pool, c, "carol@elsewhere.example", t0), board.ErrForbidden, "ChangeEmail without the right")
	wantIs(t, board.ChangeEmail(ctx, pool, asGuest(t, pool), "g@elsewhere.example", t0), board.ErrForbidden, "ChangeEmail by a guest")
	wantIs(t, board.SetSignature(ctx, pool, c, "new", t0), board.ErrForbidden, "SetSignature without the right")
	wantIs(t, board.SetSignature(ctx, pool, asGuest(t, pool), "new", t0), board.ErrForbidden, "SetSignature by a guest")

	if after := readUser(t, pool, carol); after.Hash != before.Hash || after.Email != before.Email || after.SigSource != before.SigSource {
		t.Errorf("a refused write changed the row: %+v -> %+v", before, after)
	}
	if n := sessionCount(t, pool, carol); n != 1 {
		t.Errorf("a refused password change deleted sessions: %d left", n)
	}
}

// The rules for a new password. A refusal changes nothing, and keeps the sessions.
func TestChangePasswordRules(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// The username must be long enough to reach the comparison.
	if _, err := pool.Exec(ctx, `update users set username = 'Alice-Longname', username_clean = 'alice-longname' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.Create(ctx, pool, alice, false, netip.MustParseAddr("10.1.2.3"), "t", t0); err != nil {
		t.Fatal(err)
	}
	a := viewerFor(t, pool, alice)
	before := readUser(t, pool, alice)

	const short, long, same = "Use at least 10 characters.", "Use at most 1,024 bytes.", "Choose a password that differs from your username."
	for _, tc := range []struct {
		name, pw, msg string
	}{
		{"empty", "", short},
		{"9 characters", "123456789", short},
		{"9 two-byte characters", strings.Repeat("é", 9), short},
		{"9 characters in decomposed form", strings.Repeat("é", 9), short},
		{"1,025 bytes", strings.Repeat("a", 1025), long},
		{"1,025 bytes of two-byte characters", strings.Repeat("é", 513), long},
		{"the username", "Alice-Longname", same},
		{"the username in other case", "ALICE-LONGNAME", same},
		{"the username in full-width letters", "ａｌｉｃｅ-longname", same},
		{"the username with a zero-width space", "alice\u200b-longname", same},
		{"not UTF-8", "ten bytes \xff\xfe", "Use a password that is valid UTF-8."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := board.ChangePassword(ctx, pool, a, tc.pw, t0)
			wantInput(t, err, "next", tc.msg)
			wantInput(t, board.CheckNewPassword(ctx, pool, a, tc.pw), "next", tc.msg)
			if after := readUser(t, pool, alice); after.Hash != before.Hash {
				t.Error("a refused password changed the hash")
			}
			if n := sessionCount(t, pool, alice); n != 1 {
				t.Errorf("a refused password left %d sessions, want 1", n)
			}
		})
	}

	// The edges that pass: 10 characters, and 1,024 bytes.
	for _, pw := range []string{strings.Repeat("é", 10), "éééééééééé", strings.Repeat("a", 1024), strings.Repeat("é", 512)} {
		if err := board.CheckNewPassword(ctx, pool, a, pw); err != nil {
			t.Errorf("CheckNewPassword(%d bytes) = %v, want nil", len(pw), err)
		}
	}
	if err := board.ChangePassword(ctx, pool, a, strings.Repeat("a", 1024), t0); err != nil {
		t.Errorf("a password of 1,024 bytes: %v", err)
	}
}

func TestChangeEmail(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	// carol is not verified. Make her verified so that the change has something to reset.
	if _, err := pool.Exec(ctx, `update users set email_verified = true where id = 3`); err != nil {
		t.Fatal(err)
	}
	c := viewerFor(t, pool, carol)

	// An address that another account uses is refused. The comparison is by
	// email_clean, so case and edge spaces do not matter.
	const taken = "That address belongs to another account."
	for _, e := range []string{"alice@example.com", "  Alice@Example.COM ", "BOB@example.com"} {
		wantInput(t, board.ChangeEmail(ctx, pool, c, e, t0), "email", taken)
	}
	if u := readUser(t, pool, carol); u.Email != "carol@example.com" || !u.Verified {
		t.Fatalf("a refused change touched the row: %+v", u)
	}

	// A free address is saved as typed (trimmed), and the address is unverified.
	if err := board.ChangeEmail(ctx, pool, c, "  Carol.New@Example.org ", t0); err != nil {
		t.Fatal(err)
	}
	u := readUser(t, pool, carol)
	if u.Email != "Carol.New@Example.org" || u.EmailClean != "carol.new@example.org" || u.Verified {
		t.Errorf("after the change: %+v", u)
	}

	// The own address, unchanged, is a no-op: the same row, and the same
	// verified flag. Case alone does not make a change either.
	if _, err := pool.Exec(ctx, `update users set email_verified = true where id = 3`); err != nil {
		t.Fatal(err)
	}
	for _, e := range []string{"Carol.New@Example.org", "carol.new@example.org", " CAROL.NEW@EXAMPLE.ORG "} {
		if err := board.ChangeEmail(ctx, pool, c, e, t0); err != nil {
			t.Errorf("own address %q: %v", e, err)
		}
		if got := readUser(t, pool, carol); got.Email != "Carol.New@Example.org" || !got.Verified {
			t.Errorf("own address %q changed the row: %+v", e, got)
		}
	}
}

// Two members already share an address on the first board. The check does not
// touch that data, and a member of such a pair can still change to a free one.
func TestChangeEmailSharedAddress(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, err := pool.Exec(ctx, `update users set email = 'shared@example.net', email_clean = 'shared@example.net' where id in (2, 3)`); err != nil {
		t.Fatal(err)
	}
	c := viewerFor(t, pool, carol)
	// The own address, shared with bob, is unchanged: a no-op, not "taken".
	if err := board.ChangeEmail(ctx, pool, c, "shared@example.net", t0); err != nil {
		t.Errorf("carol keeps the address that bob shares: %v", err)
	}
	// A third member may not take it.
	wantInput(t, board.ChangeEmail(ctx, pool, viewerFor(t, pool, alice), "shared@example.net", t0), "email", "That address belongs to another account.")
	// carol leaves it for a free one; bob keeps it.
	if err := board.ChangeEmail(ctx, pool, c, "carol@example.net", t0); err != nil {
		t.Fatal(err)
	}
	if u := readUser(t, pool, bob); u.Email != "shared@example.net" {
		t.Errorf("bob's address = %q", u.Email)
	}
}

func TestChangeEmailFormat(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	const bad = "Enter a valid email address."
	for _, e := range []string{
		"", "   ", "carol", "carol@", "@example.org", "a b@example.org", "a@b@example.org",
		"Carol <carol@example.org>", "carol@example.org\r\nBcc: x@example.org",
		"ca\x00rol@example.org", "carol@exam\xffple.org", "\"a b\"@example.org",
		strings.Repeat("a", 250) + "@example.org",
	} {
		if err := board.ChangeEmail(ctx, pool, c, e, t0); err == nil {
			t.Errorf("%q was accepted", e)
		} else {
			wantInput(t, err, "email", bad)
		}
		if u := readUser(t, pool, carol); u.Email != "carol@example.com" {
			t.Fatalf("%q was stored as %q", e, u.Email)
		}
	}
}

// Two members who ask for the same free address at once: one wins, the other
// is told it is taken. Without the lock, both would pass the check.
func TestChangeEmailRace(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	var wg sync.WaitGroup
	errs := make([]error, 3)
	for i, id := range []int64{alice, bob, carol} {
		v := viewerFor(t, pool, id)
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs[i] = board.ChangeEmail(ctx, pool, v, "contested@example.org", t0)
		}()
	}
	wg.Wait()
	won := 0
	for _, err := range errs {
		if err == nil {
			won++
		} else {
			wantInput(t, err, "email", "That address belongs to another account.")
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from users where email_clean = 'contested@example.org'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if won != 1 || n != 1 {
		t.Errorf("%d changes succeeded and %d rows hold the address, want 1 and 1", won, n)
	}
}

// The lock is what stops the race, so the test holds it. A second member takes
// the address while alice's change waits on the lock; when the lock is released,
// alice must see the committed row and be refused. Without the lock, her check
// runs at once, finds nothing, and the address is taken twice.
func TestChangeEmailWaitsForTheAddressLock(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended('gobb:email:held@example.org', 0))`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	a := viewerFor(t, pool, alice)
	go func() { done <- board.ChangeEmail(ctx, pool, a, "held@example.org", t0) }()

	// Wait until the change is blocked on the lock.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `select exists (select 1 from pg_locks where locktype = 'advisory' and not granted)`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("ChangeEmail finished without waiting for the lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("ChangeEmail never waited on the lock")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `update users set email = 'held@example.org', email_clean = 'held@example.org' where id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wantInput(t, <-done, "email", "That address belongs to another account.")
	if u := readUser(t, pool, alice); u.Email != "alice@example.com" {
		t.Errorf("alice's address = %q", u.Email)
	}
}

func TestSetSignature(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)

	if err := board.SetSignature(ctx, pool, c, "Plain **bold** words", t0); err != nil {
		t.Fatal(err)
	}
	u := readUser(t, pool, carol)
	if u.SigSource != "Plain **bold** words" || len(u.SigTree) == 0 {
		t.Fatalf("stored source %q, tree %q", u.SigSource, u.SigTree)
	}
	tree, err := markup.Decode(u.SigTree)
	if err != nil {
		t.Fatal(err)
	}
	if html := markup.Render(tree, markup.RenderContext{}).HTML; !strings.Contains(string(html), "<strong>bold</strong>") {
		t.Errorf("the tree renders as %s", html)
	}
	if !json.Valid(u.SigTree) {
		t.Error("the stored tree is not JSON")
	}

	// The stored flags decide the parse: with Markdown off, ** stays text.
	if _, err := pool.Exec(ctx, `update users set signature_markdown = false where id = 3`); err != nil {
		t.Fatal(err)
	}
	if err := board.SetSignature(ctx, pool, c, "Plain **bold** words", t0); err != nil {
		t.Fatal(err)
	}
	tree, _ = markup.Decode(readUser(t, pool, carol).SigTree)
	if html := markup.Render(tree, markup.RenderContext{}).HTML; strings.Contains(string(html), "<strong>") {
		t.Errorf("a signature with Markdown off rendered bold: %s", html)
	}

	// An empty signature clears the source and the tree.
	for _, empty := range []string{"", "  \n "} {
		if err := board.SetSignature(ctx, pool, c, "again", t0); err != nil {
			t.Fatal(err)
		}
		if err := board.SetSignature(ctx, pool, c, empty, t0); err != nil {
			t.Fatal(err)
		}
		if u := readUser(t, pool, carol); u.SigSource != "" || u.SigTree != nil {
			t.Errorf("after %q: source %q, tree %q, want both empty", empty, u.SigSource, u.SigTree)
		}
	}
}

func TestSetSignatureLimits(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	c := viewerFor(t, pool, carol)
	if err := board.SetSignature(ctx, pool, c, "kept", t0); err != nil {
		t.Fatal(err)
	}
	wantInput(t, board.SetSignature(ctx, pool, c, strings.Repeat("a", 2001), t0), "body", "The text is too long. The limit is 2,000 bytes.")
	wantInput(t, board.SetSignature(ctx, pool, c, "bad \xff", t0), "body", "")
	wantInput(t, board.SetSignature(ctx, pool, c, "nul \x00", t0), "body", "")
	// An image needs post.images in some forum; carol holds none.
	wantInput(t, board.SetSignature(ctx, pool, c, "![x](https://img.example/a.png)", t0), "body", "You may not use images in a signature.")
	if u := readUser(t, pool, carol); u.SigSource != "kept" {
		t.Errorf("a refused signature changed the stored one to %q", u.SigSource)
	}
	if err := board.SetSignature(ctx, pool, c, strings.Repeat("a", 2000), t0); err != nil {
		t.Errorf("2,000 bytes: %v", err)
	}
	grantOpts(t, pool, carol, 2, "post.images")
	if err := board.SetSignature(ctx, pool, viewerFor(t, pool, carol), "![x](https://img.example/a.png)", t0); err != nil {
		t.Errorf("an image with post.images in a forum: %v", err)
	}
}

func TestAccountPage(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	a, err := board.Account(ctx, pool, viewerFor(t, pool, alice))
	if err != nil {
		t.Fatal(err)
	}
	if a.Email != "alice@example.com" || a.Signature != "Example signature" || !a.CanPassword || !a.CanEmail || !a.CanSignature {
		t.Errorf("alice's account = %+v", a)
	}
	denyOpts(t, pool, carol, 0, "account.email")
	c, err := board.Account(ctx, pool, viewerFor(t, pool, carol))
	if err != nil {
		t.Fatal(err)
	}
	if c.CanEmail || !c.CanPassword {
		t.Errorf("carol's rights = %+v", c)
	}
	_, err = board.Account(ctx, pool, asGuest(t, pool))
	wantIs(t, err, board.ErrForbidden, "Account of a guest")
}
