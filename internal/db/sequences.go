package db

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ResetSequences moves the sequence of every identity column past the largest
// value in its column. The importer inserts rows with explicit IDs. After it
// loads them, the next insert without an ID must not collide. An empty table
// gets 1 as its next ID.
//
// The columns come from the catalog, so a new table needs no change here.
func ResetSequences(ctx context.Context, q Querier) error {
	rows, err := q.Query(ctx,
		`select table_schema, table_name, column_name
		 from information_schema.columns
		 where table_schema = current_schema() and is_identity = 'YES'
		 order by table_name, column_name`)
	if err != nil {
		return fmt.Errorf("db: list identity columns: %w", err)
	}
	type column struct{ schema, table, name string }
	var columns []column
	for rows.Next() {
		var c column
		if err := rows.Scan(&c.schema, &c.table, &c.name); err != nil {
			rows.Close()
			return fmt.Errorf("db: list identity columns: %w", err)
		}
		columns = append(columns, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("db: list identity columns: %w", err)
	}

	for _, c := range columns {
		table := pgx.Identifier{c.schema, c.table}.Sanitize()
		col := pgx.Identifier{c.name}.Sanitize()
		// The names come from the catalog and are quoted, so they are safe to
		// put in the statement. A bind parameter cannot name a table.
		stmt := fmt.Sprintf(
			`select setval(pg_get_serial_sequence($1, $2), coalesce(max(%s), 0) + 1, false) from %s`,
			col, table)
		var next int64
		if err := q.QueryRow(ctx, stmt, table, c.name).Scan(&next); err != nil {
			return fmt.Errorf("db: reset sequence for %s.%s: %w", c.table, c.name, err)
		}
	}
	return nil
}
