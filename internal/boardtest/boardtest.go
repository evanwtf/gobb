// Package boardtest gives the tests of the read side a database with a board in
// it. The board comes from package bundletest, so every name and post is made up.
package boardtest

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/evanwtf/gobb/internal/importer"
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
