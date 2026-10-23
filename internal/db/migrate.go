package db

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"regexp"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// migrationLockID is the key of the advisory lock that serializes migration runs.
const migrationLockID = 7212026

//go:embed migrations/*.sql
var embedded embed.FS

// migrationName matches a migration file name: four digits, a name, ".sql".
var migrationName = regexp.MustCompile(`^[0-9]{4}_[a-z0-9_]+\.sql$`)

// Migrate applies the embedded migrations that the database does not have yet.
// It returns the names of the files it applied.
func Migrate(ctx context.Context, pool *pgxpool.Pool) ([]string, error) {
	sub, err := fs.Sub(embedded, "migrations")
	if err != nil {
		return nil, fmt.Errorf("db: embedded migrations: %w", err)
	}
	return MigrateFS(ctx, pool, sub)
}

type migration struct {
	name     string
	sql      string
	checksum string
}

// MigrateFS applies the NNNN_name.sql files at the root of fsys in name order.
// A file that was applied before must have the same checksum. Each file runs in
// its own transaction. An advisory lock keeps two runs from overlapping.
func MigrateFS(ctx context.Context, pool *pgxpool.Pool, fsys fs.FS) (applied []string, err error) {
	files, err := readMigrations(fsys)
	if err != nil {
		return nil, err
	}

	conn, err := pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("db: acquire connection: %w", err)
	}
	defer conn.Release()

	if _, err := conn.Exec(ctx, "select pg_advisory_lock($1)", migrationLockID); err != nil {
		return nil, fmt.Errorf("db: take migration lock: %w", err)
	}
	defer func() {
		// The caller's context may be done. The lock must still go.
		unlockCtx := context.WithoutCancel(ctx)
		if _, uerr := conn.Exec(unlockCtx, "select pg_advisory_unlock($1)", migrationLockID); uerr != nil {
			// Do not return a connection that may still hold the lock to the pool.
			_ = conn.Hijack().Close(unlockCtx)
			err = errors.Join(err, fmt.Errorf("db: release migration lock: %w", uerr))
		}
	}()

	if _, err := conn.Exec(ctx, `create table if not exists schema_migrations (
		version text primary key,
		checksum text not null,
		applied_at timestamptz not null default now()
	)`); err != nil {
		return nil, fmt.Errorf("db: create schema_migrations: %w", err)
	}

	rows, err := conn.Query(ctx, "select version, checksum from schema_migrations")
	if err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}
	have := map[string]string{}
	for rows.Next() {
		var version, sum string
		if err := rows.Scan(&version, &sum); err != nil {
			rows.Close()
			return nil, fmt.Errorf("db: read schema_migrations: %w", err)
		}
		have[version] = sum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: read schema_migrations: %w", err)
	}

	// Check every applied version before the run changes anything. A version
	// with no file means an older binary runs against a newer database, or
	// someone deleted a migration.
	known := make(map[string]bool, len(files))
	for _, m := range files {
		known[m.name] = true
	}
	var unknown []string
	for version := range have {
		if !known[version] {
			unknown = append(unknown, version)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		return nil, fmt.Errorf("db: the database has applied migration %s, which has no file", strings.Join(unknown, ", "))
	}
	for _, m := range files {
		if sum, ok := have[m.name]; ok && sum != m.checksum {
			return nil, fmt.Errorf("db: migration %s changed after it was applied", m.name)
		}
	}

	applied = []string{}
	for _, m := range files {
		if _, ok := have[m.name]; ok {
			continue
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			return applied, fmt.Errorf("db: migration %s: begin: %w", m.name, err)
		}
		// With no arguments, pgx uses the simple protocol, so a file may hold
		// more than one statement.
		if _, err := tx.Exec(ctx, m.sql); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return applied, fmt.Errorf("db: migration %s: %w", m.name, err)
		}
		if _, err := tx.Exec(ctx,
			"insert into schema_migrations (version, checksum) values ($1, $2)",
			m.name, m.checksum); err != nil {
			_ = tx.Rollback(context.WithoutCancel(ctx))
			return applied, fmt.Errorf("db: migration %s: record: %w", m.name, err)
		}
		if err := tx.Commit(ctx); err != nil {
			return applied, fmt.Errorf("db: migration %s: commit: %w", m.name, err)
		}
		applied = append(applied, m.name)
	}
	return applied, nil
}

// readMigrations reads the migration files at the root of fsys, sorted by name.
// A .sql file with any other name is an error. Other files are ignored.
func readMigrations(fsys fs.FS) ([]migration, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("db: read migrations: %w", err)
	}
	var files []migration
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isSQL(name) {
			continue
		}
		if !migrationName.MatchString(name) {
			return nil, fmt.Errorf("db: migration file %q does not match NNNN_name.sql", name)
		}
		data, err := fs.ReadFile(fsys, name)
		if err != nil {
			return nil, fmt.Errorf("db: read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		files = append(files, migration{name: name, sql: string(data), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].name < files[j].name })
	// Sorted by name, files with one number are next to each other.
	for i := 1; i < len(files); i++ {
		if files[i-1].name[:4] == files[i].name[:4] {
			return nil, fmt.Errorf("db: migrations %s and %s have the same number",
				files[i-1].name, files[i].name)
		}
	}
	return files, nil
}

func isSQL(name string) bool {
	return len(name) >= 4 && name[len(name)-4:] == ".sql"
}
