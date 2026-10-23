package db_test

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"testing/fstest"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
)

func twoFiles() fstest.MapFS {
	return fstest.MapFS{
		"0002_b.sql": {Data: []byte("create table b (id int);")},
		"0001_a.sql": {Data: []byte("create table a (id int); insert into a values (1);")},
	}
}

func TestMigrateAppliesInOrder(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	got, err := db.MigrateFS(ctx, pool, twoFiles())
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0001_a.sql", "0002_b.sql"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
	for _, table := range []string{"a", "b"} {
		var n int
		if err := pool.QueryRow(ctx, "select count(*) from "+table).Scan(&n); err != nil {
			t.Errorf("table %s: %v", table, err)
		}
	}
}

func TestMigrateIdempotent(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	if _, err := db.MigrateFS(ctx, pool, twoFiles()); err != nil {
		t.Fatal(err)
	}
	got, err := db.MigrateFS(ctx, pool, twoFiles())
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("second run applied %v, want nothing", got)
	}
}

func TestMigrateRefusesChangedChecksum(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	first := fstest.MapFS{"0001_a.sql": {Data: []byte("create table a (id int);")}}
	if _, err := db.MigrateFS(ctx, pool, first); err != nil {
		t.Fatal(err)
	}
	second := fstest.MapFS{"0001_a.sql": {Data: []byte("create table a (id bigint);")}}
	_, err := db.MigrateFS(ctx, pool, second)
	if err == nil || !strings.Contains(err.Error(), "0001_a.sql changed after it was applied") {
		t.Fatalf("want a changed-checksum error, got %v", err)
	}
}

func TestMigrateConcurrent(t *testing.T) {
	ctx := context.Background()
	admin := dbtest.NewEmpty(t)
	// Each goroutine gets its own pool, so the advisory lock is contended.
	pools := []*pgxpool.Pool{dbtest.Connect(t, admin), dbtest.Connect(t, admin)}

	start := make(chan struct{})
	var ready, done sync.WaitGroup
	errs := make([]error, len(pools))
	applied := make([][]string, len(pools))
	for i, p := range pools {
		ready.Add(1)
		done.Add(1)
		go func() {
			defer done.Done()
			ready.Done()
			<-start
			applied[i], errs[i] = db.MigrateFS(ctx, p, twoFiles())
		}()
	}
	ready.Wait()
	close(start)
	done.Wait()

	total := 0
	for i := range pools {
		if errs[i] != nil {
			t.Fatalf("goroutine %d: %v", i, errs[i])
		}
		total += len(applied[i])
	}
	if total != 2 {
		t.Errorf("files applied in total = %d, want 2 (%v)", total, applied)
	}
	var rows int
	if err := admin.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 2 {
		t.Errorf("schema_migrations has %d rows, want 2", rows)
	}
}

func TestMigrateRejectsBadName(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	for _, name := range []string{"a.sql", "1_a.sql", "0001_A.sql", "0001-a.sql", "00001_a.sql"} {
		fsys := fstest.MapFS{name: {Data: []byte("select 1;")}}
		if _, err := db.MigrateFS(context.Background(), pool, fsys); err == nil {
			t.Errorf("%q: want an error", name)
		}
	}
}

func TestMigrateIgnoresNonSQLAndSubdirs(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	fsys := fstest.MapFS{
		"README.md":           {Data: []byte("notes")},
		"0001_a.sql":          {Data: []byte("create table a (id int);")},
		"sub/zzz_ignored.sql": {Data: []byte("not sql at all")},
	}
	got, err := db.MigrateFS(context.Background(), pool, fsys)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"0001_a.sql"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("applied %v, want %v", got, want)
	}
}

func TestMigrateEmbedded(t *testing.T) {
	pool := dbtest.NewEmpty(t)
	got, err := db.Migrate(context.Background(), pool)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 || got[0] != "0000_bootstrap.sql" {
		t.Fatalf("applied %v, want 0000_bootstrap.sql first", got)
	}
}

func TestMigrateRollsBackFailedFile(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	fsys := fstest.MapFS{"0001_a.sql": {Data: []byte("create table a (id int); select * from nope;")}}
	if _, err := db.MigrateFS(ctx, pool, fsys); err == nil {
		t.Fatal("want an error")
	}
	var n int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations").Scan(&n); err != nil || n != 0 {
		t.Fatalf("schema_migrations rows = %d, err = %v; want 0", n, err)
	}
	if err := pool.QueryRow(ctx, "select count(*) from a").Scan(&n); err == nil {
		t.Fatal("table a exists after a failed migration")
	}
}

func TestMigrateRejectsDuplicateNumber(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	fsys := fstest.MapFS{
		"0001_a.sql": {Data: []byte("create table a (id int);")},
		"0002_a.sql": {Data: []byte("create table b (id int);")},
		"0002_b.sql": {Data: []byte("create table c (id int);")},
	}
	_, err := db.MigrateFS(ctx, pool, fsys)
	if err == nil || !strings.Contains(err.Error(), "0002_a.sql") || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("want an error that names both files, got %v", err)
	}
	// Nothing ran, not even the unique file.
	var n int
	if err := pool.QueryRow(ctx, "select count(*) from pg_tables where tablename in ('a','b','c')").Scan(&n); err != nil || n != 0 {
		t.Fatalf("tables created = %d, err = %v; want 0", n, err)
	}
}

func TestMigrateRefusesUnknownAppliedVersion(t *testing.T) {
	ctx := context.Background()
	pool := dbtest.NewEmpty(t)
	if _, err := db.MigrateFS(ctx, pool, twoFiles()); err != nil {
		t.Fatal(err)
	}
	// An older binary: it knows 0001 only, and a new file 0003 is pending.
	older := fstest.MapFS{
		"0001_a.sql": twoFiles()["0001_a.sql"],
		"0003_c.sql": {Data: []byte("create table c (id int);")},
	}
	_, err := db.MigrateFS(ctx, pool, older)
	if err == nil || !strings.Contains(err.Error(), "0002_b.sql") {
		t.Fatalf("want an error that names 0002_b.sql, got %v", err)
	}
	var n int
	if err := pool.QueryRow(ctx, "select count(*) from pg_tables where tablename = 'c'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("table c exists = %d, err = %v; want it absent", n, err)
	}
}
