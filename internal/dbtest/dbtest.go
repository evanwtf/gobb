// Package dbtest gives each test its own throwaway PostgreSQL database.
package dbtest

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// EnvVar names the variable that holds the admin connection string. The tests
// create and drop their databases through it.
const EnvVar = "GOBB_TEST_DATABASE_URL"

// New creates a database, applies the embedded migrations, and returns a pool
// for it. The test is skipped when GOBB_TEST_DATABASE_URL is unset. The database
// is dropped when the test ends, even if the test failed.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	pool := NewEmpty(t)
	if _, err := db.Migrate(context.Background(), pool); err != nil {
		t.Fatalf("dbtest: migrate: %v", err)
	}
	return pool
}

// NewEmpty is like New, but it applies no migrations.
func NewEmpty(t testing.TB) *pgxpool.Pool {
	t.Helper()
	adminURL := os.Getenv(EnvVar)
	if adminURL == "" {
		t.Skipf("%s is not set", EnvVar)
	}
	ctx := context.Background()

	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		t.Fatalf("dbtest: random name: %v", err)
	}
	name := "gobb_test_" + hex.EncodeToString(buf[:])

	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Fatalf("dbtest: connect to the admin database: %v", err)
	}
	// name is hex that this function generated, so it needs no quoting.
	if _, err := admin.Exec(ctx, "create database "+name); err != nil {
		_ = admin.Close(ctx)
		t.Fatalf("dbtest: create database: %v", err)
	}
	_ = admin.Close(ctx)

	// Registered before the pool, so it runs after the pool closes.
	t.Cleanup(func() { drop(t, adminURL, name) })

	return connect(t, adminURL, name)
}

// Connect opens another pool on the database that pool uses.
func Connect(t testing.TB, pool *pgxpool.Pool) *pgxpool.Pool {
	t.Helper()
	cfg := pool.Config()
	p, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dbtest: connect: %v", err)
	}
	t.Cleanup(p.Close)
	return p
}

func connect(t testing.TB, adminURL, name string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(adminURL)
	if err != nil {
		t.Fatalf("dbtest: parse %s: %v", EnvVar, err)
	}
	cfg.ConnConfig.Database = name
	db.Trace(cfg.ConnConfig)
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("dbtest: open pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func drop(t testing.TB, adminURL, name string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		t.Errorf("dbtest: connect to drop %s: %v", name, err)
		return
	}
	defer func() { _ = admin.Close(ctx) }()
	if _, err := admin.Exec(ctx, "drop database if exists "+name+" with (force)"); err != nil {
		t.Errorf("dbtest: drop %s: %v", name, err)
	}
}
