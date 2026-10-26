package board_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/password"
)

// resetMail builds a reset email whose body holds the username and the token, so
// a test can read both back from the queue.
func resetMail(username, token string) (subject, body string) {
	return "Reset", "user=" + username + " token=" + token
}

func allowAll(int64) bool { return true }

func resetPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, _ := boardtest.Load(t, bundletest.Small())
	return pool
}

// queued is one row of the email queue as a reset test reads it.
type queued struct {
	To, User, Token string
	UserID          int64
	SendBy          *time.Time
}

func readQueue(t *testing.T, pool *pgxpool.Pool) []queued {
	t.Helper()
	rows, err := pool.Query(context.Background(), `select to_addr, body, user_id, send_by from email_queue order by id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []queued
	for rows.Next() {
		var q queued
		var body string
		var uid *int64
		if err := rows.Scan(&q.To, &body, &uid, &q.SendBy); err != nil {
			t.Fatal(err)
		}
		if uid != nil {
			q.UserID = *uid
		}
		fields := strings.Fields(body)
		if len(fields) != 2 {
			t.Fatalf("queue body %q has %d fields, want 2", body, len(fields))
		}
		q.User = strings.TrimPrefix(fields[0], "user=")
		q.Token = strings.TrimPrefix(fields[1], "token=")
		out = append(out, q)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func countRows(t *testing.T, pool *pgxpool.Pool, query string, args ...any) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func resetCount(t *testing.T, pool *pgxpool.Pool) int {
	return countRows(t, pool, `select count(*) from password_resets`)
}

// requestOne asks for a reset by name and returns the token in the one email it
// queued.
func requestOne(t *testing.T, pool *pgxpool.Pool, name string) string {
	t.Helper()
	n, err := board.RequestReset(context.Background(), pool, name, allowAll, resetMail, t0)
	if err != nil || n != 1 {
		t.Fatalf("RequestReset(%q) = %d, %v; want 1, nil", name, n, err)
	}
	q := readQueue(t, pool)
	return q[len(q)-1].Token
}

func tokenID(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

func TestRequestResetByNameOrEmail(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	for _, name := range []string{"Alice", " alice ", "ALICE@EXAMPLE.COM"} {
		n, err := board.RequestReset(ctx, pool, name, allowAll, resetMail, t0)
		if err != nil || n != 1 {
			t.Fatalf("RequestReset(%q) = %d, %v; want 1, nil", name, n, err)
		}
	}
	q := readQueue(t, pool)
	if len(q) != 3 {
		t.Fatalf("queue holds %d emails, want 3", len(q))
	}
	if got := resetCount(t, pool); got != 3 {
		t.Fatalf("%d links, want 3", got)
	}
	for _, m := range q {
		if m.To != "alice@example.com" || m.User != "alice" || m.UserID != alice {
			t.Errorf("email = %+v, want alice at alice@example.com", m)
		}
		if len(m.Token) != 43 {
			t.Errorf("token has %d characters, want 43", len(m.Token))
		}
		var uid int64
		var expires time.Time
		var epoch int64
		err := pool.QueryRow(ctx, `select user_id, expires_at, epoch from password_resets where id = $1`, tokenID(m.Token)).
			Scan(&uid, &expires, &epoch)
		if err != nil {
			t.Fatalf("no link row for the SHA-256 of the token: %v", err)
		}
		if uid != alice || epoch != 0 || !expires.Equal(t0.Add(time.Hour)) {
			t.Errorf("link = user %d, epoch %d, expires %v; want alice, 0, %v", uid, epoch, expires, t0.Add(time.Hour))
		}
		if m.SendBy == nil || !m.SendBy.Equal(expires) {
			t.Errorf("send_by = %v, want %v", m.SendBy, expires)
		}
	}
	if got := countRows(t, pool, `select count(*) from password_resets where id = any($1)`, []string{q[0].Token, q[1].Token, q[2].Token}); got != 0 {
		t.Errorf("%d link rows hold a raw token as the id, want 0", got)
	}
}

func TestRequestResetQuiet(t *testing.T) {
	ctx := context.Background()
	exec := func(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name      string
		ask       string
		setup     func(t *testing.T, pool *pgxpool.Pool)
		allow     bool
		wantAllow bool // the case reaches allow
	}{
		{name: "unknown name", ask: "nobody", allow: true},
		{name: "inactive member", ask: "carol", allow: true, setup: func(t *testing.T, p *pgxpool.Pool) {
			exec(t, p, `update users set state = 'inactive' where id = $1`, carol)
		}},
		{name: "user ban", ask: "carol", allow: true, setup: func(t *testing.T, p *pgxpool.Pool) {
			if err := board.BanUser(ctx, p, carol, "", t0); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "email ban", ask: "carol", allow: true, setup: func(t *testing.T, p *pgxpool.Pool) {
			exec(t, p, `insert into bans (kind, email, created_at) values ('email', ' CAROL@Example.com ', $1)`, t0)
		}},
		{name: "empty address", ask: "carol", allow: true, setup: func(t *testing.T, p *pgxpool.Pool) {
			exec(t, p, `update users set email = '', email_clean = '' where id = $1`, carol)
		}},
		{name: "non-ASCII address", ask: "carol", allow: true, setup: func(t *testing.T, p *pgxpool.Pool) {
			exec(t, p, `update users set email = 'jörg@example.com', email_clean = 'jörg@example.com' where id = $1`, carol)
		}},
		{name: "no account.password", ask: "carol", allow: true, setup: func(t *testing.T, p *pgxpool.Pool) {
			denyOpts(t, p, carol, 0, "account.password")
		}},
		{name: "allow false", ask: "carol", allow: false, wantAllow: true},
		{name: "name of 513 bytes", ask: strings.Repeat("a", 513), allow: true},
		{name: "name with a NUL", ask: "car\x00ol", allow: true},
		{name: "empty name", ask: " \t ", allow: true},
		{name: "name that is not UTF-8", ask: "car\xffol", allow: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			pool := resetPool(t)
			if c.setup != nil {
				c.setup(t, pool)
			}
			calls := 0
			allow := func(int64) bool { calls++; return c.allow }
			n, err := board.RequestReset(ctx, pool, c.ask, allow, resetMail, t0)
			if err != nil || n != 0 {
				t.Errorf("RequestReset = %d, %v; want 0, nil", n, err)
			}
			if got := len(readQueue(t, pool)); got != 0 {
				t.Errorf("queue holds %d emails, want 0", got)
			}
			if got := resetCount(t, pool); got != 0 {
				t.Errorf("%d links, want 0", got)
			}
			if c.wantAllow != (calls > 0) {
				t.Errorf("allow called %d times, reached = %v", calls, c.wantAllow)
			}
		})
	}
}

func TestRequestResetIgnoresAddressBan(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	if _, err := pool.Exec(ctx, `insert into bans (kind, ip, created_at) values ('ip', '10.1.2.3/32', $1)`, t0); err != nil {
		t.Fatal(err)
	}
	if got := requestOne(t, pool, "alice"); got == "" {
		t.Error("no token")
	}
}

func TestRequestResetSharedAddress(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	if _, err := pool.Exec(ctx, `update users set email = 'alice@example.com', email_clean = 'alice@example.com' where id = $1`, bob); err != nil {
		t.Fatal(err)
	}
	n, err := board.RequestReset(ctx, pool, "alice@example.com", allowAll, resetMail, t0)
	if err != nil || n != 2 {
		t.Fatalf("RequestReset = %d, %v; want 2, nil", n, err)
	}
	q := readQueue(t, pool)
	if len(q) != 2 || q[0].User != "alice" || q[1].User != "bob" || q[0].Token == q[1].Token {
		t.Fatalf("queue = %+v, want one email each for alice and bob with different tokens", q)
	}
	bobBefore := readUser(t, pool, bob)
	if _, err := board.CompleteReset(ctx, pool, q[0].Token, "a brand new password", nil, t0.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if got := readUser(t, pool, bob); got.Hash != bobBefore.Hash {
		t.Error("the first token changed bob's password")
	}
	if got := countRows(t, pool, `select count(*) from password_resets where user_id = $1`, bob); got != 1 {
		t.Errorf("bob has %d links, want 1", got)
	}
	if _, err := board.ResetLink(ctx, pool, q[1].Token, t0.Add(time.Minute)); err != nil {
		t.Errorf("bob's link = %v, want live", err)
	}
}

func TestResetLinkRules(t *testing.T) {
	ctx := context.Background()
	soon := t0.Add(30 * time.Minute)

	t.Run("live", func(t *testing.T) {
		pool := resetPool(t)
		token := requestOne(t, pool, "alice")
		got, err := board.ResetLink(ctx, pool, token, soon)
		if err != nil || got.UserID != alice || got.Username != "alice" {
			t.Fatalf("ResetLink = %+v, %v; want alice", got, err)
		}
		if _, err := board.ResetLink(ctx, pool, token, soon); err != nil {
			t.Errorf("a second look = %v: ResetLink must write nothing", err)
		}
		if got := resetCount(t, pool); got != 1 {
			t.Errorf("%d links after two looks, want 1", got)
		}
	})

	dead := []struct {
		name  string
		setup func(t *testing.T, pool *pgxpool.Pool)
		at    time.Time
		mod   func(string) string
	}{
		{name: "1 h later", at: t0.Add(time.Hour)},
		{name: "1 h and 1 s later", at: t0.Add(time.Hour + time.Second)},
		{name: "42 characters", at: soon, mod: func(s string) string { return s[:42] }},
		{name: "44 characters", at: soon, mod: func(s string) string { return s + "A" }},
		{name: "a character outside base64url", at: soon, mod: func(s string) string { return s[:42] + "=" }},
		{name: "an unknown token", at: soon, mod: func(string) string { return strings.Repeat("A", 43) }},
		{name: "after a password change", at: soon, setup: func(t *testing.T, p *pgxpool.Pool) {
			if err := board.ChangePassword(ctx, p, viewerFor(t, p, alice), "a brand new password", t0); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "after an administrator's reset", at: soon, setup: func(t *testing.T, p *pgxpool.Pool) {
			if _, err := board.ResetPassword(ctx, p, alice, t0); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "after a user ban", at: soon, setup: func(t *testing.T, p *pgxpool.Pool) {
			if err := board.BanUser(ctx, p, alice, "", t0); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "inactive account", at: soon, setup: func(t *testing.T, p *pgxpool.Pool) {
			if _, err := p.Exec(ctx, `update users set state = 'inactive' where id = $1`, alice); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "no account.password", at: soon, setup: func(t *testing.T, p *pgxpool.Pool) {
			denyOpts(t, p, alice, 0, "account.password")
		}},
	}
	for _, c := range dead {
		t.Run(c.name, func(t *testing.T) {
			pool := resetPool(t)
			token := requestOne(t, pool, "alice")
			if c.setup != nil {
				c.setup(t, pool)
			}
			if c.mod != nil {
				token = c.mod(token)
			}
			_, err := board.ResetLink(ctx, pool, token, c.at)
			wantIs(t, err, board.ErrNotFound, "ResetLink")
		})
	}
}

func TestCompleteReset(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	if _, err := pool.Exec(ctx, `update users set email_verified = false where id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	token := requestOne(t, pool, "alice")
	other := requestOne(t, pool, "alice")
	for range 2 {
		if _, _, err := boardtest.Session(ctx, pool, alice, false, ip1, "t", t0); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := boardtest.Session(ctx, pool, bob, false, ip1, "t", t0); err != nil {
		t.Fatal(err)
	}
	before := readUser(t, pool, alice)

	var hold int
	holder := func(context.Context) (func(), error) { hold++; return func() { hold-- }, nil }
	id, err := board.CompleteReset(ctx, pool, token, "a brand new password", holder, t0.Add(5*time.Minute))
	if err != nil || id != alice {
		t.Fatalf("CompleteReset = %d, %v; want alice", id, err)
	}
	if hold != 0 {
		t.Errorf("hold count = %d after the call, want 0 (the share is released)", hold)
	}
	after := readUser(t, pool, alice)
	if after.Hash == before.Hash || after.Scheme != "gobb" || !after.Verified {
		t.Errorf("hash changed = %v, scheme = %q, verified = %v", after.Hash != before.Hash, after.Scheme, after.Verified)
	}
	if ok, _ := password.Verify("a brand new password", after.Hash, after.Scheme); !ok {
		t.Error("the stored hash does not verify the new password")
	}
	if got := countRows(t, pool, `select password_epoch from users where id = $1`, alice); got != 1 {
		t.Errorf("epoch = %d, want 1", got)
	}
	if n := sessionCount(t, pool, alice); n != 0 {
		t.Errorf("alice has %d sessions, want 0", n)
	}
	if n := sessionCount(t, pool, bob); n != 1 {
		t.Errorf("bob has %d sessions, want 1", n)
	}
	if got := countRows(t, pool, `select count(*) from password_resets where user_id = $1`, alice); got != 0 {
		t.Errorf("alice has %d links, want 0", got)
	}
	_, err = board.ResetLink(ctx, pool, other, t0.Add(6*time.Minute))
	wantIs(t, err, board.ErrNotFound, "the other link")

	var actor, target *int64
	var targetType, details string
	var n int
	rows, err := pool.Query(ctx, `select actor_id, target_type, target_id, details::text from mod_log where action = 'user.reset_password'`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		n++
		if err := rows.Scan(&actor, &targetType, &target, &details); err != nil {
			t.Fatal(err)
		}
	}
	if n != 1 || actor == nil || *actor != alice || target == nil || *target != alice || targetType != "user" {
		t.Fatalf("mod_log: %d rows, actor %v, target %v (%s); want one row, alice, alice, user", n, actor, target, targetType)
	}
	if got := countRows(t, pool, `select count(*) from mod_log where action = 'user.reset_password' and details = '{"via":"email"}'::jsonb`); got != 1 {
		t.Errorf("details = %s, want {\"via\":\"email\"}", details)
	}
	for _, secret := range []string{token, other, "a brand new password"} {
		if got := countRows(t, pool, `select count(*) from mod_log where details::text like '%' || $1 || '%'`, secret); got != 0 {
			t.Error("a secret is in the mod_log")
		}
	}
}

func TestCompleteResetSingleUse(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	token := requestOne(t, pool, "alice")
	if _, err := board.CompleteReset(ctx, pool, token, "the first password", nil, t0); err != nil {
		t.Fatal(err)
	}
	_, err := board.CompleteReset(ctx, pool, token, "the second password", nil, t0)
	wantIs(t, err, board.ErrNotFound, "second CompleteReset")
	u := readUser(t, pool, alice)
	if ok, _ := password.Verify("the first password", u.Hash, u.Scheme); !ok {
		t.Error("the password is not the first one")
	}
	if got := countRows(t, pool, `select count(*) from mod_log where action = 'user.reset_password'`); got != 1 {
		t.Errorf("%d mod_log rows, want 1", got)
	}
}

func TestCompleteResetRefusesWeak(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	// The default username is shorter than the minimum, so the length rule
	// would fire first. A longer name tests the username rule.
	if _, err := pool.Exec(ctx, `update users set username = 'aliceexample', username_clean = 'aliceexample' where id = $1`, alice); err != nil {
		t.Fatal(err)
	}
	token := requestOne(t, pool, "aliceexample")
	_, err := board.CompleteReset(ctx, pool, token, "short1234", nil, t0)
	wantInput(t, err, "next", "Use at least 10 characters.")
	if _, err := board.ResetLink(ctx, pool, token, t0); err != nil {
		t.Errorf("link after a short password = %v, want live", err)
	}
	_, err = board.CompleteReset(ctx, pool, token, "AliceExample", nil, t0)
	wantInput(t, err, "next", "Choose a password that differs from your username.")
	if _, err := board.ResetLink(ctx, pool, token, t0); err != nil {
		t.Errorf("link after the username = %v, want live", err)
	}
	if _, err := board.CompleteReset(ctx, pool, token, "a long enough one", nil, t0); err != nil {
		t.Errorf("a good password = %v", err)
	}
}

func TestCompleteResetRace(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	token := requestOne(t, pool, "alice")
	hold := func(context.Context) (func(), error) {
		if err := board.ChangePassword(ctx, pool, viewerFor(t, pool, alice), "the account page password", t0); err != nil {
			t.Error(err)
		}
		return func() {}, nil
	}
	_, err := board.CompleteReset(ctx, pool, token, "the reset password", hold, t0)
	wantIs(t, err, board.ErrNotFound, "CompleteReset")
	u := readUser(t, pool, alice)
	if ok, _ := password.Verify("the account page password", u.Hash, u.Scheme); !ok {
		t.Error("the account page's password does not stand")
	}
	if got := countRows(t, pool, `select count(*) from mod_log where action = 'user.reset_password'`); got != 0 {
		t.Errorf("%d reset rows in mod_log, want 0", got)
	}
}

func TestCompleteResetHoldError(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	token := requestOne(t, pool, "alice")
	boom := errors.New("gate closed")
	_, err := board.CompleteReset(ctx, pool, token, "a brand new password", func(context.Context) (func(), error) { return nil, boom }, t0)
	wantIs(t, err, boom, "CompleteReset")
	if _, err := board.ResetLink(ctx, pool, token, t0); err != nil {
		t.Errorf("link after a hold error = %v, want live", err)
	}
}

func TestEmailChangeEndsResetLinks(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	token := requestOne(t, pool, "alice")
	a := viewerFor(t, pool, alice)
	if err := board.ChangeEmailWith(ctx, pool, a, "alice@example.com", 0, t0); err != nil {
		t.Fatal(err)
	}
	if _, err := board.ResetLink(ctx, pool, token, t0); err != nil {
		t.Errorf("link after a change to the same address = %v, want live", err)
	}
	if err := board.ChangeEmailWith(ctx, pool, a, "alice.new@example.com", 0, t0); err != nil {
		t.Fatal(err)
	}
	_, err := board.ResetLink(ctx, pool, token, t0)
	wantIs(t, err, board.ErrNotFound, "link after a new address")
	if got := resetCount(t, pool); got != 0 {
		t.Errorf("%d links left, want 0", got)
	}
}

func TestCleanupResets(t *testing.T) {
	ctx := context.Background()
	pool := resetPool(t)
	for i, exp := range []time.Time{t0.Add(-time.Second), t0, t0.Add(time.Second)} {
		if _, err := pool.Exec(ctx,
			`insert into password_resets (id, user_id, epoch, created_at, expires_at) values ($1, $2, 0, $3, $4)`,
			string(rune('a'+i)), alice, t0.Add(-time.Hour), exp); err != nil {
			t.Fatal(err)
		}
	}
	n, err := board.CleanupResets(ctx, pool, t0)
	if err != nil || n != 2 {
		t.Fatalf("CleanupResets = %d, %v; want 2 (a link at its expiry is dead)", n, err)
	}
	if got := countRows(t, pool, `select count(*) from password_resets where id = 'c'`); got != 1 {
		t.Error("the live link is gone")
	}
}
