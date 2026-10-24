package login

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"log/slog"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/password"
)

// The stored hashes of the charge tests. Each legacy one has a shape that
// VerifyLegacy accepts, so the tests charge hashes that a real check would run.
var (
	bcryptHash = "$2y$04$" + strings.Repeat("a", 53)
	chainHash  = "$chain$phpass,B12345678" + bcryptHash
)

// argonAt returns an argon2id hash that asks for m KiB, in the shape of the
// imported vectors (t=4, p=2). The tests never run it.
func argonAt(m int) string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=4,p=2$%s$%s", m,
		enc.EncodeToString([]byte("0123456789abcdef")), enc.EncodeToString(make([]byte, 32)))
}

// setHashes sets the stored hash of users 1 to 4 of the small board (alice, bob,
// carol and dave), as scheme and hash pairs.
func setHashes(t *testing.T, pool *pgxpool.Pool, h [4][2]string) {
	t.Helper()
	for i, sh := range h {
		_, err := pool.Exec(t.Context(),
			`update users set password_scheme = $2, password_hash = $3 where id = $1`, i+1, sh[0], sh[1])
		if err != nil {
			t.Fatal(err)
		}
	}
}

func TestChargeHashesAreRecognized(t *testing.T) {
	for _, h := range []string{bcryptHash, chainHash, argonAt(65536)} {
		if !password.Recognized("legacy", h) {
			t.Errorf("VerifyLegacy refuses the test hash %.20s…", h)
		}
	}
}

// The start-up step reads every stored hash once and sets the charge of every
// check to the cost of the most costly one, at least the cost of a new gobb hash.
// password.MemoryKiB decides the cost of each hash.
func TestLoadCharge(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	// A chain at the caps: the highest count (G, 2^18 rounds) over bcrypt at
	// cost 14. The inner layer of a chain that VerifyLegacy accepts is always a
	// bcrypt hash, so no accepted chain holds an argon2id layer.
	capChain := "$chain$phpass,G12345678$2y$14$" + strings.Repeat("a", 53)
	for _, c := range []struct {
		name   string
		hashes [4][2]string
		want   uint64
		// refused is true for a row whose hash VerifyLegacy refuses on purpose.
		refused bool
	}{
		{"bcrypt, chain and argon2id at 64 MiB",
			[4][2]string{{"legacy", bcryptHash}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", argonAt(65536)}},
			65536, false},
		{"no argon2id part",
			[4][2]string{{"legacy", bcryptHash}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}},
			password.HashMemoryKiB, false},
		{"gobb hashes at the current parameters",
			[4][2]string{{"gobb", argonAt(password.HashMemoryKiB)}, {"legacy", bcryptHash}, {"gobb", argonAt(password.HashMemoryKiB)}, {"legacy", ""}},
			password.HashMemoryKiB, false},
		{"a gobb hash above the current parameters",
			[4][2]string{{"gobb", argonAt(32768)}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}},
			32768, false},
		{"a chain at its caps costs its bcrypt layer",
			[4][2]string{{"legacy", bcryptHash}, {"legacy", capChain}, {"legacy", chainHash}, {"legacy", ""}},
			password.HashMemoryKiB, false},
		{"argon2id over the cap, which Verify refuses before any work",
			[4][2]string{{"legacy", argonAt(524288)}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}},
			password.HashMemoryKiB, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			for _, sh := range c.hashes {
				if !c.refused && !password.Recognized(sh[0], sh[1]) {
					t.Fatalf("VerifyLegacy refuses the test hash %.24s…", sh[1])
				}
			}
			setHashes(t, pool, c.hashes)
			s := NewWith(pool, Options{Gate: NewGate(DefaultBudgetKiB)})
			if err := s.LoadCharge(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := s.ChargeKiB(); got != c.want {
				t.Errorf("charge = %d KiB, want %d", got, c.want)
			}
		})
	}
}

