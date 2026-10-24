package login

import (
	"bytes"
	"log/slog"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/password"
)

// shareEmails gives users 1 to n of the small board one email address, and
// every other user an address of its own. n of 0 or 1 shares nothing.
func shareEmails(t *testing.T, pool *pgxpool.Pool, n int) {
	t.Helper()
	_, err := pool.Exec(t.Context(), `update users set email = case
  when id <= $1 and $1 > 1 then 'shared@example.com'
  else username_clean || '@example.com' end`, n)
	if err == nil {
		_, err = pool.Exec(t.Context(), `update users set email_clean = email`)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// The cost shapes of the test hashes.
const (
	shapeDummy  = "gobb argon2id m=19456,t=2,p=1"
	shapeHeavy  = "legacy argon2id m=65536,t=4,p=2"
	shapeBcrypt = "legacy bcrypt 04"
	shapeChain  = "legacy chain B bcrypt 04"
)

// timing returns a hook that says a check of the cost shape slowShape takes
// slow and every other check takes fast. It records the shape of each call.
func timing(slowShape string, slow, fast time.Duration, shapes *[]string) func(stored, scheme string) time.Duration {
	return func(stored, scheme string) time.Duration {
		shape := password.CostShape(scheme, stored)
		*shapes = append(*shapes, shape)
		if shape == slowShape {
			return slow
		}
		return fast
	}
}

// The floor at start is max(DefaultFloor, 2 × K × T), rounded up to a whole
// 50 ms. T is the slowest single check over the cost shapes of the stored hashes
// and the dummy check. K is the most accounts that share one email address.
func TestLoadFloor(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	// alice's hash has the gobb parameters, so it shares the dummy check's shape.
	gobb := strings.Replace(argonAt(19456), "t=4,p=2", "t=2,p=1", 1)
	heavy := [4][2]string{{"gobb", gobb}, {"legacy", bcryptHash}, {"legacy", argonAt(65536)}, {"legacy", ""}}
	light := [4][2]string{{"legacy", bcryptHash}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}}
	ms := time.Millisecond
	for _, c := range []struct {
		name       string
		hashes     [4][2]string
		shared     int
		slowShape  string
		slow, fast time.Duration
		want       time.Duration
		shapes     []string
	}{
		{"one shape at 300 ms and three accounts on one email", heavy, 3, shapeHeavy, 300 * ms, 10 * ms,
			1800 * ms, []string{shapeDummy, shapeHeavy, shapeBcrypt}},
		{"every shape fast and no shared email", heavy, 1, shapeHeavy, 10 * ms, 10 * ms,
			DefaultFloor, nil},
		{"no gobb hash stored: the dummy check is still a shape", light, 1, "", 0, 10 * ms,
			DefaultFloor, []string{shapeDummy, shapeBcrypt, shapeChain}},
		{"the dummy check is the slowest", light, 1, shapeDummy, 260 * ms, 10 * ms, 550 * ms, nil},
		{"K = 1", heavy, 1, shapeHeavy, 250 * ms, 10 * ms, 500 * ms, nil},
		{"K = 2", heavy, 2, shapeHeavy, 250 * ms, 10 * ms, 1000 * ms, nil},
		{"K = 3", heavy, 3, shapeHeavy, 250 * ms, 10 * ms, 1500 * ms, nil},
		{"rounded up to 50 ms", heavy, 1, shapeHeavy, 230 * ms, 10 * ms, 500 * ms, nil},
	} {
		t.Run(c.name, func(t *testing.T) {
			setHashes(t, pool, c.hashes)
			shareEmails(t, pool, c.shared)
			s := NewWith(pool, Options{Gate: NewGate(DefaultBudgetKiB)})
			var shapes []string
			s.timeVerify = timing(c.slowShape, c.slow, c.fast, &shapes)
			if err := s.LoadFloor(t.Context()); err != nil {
				t.Fatal(err)
			}
			if s.Floor != c.want {
				t.Errorf("floor = %v, want %v", s.Floor, c.want)
			}
			if c.shapes != nil {
				slices.Sort(shapes)
				if !slices.Equal(shapes, c.shapes) {
					t.Errorf("timed shapes = %q, want %q (each once)", shapes, c.shapes)
				}
			}
		})
	}
}

// The start-up step logs one INFO line: the floor in ms, T in ms and K. It
// logs no name and no hash.
func TestLoadFloorLogs(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	setHashes(t, pool, [4][2]string{{"gobb", argonAt(19456)}, {"legacy", bcryptHash}, {"legacy", argonAt(65536)}, {"legacy", ""}})
	shareEmails(t, pool, 3)
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	s := NewWith(pool, Options{})
	var shapes []string
	s.timeVerify = timing(shapeHeavy, 300*time.Millisecond, 10*time.Millisecond, &shapes)
	if err := s.LoadFloor(t.Context()); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], "level=INFO") ||
		!strings.Contains(lines[0], "floor_ms=1800 t_ms=300 k=3") {
		t.Errorf("log = %q, want one INFO line with floor_ms=1800 t_ms=300 k=3", out)
	}
	for _, secret := range []string{"alice", "bob", "carol", "dave", "example.com", "$argon2id$", "$2y$", "m=65536"} {
		if strings.Contains(out, secret) {
			t.Errorf("the log holds %q: %q", secret, out)
		}
	}
}

