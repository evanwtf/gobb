// Package db opens the PostgreSQL pool and applies the schema migrations.
package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Querier is the part of a pool or a transaction that the stores use.
// Both *pgxpool.Pool and pgx.Tx satisfy it.
type Querier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

var (
	_ Querier = (*pgxpool.Pool)(nil)
	_ Querier = pgx.Tx(nil)
)

// Open creates a pool for dsn and pings the database before it returns. The pool
// counts its statements for [Counter].
func Open(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	Trace(cfg.ConnConfig)
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return nil, fmt.Errorf("db: open pool: %w", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("db: ping: %w", err)
	}
	return pool, nil
}

// DataTables returns the names of the base tables of the current schema, except
// schema_migrations, sorted. These are the tables that hold forum data. The
// importer checks them for rows and truncates them.
func DataTables(ctx context.Context, q Querier) ([]string, error) {
	rows, err := q.Query(ctx,
		`select table_name from information_schema.tables
		 where table_schema = current_schema() and table_type = 'BASE TABLE'
		   and table_name <> 'schema_migrations'
		 order by table_name`)
	if err != nil {
		return nil, fmt.Errorf("db: list data tables: %w", err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, fmt.Errorf("db: list data tables: %w", err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("db: list data tables: %w", err)
	}
	return names, nil
}
