package login_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/internal/session"
)

// Two sign-ins at once on one legacy hash (a double click, or a phone and a
// laptop), both with the right password: both succeed and each gets a session.
// Both read the legacy hash before either rehash commits, so one rehash finds the
// row changed. That is not a password change, and it must not refuse the sign-in.
// At cutover every member holds a legacy hash, so each first sign-in meets this.
func TestTwoSignInsOnALegacyHashBothGetASession(t *testing.T) {
	_, pool := newService(t)
	var arrived atomic.Int32
	both := make(chan struct{})
	opt := login.DefaultOptions()
	opt.Verify = func(raw, stored, scheme string) (bool, bool) {
		// Hold each check until the other one has read the same hash.
		if arrived.Add(1) == 2 {
			close(both)
		}
		select {
		case <-both:
		case <-time.After(10 * time.Second):
		}
		return password.Verify(raw, stored, scheme)
	}
	svc := login.NewWith(pool, opt)
	svc.Floor = 0

	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = svc.CheckWith(t.Context(), "bob", pw, ip, now, func(ctx context.Context, id, epoch int64) error {
				_, _, err := session.CreateChecked(ctx, pool, id, epoch, false, ip, "ua", now)
				if errors.Is(err, session.ErrStale) {
					return login.ErrStale
				}
				return err
			})
		}()
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Errorf("sign-in %d: %v, want success", i, err)
		}
	}
	var n int
	if err := pool.QueryRow(t.Context(), `select count(*) from sessions where user_id = 2`).Scan(&n); err != nil || n != 2 {
		t.Errorf("bob has %d sessions, %v, want 2", n, err)
	}
	var scheme string
	if err := pool.QueryRow(t.Context(), `select password_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "gobb" {
		t.Errorf("scheme after the sign-ins = %q, %v, want gobb", scheme, err)
	}
}

// A sign-in never changes the epoch, not even when it replaces a legacy hash:
// the epoch counts changes of the password, not of its storage form.
func TestRehashLeavesThePasswordEpoch(t *testing.T) {
	svc, pool := newService(t)
	svc.Floor = 0
	epoch := func() int64 {
		var e int64
		if err := pool.QueryRow(t.Context(), `select password_epoch from users where id = 2`).Scan(&e); err != nil {
			t.Fatal(err)
		}
		return e
	}
	before := epoch()
	if _, err := svc.Check(t.Context(), "bob", pw, ip, now); err != nil {
		t.Fatal(err)
	}
	var scheme string
	if err := pool.QueryRow(t.Context(), `select password_scheme from users where id = 2`).Scan(&scheme); err != nil || scheme != "gobb" {
		t.Fatalf("the hash was not replaced: %q, %v", scheme, err)
	}
	if after := epoch(); after != before {
		t.Errorf("epoch = %d after a rehash, want %d", after, before)
	}
}
