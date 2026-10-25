package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
	"github.com/jackc/pgx/v5/pgxpool"
)

// probe adds a table that holds n rows. DataTables lists it with the schema's
// own tables, which stay empty here.
func probe(t *testing.T, pool *pgxpool.Pool, name string, n int) {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, "create table "+name+" (x int)"); err != nil {
		t.Fatal(err)
	}
	if n > 0 {
		if _, err := pool.Exec(ctx, "insert into "+name+" select generate_series(1, $1)", n); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCompare(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name    string
		setup   func(t *testing.T, src, dst *pgxpool.Pool)
		wantErr string // a substring; empty means the databases match
	}{
		{
			name: "equal counts match",
			setup: func(t *testing.T, src, dst *pgxpool.Pool) {
				probe(t, src, "rc_probe", 3)
				probe(t, dst, "rc_probe", 3)
			},
		},
		{
			name: "one row fewer in the restore",
			setup: func(t *testing.T, src, dst *pgxpool.Pool) {
				probe(t, src, "rc_probe", 3)
				probe(t, dst, "rc_probe", 2)
			},
			wantErr: "rc_probe: source 3, restored 2",
		},
		{
			name: "one row more in the restore",
			setup: func(t *testing.T, src, dst *pgxpool.Pool) {
				probe(t, src, "rc_probe", 3)
				probe(t, dst, "rc_probe", 4)
			},
			wantErr: "rc_probe: source 3, restored 4",
		},
		{
			name: "a table the restore lacks",
			setup: func(t *testing.T, src, dst *pgxpool.Pool) {
				probe(t, src, "rc_probe", 3)
			},
			wantErr: "tables differ",
		},
		{
			name: "a table the source lacks",
			setup: func(t *testing.T, src, dst *pgxpool.Pool) {
				probe(t, src, "rc_probe", 3)
				probe(t, dst, "rc_probe", 3)
				probe(t, dst, "rc_extra", 0)
			},
			wantErr: "tables differ",
		},
		{
			name:    "an empty source proves nothing",
			setup:   func(t *testing.T, src, dst *pgxpool.Pool) {},
			wantErr: "no rows",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			src, dst := dbtest.New(t), dbtest.New(t)
			tt.setup(t, src, dst)
			var out bytes.Buffer
			err := compare(ctx, src, dst, &out)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("compare: %v\n%s", err, out.String())
				}
				if !strings.Contains(out.String(), "rc_probe: source 3, restored 3") {
					t.Errorf("output lacks the probe line:\n%s", out.String())
				}
				return
			}
			if err == nil {
				t.Fatalf("compare passed, want an error with %q\n%s", tt.wantErr, out.String())
			}
			if !strings.Contains(err.Error(), tt.wantErr) && !strings.Contains(out.String(), tt.wantErr) {
				t.Fatalf("error = %q\noutput:\n%s\nwant %q", err, out.String(), tt.wantErr)
			}
		})
	}
}

// TestCompareListsEveryTable checks that the count covers every table that
// db.DataTables lists, so a table added by a later migration needs no edit.
func TestCompareListsEveryTable(t *testing.T) {
	ctx := context.Background()
	src, dst := dbtest.New(t), dbtest.New(t)
	probe(t, src, "rc_probe", 1)
	probe(t, dst, "rc_probe", 1)
	var out bytes.Buffer
	if err := compare(ctx, src, dst, &out); err != nil {
		t.Fatal(err)
	}
	tables, err := db.DataTables(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if len(tables) < 10 {
		t.Fatalf("only %d tables", len(tables))
	}
	for _, name := range tables {
		if !strings.Contains(out.String(), name+": source ") {
			t.Errorf("table %s is missing from the output", name)
		}
	}
}
