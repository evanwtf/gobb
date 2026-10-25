// Command rowcounts proves a backup round trip. It counts the rows of every table
// in two databases and exits 1 on any difference.
//
// CI restores a dump, then runs this command on the source and the restored copy.
// It lists the tables through db.DataTables, so a table that a later migration
// adds is covered with no edit here. It counts schema_migrations too.
//
// The connection strings come from two variables, so they stay out of the
// process list:
//
//	GOBB_SOURCE_DSN    the database that was dumped
//	GOBB_RESTORED_DSN  the database that the dump was restored into
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/jackc/pgx/v5"
)

// compare counts the rows of every data table in src and dst, and writes one line
// for each table, schema_migrations included. It returns an error when the table lists differ, when a count
// differs, or when src holds no rows at all (an empty source proves nothing).
func compare(ctx context.Context, src, dst db.Querier, out io.Writer) error {
	srcTables, err := db.DataTables(ctx, src)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	dstTables, err := db.DataTables(ctx, dst)
	if err != nil {
		return fmt.Errorf("restored: %w", err)
	}
	if !slices.Equal(srcTables, dstTables) {
		return fmt.Errorf("tables differ: source %v, restored %v", srcTables, dstTables)
	}
	// db.DataTables leaves out schema_migrations. A restore that loses its rows
	// would make gobb run every migration again, so it is counted too.
	counted := append(slices.Clone(srcTables), "schema_migrations")
	var bad []string
	total := 0
	for _, name := range counted {
		a, err := count(ctx, src, name)
		if err != nil {
			return fmt.Errorf("source %s: %w", name, err)
		}
		b, err := count(ctx, dst, name)
		if err != nil {
			return fmt.Errorf("restored %s: %w", name, err)
		}
		if name != "schema_migrations" {
			total += a
		}
		fmt.Fprintf(out, "%s: source %d, restored %d\n", name, a, b)
		if a != b {
			bad = append(bad, fmt.Sprintf("%s: source %d, restored %d", name, a, b))
		}
	}
	fmt.Fprintf(out, "%d tables, %d rows in the data tables of the source\n", len(counted), total)
	if len(bad) > 0 {
		return fmt.Errorf("row counts differ:\n%s", strings.Join(bad, "\n"))
	}
	if total == 0 {
		return errors.New("the source holds no rows, so the round trip proves nothing")
	}
	return nil
}

func count(ctx context.Context, q db.Querier, table string) (int, error) {
	var n int
	err := q.QueryRow(ctx, "select count(*) from "+pgx.Identifier{table}.Sanitize()).Scan(&n)
	return n, err
}

func main() {
	if err := mainErr(); err != nil {
		fmt.Fprintf(os.Stderr, "rowcounts: %v\n", err)
		os.Exit(1)
	}
}

func mainErr() error {
	srcDSN, dstDSN := os.Getenv("GOBB_SOURCE_DSN"), os.Getenv("GOBB_RESTORED_DSN")
	if srcDSN == "" || dstDSN == "" {
		return errors.New("set GOBB_SOURCE_DSN and GOBB_RESTORED_DSN")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	src, err := db.Open(ctx, srcDSN)
	if err != nil {
		return fmt.Errorf("source: %w", err)
	}
	defer src.Close()
	dst, err := db.Open(ctx, dstDSN)
	if err != nil {
		return fmt.Errorf("restored: %w", err)
	}
	defer dst.Close()
	return compare(ctx, src, dst, os.Stdout)
}
