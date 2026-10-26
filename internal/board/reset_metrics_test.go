package board_test

import (
	"context"
	"testing"

	"github.com/evanwtf/gobb/internal/board"
	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/metrics"
)

// A reset request that reaches an account and sends no email counts the reason:
// admin for an administrator, ineligible for an inactive account. An account
// that allow refuses counts neither: the caller counts the limit that refused.
func TestRequestResetCountsSkips(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small()) // alice is an administrator, dave is inactive
	admin, ineligible := metrics.ResetSkipped.Value("admin"), metrics.ResetSkipped.Value("ineligible")
	for _, name := range []string{"alice", "dave"} {
		if n, err := board.RequestReset(ctx, pool, name, allowAll, resetMail, t0); err != nil || n != 0 {
			t.Fatalf("RequestReset(%q) = %d, %v; want 0, nil", name, n, err)
		}
	}
	if n, err := board.RequestReset(ctx, pool, "bob", func(int64) bool { return false }, resetMail, t0); err != nil || n != 0 {
		t.Fatalf("RequestReset(bob) with allow refusing = %d, %v", n, err)
	}
	if got := metrics.ResetSkipped.Value("admin") - admin; got != 1 {
		t.Errorf("admin skips = %d, want 1", got)
	}
	if got := metrics.ResetSkipped.Value("ineligible") - ineligible; got != 1 {
		t.Errorf("ineligible skips = %d, want 1 (dave; bob's refusal belongs to the caller)", got)
	}
	if n, err := board.RequestReset(ctx, pool, "bob", allowAll, resetMail, t0); err != nil || n != 1 {
		t.Errorf("RequestReset(bob) = %d, %v; want 1 (bob is eligible)", n, err)
	}
}