// The start-up step times each check with a wrong password of 4,096 bytes, the
// longest that Verify reads. A chain check grows with the length of the
// password, and a stranger chooses the length. The probe has a legacy form of
// the same length, so VerifyLegacy runs the whole check.
func TestLoadFloorProbeIsLong(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	setHashes(t, pool, [4][2]string{{"legacy", chainHash}, {"legacy", bcryptHash}, {"legacy", argonAt(65536)}, {"legacy", ""}})
	var probes []string
	s := NewWith(pool, Options{Gate: NewGate(DefaultBudgetKiB), Verify: func(raw, _, _ string) (bool, bool) {
		probes = append(probes, raw)
		return false, false
	}})
	if err := s.LoadFloor(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(probes) == 0 {
		t.Fatal("LoadFloor timed no check")
	}
	for _, raw := range probes {
		form, ok := password.LegacyForm(raw)
		if len(raw) != 4096 || !ok || len(form) != 4096 {
			t.Errorf("probe: %d bytes, legacy form %d bytes (ok %v), want 4096 and 4096", len(raw), len(form), ok)
		}
	}
}

// Each timing check at start holds the gate, with the charge of a check of
// that hash (chargeKiB), and gives it back before the next one. Each release
// collects. After the last release, the step runs one more garbage collection,
// so the memory of the checks (and of the dummy hash) does not sit under the
// first sign-ins. The test counts the forced collections of the step: one per
// release, and one after the last.
func TestLoadFloorHoldsTheGate(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	gobb := strings.Replace(argonAt(19456), "t=4,p=2", "t=2,p=1", 1)
	setHashes(t, pool, [4][2]string{{"gobb", gobb}, {"legacy", bcryptHash}, {"legacy", argonAt(65536)}, {"legacy", ""}})
	gate := NewGate(DefaultBudgetKiB)
	s := NewWith(pool, Options{Gate: gate})
	if err := s.LoadCharge(t.Context()); err != nil {
		t.Fatal(err)
	}
	var holds []uint64
	s.onAcquire = func(kib uint64) { holds = append(holds, kib) }
	var held []uint64
	s.timeVerify = func(string, string) time.Duration {
		gate.mu.Lock()
		held = append(held, gate.used)
		gate.mu.Unlock()
		return time.Millisecond
	}
	before := forcedGCs()
	if err := s.LoadFloor(t.Context()); err != nil {
		t.Fatal(err)
	}
	after := forcedGCs()
	// Three shapes: the dummy check (alice's hash shares it), bcrypt and argon2id.
	want := []uint64{65536, 65536, 65536}
	if !slices.Equal(holds, want) {
		t.Errorf("gate holds = %v, want %v (one per timed shape, at the charge)", holds, want)
	}
	if !slices.Equal(held, want) {
		t.Errorf("the gate held %v KiB while each check ran, want %v", held, want)
	}
	gate.mu.Lock()
	used := gate.used
	gate.mu.Unlock()
	if used != 0 {
		t.Errorf("the gate holds %d KiB after the step", used)
	}
	if got, wantGCs := after-before, uint32(len(want))+1; got < wantGCs {
		t.Errorf("forced collections in the step: %d, want %d (one per release and one after the last check)", got, wantGCs)
	}
}

// The gate budget at start follows the memory limit of the process: 128 MiB,
// or the limit less 48 MiB for the rest of the server when that is less. A
// budget never falls below 1 MiB, so the gate always runs at least one check.
func TestBudgetKiB(t *testing.T) {
	const mib = 1 << 20
	for _, c := range []struct {
		limit int64
		want  uint64
	}{
		{0, DefaultBudgetKiB},
		{-1, DefaultBudgetKiB},
		{1 << 40, DefaultBudgetKiB},
		{176 * mib, 128 * 1024},
		{179 * mib, 128 * 1024},   // 70% of a 256 MiB cap is 179.2 MiB
		{150 * mib, 102 * 1024},   // 150 − 48
		{100 * mib, 52 * 1024},    // 100 − 48
		{48 * mib, 1024},          // nothing left: the least budget
		{10 * mib, 1024},          // below the headroom
		{49*mib + 512*1024, 1536}, // 1.5 MiB left
	} {
		if got := BudgetKiB(c.limit); got != c.want {
			t.Errorf("BudgetKiB(%d) = %d, want %d", c.limit, got, c.want)
		}
	}
}

// A stored hash that costs more than the gate budget gets one WARN line at
// start, with the number of such accounts and no name. Such a check still runs:
// Acquire clamps its cost to the budget, so it runs alone.
func TestLoadChargeWarnsAboveTheBudget(t *testing.T) {
	pool, _ := boardtest.Load(t, bundletest.Small())
	var buf bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(old) })
	for _, c := range []struct {
		name   string
		budget uint64
		hashes [4][2]string
		want   string // "" for no WARN line
	}{
		{"two accounts above a 64 MiB budget", 65536,
			[4][2]string{{"legacy", argonAt(131072)}, {"legacy", bcryptHash}, {"legacy", argonAt(262144)}, {"legacy", argonAt(65536)}},
			"accounts=2 budget_kib=65536"},
		{"one account above the default budget", DefaultBudgetKiB,
			[4][2]string{{"legacy", argonAt(262144)}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}},
			"accounts=1 budget_kib=131072"},
		{"a hash equal to the budget", 65536,
			[4][2]string{{"legacy", argonAt(65536)}, {"legacy", bcryptHash}, {"legacy", chainHash}, {"legacy", ""}},
			""},
	} {
		t.Run(c.name, func(t *testing.T) {
			setHashes(t, pool, c.hashes)
			buf.Reset()
			s := NewWith(pool, Options{Gate: NewGate(c.budget)})
			if err := s.LoadCharge(t.Context()); err != nil {
				t.Fatal(err)
			}
			out := buf.String()
			var warns []string
			for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
				if strings.Contains(line, "level=WARN") {
					warns = append(warns, line)
				}
			}
			switch {
			case c.want == "" && len(warns) != 0:
				t.Errorf("WARN lines %q, want none", warns)
			case c.want != "" && (len(warns) != 1 || !strings.Contains(warns[0], c.want)):
				t.Errorf("WARN lines %q, want one with %q", warns, c.want)
			}
			for _, secret := range []string{"alice", "bob", "carol", "dave", "$argon2id$", "$2y$", "$chain$"} {
				if strings.Contains(out, secret) {
					t.Errorf("the log holds %q: %q", secret, out)
				}
			}
		})
	}
}
