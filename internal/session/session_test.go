package session_test

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/session"
)

var t0 = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)

func TestSessionLifecycle(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	ip := netip.MustParseAddr("10.1.2.3")

	token, s, err := session.Create(ctx, pool, 1, false, ip, "TestAgent/1.0", t0)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(token))
	if want := hex.EncodeToString(sum[:]); s.ID != want {
		t.Errorf("ID = %q, want the SHA-256 of the token, %q", s.ID, want)
	}
	if len(token) != 43 || strings.ContainsAny(token, "+/=") {
		t.Errorf("token %q is not 32 bytes of base64url without padding", token)
	}
	if s.UserID != 1 || s.Remember || !s.CreatedAt.Equal(t0) || !s.LastSeenAt.Equal(t0) || !s.ExpiresAt.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("session = %+v", s)
	}
	var stored string
	if err := pool.QueryRow(ctx, `select id from sessions`).Scan(&stored); err != nil || stored != s.ID {
		t.Errorf("stored id = %q (%v), want %q", stored, err, s.ID)
	}
	var ipText, ua string
	if err := pool.QueryRow(ctx, `select host(ip), user_agent from sessions`).Scan(&ipText, &ua); err != nil || ipText != "10.1.2.3" || ua != "TestAgent/1.0" {
		t.Errorf("stored ip and agent = %q %q (%v)", ipText, ua, err)
	}

	got, err := session.Lookup(ctx, pool, token, t0.Add(time.Minute))
	if err != nil || got.ID != s.ID || got.UserID != 1 {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
	if _, err := session.Lookup(ctx, pool, token, t0.Add(24*time.Hour+time.Second)); !errors.Is(err, session.ErrNone) {
		t.Errorf("Lookup after expiry = %v, want ErrNone", err)
	}
	for _, bad := range []string{"", "x", "not a token", token + "x", strings.ToUpper(token)} {
		if _, err := session.Lookup(ctx, pool, bad, t0); !errors.Is(err, session.ErrNone) {
			t.Errorf("Lookup(%q) = %v, want ErrNone", bad, err)
		}
	}

	// Touch inside 5 minutes changes nothing.
	if err := session.Touch(ctx, pool, got, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	row := func() (seen, expires time.Time) {
		t.Helper()
		if err := pool.QueryRow(ctx, `select last_seen_at, expires_at from sessions where id = $1`, s.ID).Scan(&seen, &expires); err != nil {
			t.Fatal(err)
		}
		return seen, expires
	}
	if seen, exp := row(); !seen.Equal(t0) || !exp.Equal(t0.Add(24*time.Hour)) {
		t.Errorf("after a Touch at +1 minute: seen %v, expires %v", seen, exp)
	}
	// Touch after 10 minutes moves both.
	later := t0.Add(10 * time.Minute)
	if err := session.Touch(ctx, pool, got, later); err != nil {
		t.Fatal(err)
	}
	if seen, exp := row(); !seen.Equal(later) || !exp.Equal(later.Add(24*time.Hour)) {
		t.Errorf("after a Touch at +10 minutes: seen %v, expires %v", seen, exp)
	}

	var n int
	if err := pool.QueryRow(ctx, `select count(*) from user_ips where user_id = 1 and host(ip) = '10.1.2.3' and last_seen_at = $1`, t0).Scan(&n); err != nil || n != 1 {
		t.Errorf("user_ips rows = %d (%v), want 1", n, err)
	}

	if err := session.Delete(ctx, pool, token); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Lookup(ctx, pool, token, later); !errors.Is(err, session.ErrNone) {
		t.Errorf("Lookup after Delete = %v, want ErrNone", err)
	}
	// Deleting a token twice, or a bad token, is not an error.
	if err := session.Delete(ctx, pool, token); err != nil {
		t.Errorf("second Delete: %v", err)
	}
}

func TestSessionRemember(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	token, s, err := session.Create(ctx, pool, 2, true, netip.MustParseAddr("2001:db8::9"), "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Remember || !s.ExpiresAt.Equal(t0.Add(365*24*time.Hour)) {
		t.Errorf("session = %+v", s)
	}
	got, err := session.Lookup(ctx, pool, token, t0)
	if err != nil || !got.Remember {
		t.Fatalf("Lookup = %+v, %v", got, err)
	}
	later := t0.Add(30 * 24 * time.Hour)
	if !session.NeedsTouch(got, later) {
		t.Error("NeedsTouch = false after 30 days")
	}
	if err := session.Touch(ctx, pool, got, later); err != nil {
		t.Fatal(err)
	}
	again, err := session.Lookup(ctx, pool, token, later)
	if err != nil || !again.ExpiresAt.Equal(later.Add(365*24*time.Hour)) || !again.LastSeenAt.Equal(later) {
		t.Errorf("after Touch: %+v, %v", again, err)
	}
	if session.NeedsTouch(again, later.Add(time.Minute)) {
		t.Error("NeedsTouch = true one minute after a touch")
	}
}

// A user agent over 512 bytes is cut, and never inside a character.
func TestSessionUserAgentCut(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	// 511 ASCII bytes, then a 3-byte character that would straddle byte 512.
	ua := strings.Repeat("a", 511) + "€" + "tail\x00end\xff"
	if _, _, err := session.Create(ctx, pool, 1, false, netip.MustParseAddr("10.1.2.3"), ua, t0); err != nil {
		t.Fatal(err)
	}
	var got string
	if err := pool.QueryRow(ctx, `select user_agent from sessions`).Scan(&got); err != nil {
		t.Fatal(err)
	}
	if got != strings.Repeat("a", 511) {
		t.Errorf("stored agent has %d bytes: %q", len(got), got[max(0, len(got)-10):])
	}
}

func TestSessionCreateUnknownUser(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	if _, _, err := session.Create(t.Context(), pool, 999, false, netip.MustParseAddr("10.1.2.3"), "", t0); err == nil {
		t.Error("Create for a user that does not exist gave no error")
	}
	if _, _, err := session.Create(t.Context(), pool, 1, false, netip.Addr{}, "", t0); err == nil {
		t.Error("Create with no address gave no error")
	}
}

func TestCSRF(t *testing.T) {
	tok := session.CSRFToken("secret-one")
	if tok == "" || strings.ContainsAny(tok, "+/=") {
		t.Fatalf("token %q", tok)
	}
	if tok != session.CSRFToken("secret-one") {
		t.Error("the token is not stable")
	}
	if !session.CheckCSRF("secret-one", tok) {
		t.Error("a token fails against its own secret")
	}
	for name, got := range map[string]string{
		"another secret": session.CSRFToken("secret-two"),
		"empty":          "",
		"cut":            tok[:len(tok)-1],
		"long":           tok + "A",
	} {
		if session.CheckCSRF("secret-one", got) {
			t.Errorf("%s: the check passed", name)
		}
	}
	// An empty secret never validates, not even its own token.
	if session.CheckCSRF("", session.CSRFToken("")) {
		t.Error("an empty secret validated")
	}
}

// A session of a user who is not active is no session: the rule lives in Lookup,
// so no caller can forget it.
func TestLookupRequiresActiveUser(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	token, _, err := session.Create(ctx, pool, 2, false, netip.MustParseAddr("10.1.2.3"), "", t0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := session.Lookup(ctx, pool, token, t0); err != nil {
		t.Fatalf("Lookup of an active user: %v", err)
	}
	if _, err := pool.Exec(ctx, `update users set state = 'inactive' where id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Lookup(ctx, pool, token, t0); !errors.Is(err, session.ErrNone) {
		t.Errorf("Lookup of an inactive user = %v, want ErrNone", err)
	}
	if _, err := pool.Exec(ctx, `update users set state = 'active' where id = 2`); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Lookup(ctx, pool, token, t0); err != nil {
		t.Errorf("Lookup after the user is active again: %v", err)
	}
}

func TestCleanup(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	now := t0.Add(100 * 24 * time.Hour)

	// An expired session, a live one and a remembered one that has not expired.
	expired, _, err := session.Create(ctx, pool, 1, false, netip.MustParseAddr("10.1.0.1"), "", now.Add(-48*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	live, _, err := session.Create(ctx, pool, 1, false, netip.MustParseAddr("10.1.0.2"), "", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	remembered, _, err := session.Create(ctx, pool, 2, true, netip.MustParseAddr("10.1.0.3"), "", now.Add(-30*24*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Rows of user_ips at 91 days, 89 days, and just past 90 days.
	for ip, age := range map[string]time.Duration{
		"10.1.0.1": 91 * 24 * time.Hour,
		"10.1.0.2": 89 * 24 * time.Hour,
		"10.1.0.3": 90*24*time.Hour + time.Second,
	} {
		if _, err := pool.Exec(ctx, `update user_ips set last_seen_at = $2 where host(ip) = $1`, ip, now.Add(-age)); err != nil {
			t.Fatal(err)
		}
	}

	sessions, ips, err := session.Cleanup(ctx, pool, now)
	if err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || ips != 2 {
		t.Errorf("Cleanup = %d sessions, %d ips; want 1, 2", sessions, ips)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from sessions`).Scan(&n); err != nil || n != 2 {
		t.Errorf("sessions left = %d (%v), want 2", n, err)
	}
	for name, token := range map[string]string{"live": live, "remembered": remembered} {
		if _, err := session.Lookup(ctx, pool, token, now); err != nil {
			t.Errorf("the %s session is gone: %v", name, err)
		}
	}
	var gone int
	if err := pool.QueryRow(ctx, `select count(*) from sessions where id = $1`, mustHash(expired)).Scan(&gone); err != nil || gone != 0 {
		t.Errorf("the expired session stays (%d, %v)", gone, err)
	}
	var ipsLeft []string
	rows, err := pool.Query(ctx, `select host(ip) from user_ips order by 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var ip string
		if err := rows.Scan(&ip); err != nil {
			t.Fatal(err)
		}
		ipsLeft = append(ipsLeft, ip)
	}
	if len(ipsLeft) != 1 || ipsLeft[0] != "10.1.0.2" {
		t.Errorf("user_ips left = %v, want [10.1.0.2]", ipsLeft)
	}

	// A second run finds nothing.
	if s, i, err := session.Cleanup(ctx, pool, now); err != nil || s != 0 || i != 0 {
		t.Errorf("second Cleanup = %d, %d, %v; want 0, 0, nil", s, i, err)
	}
}

// mustHash returns the stored id of a token.
func mustHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// An address that cannot be read is never stored in user_ips, so that no member
// has "the address of everyone whose address is unknown" as a known address.
func TestCreateSkipsUnspecifiedAddress(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	for _, a := range []string{"0.0.0.0", "::", "::ffff:0.0.0.0"} {
		token, _, err := session.Create(ctx, pool, 1, false, netip.MustParseAddr(a), "", t0)
		if err != nil {
			t.Fatalf("Create from %s: %v", a, err)
		}
		if _, err := session.Lookup(ctx, pool, token, t0); err != nil {
			t.Errorf("the session from %s is not live: %v", a, err)
		}
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from user_ips where ip in ('0.0.0.0', '::')`).Scan(&n); err != nil || n != 0 {
		t.Errorf("user_ips rows for an unspecified address = %d (%v), want 0", n, err)
	}
	var sessions int
	if err := pool.QueryRow(ctx, `select count(*) from sessions where user_id = 1`).Scan(&sessions); err != nil || sessions != 3 {
		t.Errorf("sessions = %d (%v), want 3", sessions, err)
	}
}

// DeleteUser ends every session of one member and no session of anyone else. It
// reports how many rows it deleted, and a member with none is not an error.
func TestDeleteUser(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	ip := netip.MustParseAddr("10.1.2.3")
	var aliceTokens []string
	for _, remember := range []bool{false, true, false} {
		token, _, err := session.Create(ctx, pool, 1, remember, ip, "a", t0)
		if err != nil {
			t.Fatal(err)
		}
		aliceTokens = append(aliceTokens, token)
	}
	bobToken, _, err := session.Create(ctx, pool, 2, false, ip, "b", t0)
	if err != nil {
		t.Fatal(err)
	}

	n, err := session.DeleteUser(ctx, pool, 1)
	if err != nil || n != 3 {
		t.Fatalf("DeleteUser(1) = %d, %v, want 3, nil", n, err)
	}
	for _, token := range aliceTokens {
		if _, err := session.Lookup(ctx, pool, token, t0); !errors.Is(err, session.ErrNone) {
			t.Errorf("a session of user 1 survived: %v", err)
		}
	}
	if _, err := session.Lookup(ctx, pool, bobToken, t0); err != nil {
		t.Errorf("the session of user 2 was deleted: %v", err)
	}
	if n, err := session.DeleteUser(ctx, pool, 1); err != nil || n != 0 {
		t.Errorf("DeleteUser of a member with no session = %d, %v, want 0, nil", n, err)
	}
	if n, err := session.DeleteUser(ctx, pool, 9999); err != nil || n != 0 {
		t.Errorf("DeleteUser of an unknown member = %d, %v, want 0, nil", n, err)
	}
}

// CreateChecked inserts the session only while the user row holds the password epoch
// that the sign-in read, the user is active, and no user ban holds. A refusal is
// ErrStale and writes nothing.
func TestCreateChecked(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	ip := netip.MustParseAddr("10.1.2.3")
	const epoch = 3
	if _, err := pool.Exec(ctx, `update users set password_epoch = $1 where id = 1`, epoch); err != nil {
		t.Fatal(err)
	}
	count := func() int {
		var n int
		if err := pool.QueryRow(ctx, `select count(*) from sessions`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	token, s, err := session.CreateChecked(ctx, pool, 1, epoch, true, ip, "ua", t0)
	if err != nil || !s.Remember || s.UserID != 1 {
		t.Fatalf("CreateChecked = %+v, %v", s, err)
	}
	if _, err := session.Lookup(ctx, pool, token, t0); err != nil {
		t.Errorf("the new session does not look up: %v", err)
	}
	var seen int
	if err := pool.QueryRow(ctx, `select count(*) from user_ips where user_id = 1 and host(ip) = '10.1.2.3'`).Scan(&seen); err != nil || seen != 1 {
		t.Errorf("user_ips rows = %d, %v, want 1", seen, err)
	}
	if count() != 1 {
		t.Fatalf("%d sessions", count())
	}

	refuse := func(what string, userID, want int64) {
		t.Helper()
		before := count()
		if _, _, err := session.CreateChecked(ctx, pool, userID, want, false, ip, "ua", t0); !errors.Is(err, session.ErrStale) {
			t.Errorf("%s: err = %v, want ErrStale", what, err)
		}
		if count() != before {
			t.Errorf("%s: a session was written", what)
		}
	}
	refuse("an older epoch", 1, epoch-1)
	refuse("a newer epoch", 1, epoch+1)
	refuse("an unknown user", 9999, epoch)
	// The hash is no part of the check: a new hash under the same epoch (a
	// rehash) still passes.
	if _, err := pool.Exec(ctx, `update users set password_hash = 'rehashed', password_scheme = 'gobb' where id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, _, err := session.CreateChecked(ctx, pool, 1, epoch, false, ip, "ua", t0); err != nil {
		t.Errorf("after a rehash under the same epoch: %v", err)
	}

	// A user ban that has not ended refuses; an ended one, an email ban and an ip
	// ban do not (the sign-in check owns those two).
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	exec(`insert into bans (kind, user_id, created_at, ends_at) values ('user', 1, $1, $2)`, t0.Add(-time.Hour), t0.Add(-time.Minute))
	exec(`insert into bans (kind, email, created_at) values ('email', 'alice@example.com', $1)`, t0)
	exec(`insert into bans (kind, ip, created_at) values ('ip', '10.0.0.0/8', $1)`, t0)
	if _, _, err := session.CreateChecked(ctx, pool, 1, epoch, false, ip, "ua", t0); err != nil {
		t.Errorf("with only ended, email and ip bans: %v", err)
	}
	exec(`insert into bans (kind, user_id, created_at) values ('user', 1, $1)`, t0)
	refuse("a user ban with no end", 1, epoch)
	exec(`delete from bans where kind = 'user'`)
	exec(`insert into bans (kind, user_id, created_at, ends_at) values ('user', 1, $1, $2)`, t0, t0.Add(time.Hour))
	refuse("a user ban that ends later", 1, epoch)
	exec(`delete from bans where kind = 'user'`)
	exec(`update users set state = 'inactive' where id = 1`)
	refuse("an inactive user", 1, epoch)
}

// The check and the insert are ordered against a write that holds the row for
// update (a password change, a ban, a reset): the insert waits for it, then sees
// what it committed.
func TestCreateCheckedWaitsForTheRowLock(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	ip := netip.MustParseAddr("10.1.2.3")
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `select 1 from users where id = 1 for update`); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := session.CreateChecked(ctx, pool, 1, 0, false, ip, "ua", t0)
		done <- err
	}()
	// Wait until the insert is blocked on a lock in this test's own database.
	deadline := time.Now().Add(5 * time.Second)
	for {
		var waiting bool
		if err := pool.QueryRow(ctx, `
select exists (select 1 from pg_stat_activity
 where datname = current_database() and pid <> pg_backend_pid() and wait_event_type = 'Lock')`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting {
			break
		}
		select {
		case err := <-done:
			t.Fatalf("CreateChecked finished without waiting for the row lock: %v", err)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("CreateChecked never waited")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := tx.Exec(ctx, `update users set password_epoch = password_epoch + 1 where id = 1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := <-done; !errors.Is(err, session.ErrStale) {
		t.Errorf("after the change committed: %v, want ErrStale", err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select count(*) from sessions`).Scan(&n); err != nil || n != 0 {
		t.Errorf("%d sessions, %v", n, err)
	}
}

// TestLookupRefusesABannedUser covers SB-D1. A ban deletes the sessions of the
// member in its own transaction, and Lookup is the second guard: a ban written
// straight to the table, with no session deleted, makes the next request a guest.
func TestLookupRefusesABannedUser(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	ip := netip.MustParseAddr("10.1.2.3")
	token, _, err := session.Create(ctx, pool, 1, false, ip, "t", t0)
	if err != nil {
		t.Fatal(err)
	}
	otherToken, _, err := session.Create(ctx, pool, 2, false, ip, "t", t0)
	if err != nil {
		t.Fatal(err)
	}
	now := t0.Add(time.Minute)
	if _, err := session.Lookup(ctx, pool, token, now); err != nil {
		t.Fatalf("Lookup before the ban: %v", err)
	}
	ban := func(kind string, userID any, endsAt any) {
		t.Helper()
		if _, err := pool.Exec(ctx, `insert into bans (kind, user_id, ip, created_at, ends_at)
values ($1, $2, case when $1 = 'ip' then '10.1.2.3/32'::cidr end, $3, $4)`, kind, userID, t0, endsAt); err != nil {
			t.Fatal(err)
		}
	}
	// Bans that do not hold user 1: an address ban, a ban of another user, and a
	// user ban that has ended.
	ban("ip", nil, nil)
	ban("user", int64(2), nil)
	ban("user", int64(1), t0.Add(30*time.Second))
	if _, err := session.Lookup(ctx, pool, token, now); err != nil {
		t.Errorf("Lookup with bans that do not hold the user: %v", err)
	}
	// The ban of user 2 holds user 2 and only user 2.
	if _, err := session.Lookup(ctx, pool, otherToken, now); !errors.Is(err, session.ErrNone) {
		t.Errorf("Lookup of the banned user 2 = %v, want ErrNone", err)
	}
	// A user ban with no end holds user 1: no session is deleted, and the session
	// is dead all the same.
	ban("user", int64(1), nil)
	var before int
	if err := pool.QueryRow(ctx, `select count(*) from sessions where user_id = 1`).Scan(&before); err != nil || before != 1 {
		t.Fatalf("sessions of user 1 = %d (%v), want 1: the test writes no delete", before, err)
	}
	if _, err := session.Lookup(ctx, pool, token, now); !errors.Is(err, session.ErrNone) {
		t.Errorf("Lookup of a banned user = %v, want ErrNone", err)
	}
	// A ban that has a future end holds until then.
	if _, err := pool.Exec(ctx, `delete from bans where user_id = 1`); err != nil {
		t.Fatal(err)
	}
	ban("user", int64(1), now.Add(time.Hour))
	if _, err := session.Lookup(ctx, pool, token, now); !errors.Is(err, session.ErrNone) {
		t.Errorf("Lookup during a ban with an end = %v, want ErrNone", err)
	}
	if _, err := session.Lookup(ctx, pool, token, now.Add(2*time.Hour)); err != nil && !errors.Is(err, session.ErrNone) {
		t.Fatal(err)
	}
}

// TestLookupReadsTheBanIndex checks that the ban test of Lookup is an indexed
// lookup: with sequential scans off, the plan names the index of migration 0011.
func TestLookupReadsTheBanIndex(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	ctx := t.Context()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `set enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, `explain select 1 from bans b where b.kind = 'user' and b.user_id = $1 and (b.ends_at is null or b.ends_at > $2)`, int64(1), t0)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		plan.WriteString(line + "\n")
	}
	if !strings.Contains(plan.String(), "bans_user_idx") {
		t.Errorf("the plan does not use bans_user_idx:\n%s", plan.String())
	}
}
