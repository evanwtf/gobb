// Package boardtest gives the tests of the read side a database with a board in
// it. The board comes from package bundletest, so every name and post is made up.
package boardtest

import (
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/importer"
	"github.com/evanwtf/gobb/internal/session"
)

// Load creates a test database, imports b into it, and returns the pool and the
// data directory that the import filled. The test is skipped when
// GOBB_TEST_DATABASE_URL is unset, and it fails when the import fails. The
// database and the directory go away when the test ends.
func Load(t testing.TB, b *bundletest.Board) (pool *pgxpool.Pool, dataDir string) {
	t.Helper()
	pool = dbtest.New(t)
	dir := bundletest.Write(t, b)
	dataDir = t.TempDir()
	if _, err := importer.Run(context.Background(), pool, dir, importer.Options{DataDir: dataDir}); err != nil {
		t.Fatalf("boardtest: import: %v", err)
	}
	return pool, dataDir
}

// Session starts a session for userID without a sign-in, for a test that needs a
// session to exist. It reads the password epoch of the user and calls
// [session.CreateChecked], so it refuses a user who is not active or is banned. A
// test that needs a session for such a user writes the row itself.
func Session(ctx context.Context, pool *pgxpool.Pool, userID int64, remember bool, ip netip.Addr, userAgent string, now time.Time) (string, session.Session, error) {
	var epoch int64
	if err := pool.QueryRow(ctx, `select password_epoch from users where id = $1`, userID).Scan(&epoch); err != nil {
		return "", session.Session{}, err
	}
	return session.CreateChecked(ctx, pool, userID, epoch, remember, ip, userAgent, now)
}