// The start-up step logs one INFO line: the charge in KiB and how many checks
// fit in the gate at once. It logs no name and no hash. A hash above the gate
// budget adds one WARN line (TestLoadChargeWarnsAboveTheBudget).
func TestLoadChargeLogs(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, c := range []struct {
		m     int
		want  string
		lines int
	}{
		{0, fmt.Sprintf("kib=%d checks_at_once=6", password.HashMemoryKiB), 1},
		{65536, "kib=65536 checks_at_once=2", 1},
		{262144, "kib=262144 checks_at_once=1", 2},
	} {
		h := ""
		if c.m > 0 {
			h = argonAt(c.m)
		}
		setHashes(t, pool, [4][2]string{{"legacy", bcryptHash}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", h}})
		buf.Reset()
		s := NewWith(pool, Options{Gate: NewGate(DefaultBudgetKiB)})
		if err := s.LoadCharge(t.Context()); err != nil {
			t.Fatal(err)
		}
		out := buf.String()
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) != c.lines || !strings.Contains(lines[0], "level=INFO") || !strings.Contains(lines[0], c.want) {
			t.Errorf("m=%d: log = %q, want %d lines, the first an INFO line with %q", c.m, out, c.lines, c.want)
		}
		for _, secret := range []string{"alice", "bob", "carol", "dave", "$argon2id$", "$2y$", "$chain$"} {
			if strings.Contains(out, secret) {
				t.Errorf("m=%d: the log holds %q: %q", c.m, secret, out)
			}
		}
	}
}

// chargeSpy is a service on the small board whose Verify and rehash hash record
// what the gate holds while they run. Verify matches the password "right" and
// asks for a rehash when it matches.
type chargeSpy struct {
	s              *Service
	gate           *Gate
	verify, rehash uint64
}

func newChargeSpy(t *testing.T, pool *pgxpool.Pool) *chargeSpy {
	t.Helper()
	c := &chargeSpy{gate: NewGate(DefaultBudgetKiB)}
	used := func() uint64 {
		c.gate.mu.Lock()
		defer c.gate.mu.Unlock()
		return c.gate.used
	}
	c.s = NewWith(pool, Options{Gate: c.gate, Verify: func(raw, _, _ string) (bool, bool) {
		c.verify = used()
		return raw == "right", raw == "right"
	}})
	c.s.hash = func(string) (string, error) {
		c.rehash = used()
		return argonAt(password.HashMemoryKiB), nil
	}
	c.s.Floor = 0
	c.s.GateWait = time.Second
	return c
}

// check runs one sign-in and returns the charge that its Verify held.
func (c *chargeSpy) check(t *testing.T, name, pw string) uint64 {
	t.Helper()
	c.verify, c.rehash = 0, 0
	_, _ = c.s.Check(t.Context(), name, pw, netip.MustParseAddr("10.1.2.3"), time.Now())
	return c.verify
}

// Every check charges the gate the same, whatever the kind of the stored hash:
// the start-up charge. So under a full gate every name waits alike and ends
// alike. The rehash, which runs after a good password, holds the same charge.
//
// The legacy package has no md5 format of its own: VerifyLegacy reads bcrypt,
// argon2id and the chain, and an md5 password is a chain over the hex MD5. The
// chain row covers it.
func TestGateChargeIsUniform(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	gobb := argonAt(password.HashMemoryKiB)
	rows := func(c *chargeSpy) map[string]uint64 {
		got := map[string]uint64{
			"legacy bcrypt":           c.check(t, "bob", "wrong"),
			"legacy chain":            c.check(t, "carol", "wrong"),
			"gobb":                    c.check(t, "alice", "wrong"),
			"dave (argon2id or none)": c.check(t, "dave", "wrong"),
			"unknown name":            c.check(t, "nobody", "wrong"),
		}
		// Last: the rehash replaces bob's hash.
		c.check(t, "bob", "right")
		got["rehash"] = c.rehash
		return got
	}

	t.Run("a board with an imported argon2id hash at 64 MiB", func(t *testing.T) {
		setHashes(t, pool, [4][2]string{{"gobb", gobb}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", argonAt(65536)}})
		c := newChargeSpy(t, pool)
		if err := c.s.LoadCharge(t.Context()); err != nil {
			t.Fatal(err)
		}
		for name, got := range rows(c) {
			if got != 65536 {
				t.Errorf("%s charged %d KiB, want 65536", name, got)
			}
		}
		c.gate.mu.Lock()
		defer c.gate.mu.Unlock()
		if c.gate.used != 0 {
			t.Errorf("the gate holds %d KiB after every check ended", c.gate.used)
		}
	})

	t.Run("a board with no hash above the gobb cost", func(t *testing.T) {
		setHashes(t, pool, [4][2]string{{"gobb", gobb}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}})
		c := newChargeSpy(t, pool)
		if err := c.s.LoadCharge(t.Context()); err != nil {
			t.Fatal(err)
		}
		for name, got := range rows(c) {
			if got != password.HashMemoryKiB {
				t.Errorf("%s charged %d KiB, want %d", name, got, password.HashMemoryKiB)
			}
		}
	})

	// The one residual: a hash stored after the start, above the start-up
	// charge, charges its own cost.
	t.Run("a 64 MiB hash stored after the start", func(t *testing.T) {
		setHashes(t, pool, [4][2]string{{"gobb", gobb}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}})
		c := newChargeSpy(t, pool)
		if err := c.s.LoadCharge(t.Context()); err != nil {
			t.Fatal(err)
		}
		setHashes(t, pool, [4][2]string{{"gobb", gobb}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", argonAt(65536)}})
		if got := c.check(t, "dave", "wrong"); got != 65536 {
			t.Errorf("the new 64 MiB hash charged %d KiB, want 65536", got)
		}
		if got := c.check(t, "bob", "wrong"); got != password.HashMemoryKiB {
			t.Errorf("bcrypt charged %d KiB, want %d", got, password.HashMemoryKiB)
		}
	})
}
