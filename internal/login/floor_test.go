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
