package login

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/password"
)

// A hash check charges the gate the same for every kind of stored hash, so the
// wait for the gate does not tell a stranger the kind of hash. Only a stored hash
// above the current parameters charges more.
func TestGateChargeIsUniform(t *testing.T) {
	gobb, err := password.Hash("x")
	if err != nil {
		t.Fatal(err)
	}
	bcrypt := "$2y$04$" + strings.Repeat("a", 53)
	chain := "$chain$phpass,$P$B12345678" + bcrypt
	heavy := "$argon2id$v=19$m=65536,t=1,p=1$MDEyMzQ1Njc4OWFiY2RlZg$" + strings.Repeat("A", 43)

	g := NewGate(1 << 20)
	var charged uint64
	s := NewWith(nil, Options{Gate: g, Verify: func(string, string, string) (bool, bool) {
		g.mu.Lock()
		charged = g.used
		g.mu.Unlock()
		return false, false
	}})
	s.GateWait = time.Second

	run := func(f func()) uint64 {
		charged = 0
		f()
		return charged
	}
	ctx := context.Background()
	want := uint64(password.HashMemoryKiB)
	for name, got := range map[string]uint64{
		"legacy bcrypt": run(func() { _, _, _ = s.verify(ctx, "p", bcrypt, "legacy") }),
		"legacy chain":  run(func() { _, _, _ = s.verify(ctx, "p", chain, "legacy") }),
		"legacy md5":    run(func() { _, _, _ = s.verify(ctx, "p", "md5:abc", "legacy") }),
		"gobb":          run(func() { _, _, _ = s.verify(ctx, "p", gobb, "gobb") }),
		"unknown name":  run(func() { _ = s.burn(ctx, "p") }),
		"rehash":        chargeKiB("gobb", ""),
	} {
		if got != want {
			t.Errorf("%s charged %d KiB, want %d", name, got, want)
		}
	}
	// A stored hash above the current parameters charges its own cost.
	if got := run(func() { _, _, _ = s.verify(ctx, "p", heavy, "legacy") }); got != 65536 {
		t.Errorf("a 64 MiB hash charged %d KiB, want 65536", got)
	}
	if g.used != 0 {
		t.Errorf("the gate holds %d KiB after every check ended", g.used)
	}
}
