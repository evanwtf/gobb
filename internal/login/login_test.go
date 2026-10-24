package login_test

import (
	"errors"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/password"
)

const pw = "example-password"

var (
	now = time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)
	ip  = netip.MustParseAddr("10.1.2.3")
)

func newService(t *testing.T) (*login.Service, *pgxpool.Pool) {
	t.Helper()
	pool, _ := boardtest.Load(t, bundletest.Small())
	return login.New(pool), pool
}

func exec(t *testing.T, pool *pgxpool.Pool, sql string, args ...any) {
	t.Helper()
	if _, err := pool.Exec(t.Context(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestCheck(t *testing.T) {
	svc, pool := newService(t)
	ctx := t.Context()

	check := func(name, pass string, from netip.Addr) (int64, error) {
		return svc.Check(ctx, name, pass, from, now)
	}
	wantID := func(name, pass string, want int64) {
		t.Helper()
		if id, err := check(name, pass, ip); err != nil || id != want {
			t.Errorf("Check(%q) = %d, %v; want %d", name, id, err, want)
		}
	}
	wantErr := func(name, pass string, want error) {
		t.Helper()
		if id, err := check(name, pass, ip); !errors.Is(err, want) || id != 0 {
			t.Errorf("Check(%q) = %d, %v; want %v", name, id, err, want)
		}
	}

	wantID("alice", pw, 1)
	wantID("  ALICE ", pw, 1) // the same name after cleaning
	wantID("ALICE@example.com", pw, 1)
	wantErr("alice", "wrong", login.ErrWrong)
	wantErr("alice", "", login.ErrWrong)
	wantErr("nobody", pw, login.ErrWrong)
	wantErr("", pw, login.ErrWrong)
	wantErr("\xff", pw, login.ErrWrong)
	wantErr(strings.Repeat("a", 5000), pw, login.ErrWrong)
	// dave is inactive and has no hash: he never matches, and the state does not show.
	wantErr("dave", pw, login.ErrWrong)
	wantErr("dave", "", login.ErrWrong)

	// bob has a legacy hash. The first sign-in rewrites it.
	wantID("bob", pw, 2)
	var hash, scheme string
	if err := pool.QueryRow(ctx, `select password_hash, password_scheme from users where id = 2`).Scan(&hash, &scheme); err != nil {
		t.Fatal(err)
	}
	if scheme != "gobb" {
		t.Errorf("scheme after the first sign-in = %q, want gobb", scheme)
	}
	if ok, rehash := password.Verify(pw, hash, "gobb"); !ok || rehash {
		t.Errorf("Verify of the stored hash = %v, %v; want true, false", ok, rehash)
	}
	wantID("bob", pw, 2)
	var again string
	if err := pool.QueryRow(ctx, `select password_hash from users where id = 2`).Scan(&again); err != nil || again != hash {
		t.Errorf("the second sign-in rewrote the hash (%v)", err)
	}
	wantErr("bob", "wrong", login.ErrWrong)

	// An ip ban holds the address, and says why.
	_, err := check("alice", pw, netip.MustParseAddr("10.99.1.2"))
	var banned *login.BannedError
	if !errors.As(err, &banned) || banned.Reason != "Proxies are not allowed" {
		t.Errorf("alice from a banned range: %v", err)
	}
	if _, err := check("alice", pw, netip.MustParseAddr("::ffff:10.99.1.2")); !errors.As(err, &banned) {
		t.Errorf("a mapped address in a banned range: %v", err)
	}
	if _, err := check("alice", pw, netip.MustParseAddr("10.98.1.2")); err != nil {
		t.Errorf("alice from outside the range: %v", err)
	}
	// A wrong password from a banned range is still just wrong.
	if _, err := check("alice", "wrong", netip.MustParseAddr("10.99.1.2")); !errors.Is(err, login.ErrWrong) {
		t.Errorf("wrong password from a banned range: %v", err)
	}

	// An email ban, written with spaces and capitals, holds carol.
	exec(t, pool, `insert into bans (kind, email, reason, reason_shown, created_at) values ('email', ' Carol@Example.COM', 'x', 'Spam address', $1)`, now.Add(-time.Hour))
	if _, err := check("carol", pw, ip); !errors.As(err, &banned) || banned.Reason != "Spam address" {
		t.Errorf("carol with an email ban: %v", err)
	}
	wantID("alice", pw, 1)
}

func TestCheckSharedEmail(t *testing.T) {
	svc, pool := newService(t)
	exec(t, pool, `update users set email = 'bob@example.com', email_clean = 'bob@example.com' where id = 3`)
	ctx := t.Context()
	if _, err := svc.Check(ctx, "bob@example.com", pw, ip, now); !errors.Is(err, login.ErrAmbiguous) {
		t.Errorf("shared email with a right password: %v, want ErrAmbiguous", err)
	}
	if _, err := svc.Check(ctx, "bob@example.com", "wrong", ip, now); !errors.Is(err, login.ErrWrong) {
		t.Errorf("shared email with a wrong password: %v, want ErrWrong", err)
	}
	// The username still works, and the legacy hash of bob was not rewritten by the ambiguous try.
	var scheme string
	if err := pool.QueryRow(ctx, `select password_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "legacy" {
		t.Errorf("bob's scheme = %q (%v) after an ambiguous sign-in, want legacy", scheme, err)
	}
	if id, err := svc.Check(ctx, "bob", pw, ip, now); err != nil || id != 2 {
		t.Errorf("bob by username = %d, %v", id, err)
	}
}

func TestCheckInactiveAfterMatch(t *testing.T) {
	svc, pool := newService(t)
	ctx := t.Context()
	hash, err := password.Hash("dave-password")
	if err != nil {
		t.Fatal(err)
	}
	exec(t, pool, `update users set password_hash = $1, password_scheme = 'gobb' where id = 4`, hash)
	if _, err := svc.Check(ctx, "dave", "dave-password", ip, now); !errors.Is(err, login.ErrInactive) {
		t.Errorf("dave with his password: %v, want ErrInactive", err)
	}
	if _, err := svc.Check(ctx, "dave", "wrong", ip, now); !errors.Is(err, login.ErrWrong) {
		t.Errorf("dave with a wrong password: %v, want ErrWrong", err)
	}

	// Active, his user ban in the fixture has ended and changes nothing.
	exec(t, pool, `update users set state = 'active' where id = 4`)
	if id, err := svc.Check(ctx, "dave", "dave-password", ip, now); err != nil || id != 4 {
		t.Errorf("dave with an ended ban: %d, %v", id, err)
	}
	// The same ban, still running, holds him.
	exec(t, pool, `update bans set ends_at = $1 where kind = 'user' and user_id = 4`, now.Add(time.Hour))
	var banned *login.BannedError
	if _, err := svc.Check(ctx, "dave", "dave-password", ip, now); !errors.As(err, &banned) || banned.Reason != "Rule 1" {
		t.Errorf("dave with a running ban: %v", err)
	}
	// A ban with no end holds him too.
	exec(t, pool, `update bans set ends_at = null where kind = 'user' and user_id = 4`)
	if _, err := svc.Check(ctx, "dave", "dave-password", ip, now); !errors.As(err, &banned) {
		t.Errorf("dave with a ban that never ends: %v", err)
	}
	// An email ban that cleans to nothing holds nobody, not even a member with no email.
	exec(t, pool, `delete from bans where kind = 'user'`)
	exec(t, pool, `insert into bans (kind, email, created_at) values ('email', '  ', $1)`, now.Add(-time.Hour))
	if id, err := svc.Check(ctx, "dave", "dave-password", ip, now); err != nil || id != 4 {
		t.Errorf("dave with a blank email ban: %d, %v", id, err)
	}
}
