package db_test

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/dbtest"
)

func TestCounterCountsEveryKindOfQuery(t *testing.T) {
	pool := dbtest.New(t)
	if _, err := pool.Exec(t.Context(), `create table counted (n int)`); err != nil {
		t.Fatal(err)
	}
	ctx, c := db.WithCounter(t.Context())
	step := func(name string, want int64, run func()) {
		t.Helper()
		before := c.Count()
		run()
		if got := c.Count() - before; got != want {
			t.Errorf("%s counted %d, want %d", name, got, want)
		}
	}
	step("Exec", 1, func() {
		if _, err := pool.Exec(ctx, `select 1`); err != nil {
			t.Fatal(err)
		}
	})
	step("QueryRow", 1, func() {
		var n int
		if err := pool.QueryRow(ctx, `select 2`).Scan(&n); err != nil {
			t.Fatal(err)
		}
	})
	step("Query", 1, func() {
		rows, err := pool.Query(ctx, `select generate_series(1, 5)`)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
	})
	step("a batch of 3", 3, func() {
		var b pgx.Batch
		b.Queue(`select 1`)
		b.Queue(`select 2`)
		b.Queue(`select 3`)
		br := pool.SendBatch(ctx, &b)
		for range 3 {
			if _, err := br.Exec(); err != nil {
				t.Fatal(err)
			}
		}
		if err := br.Close(); err != nil {
			t.Fatal(err)
		}
	})
	step("CopyFrom", 1, func() {
		if _, err := pool.CopyFrom(ctx, pgx.Identifier{"counted"}, []string{"n"}, pgx.CopyFromRows([][]any{{1}, {2}})); err != nil {
			t.Fatal(err)
		}
	})
	// A transaction counts its begin and commit as statements, and the query
	// inside it: three.
	step("a transaction with one query", 3, func() {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := tx.Exec(ctx, `select 1`); err != nil {
			t.Fatal(err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
	})
}

// TestCounterTotal pins the exact count for a known mix.
func TestCounterTotal(t *testing.T) {
	pool := dbtest.New(t)
	ctx, c := db.WithCounter(t.Context())
	if _, err := pool.Exec(ctx, `select 1`); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := pool.QueryRow(ctx, `select 2`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	rows, err := pool.Query(ctx, `select generate_series(1, 5)`)
	if err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if got := c.Count(); got != 3 {
		t.Errorf("3 queries counted %d", got)
	}
}

func TestQueryCountIsPerContext(t *testing.T) {
	pool := dbtest.New(t)
	var wg sync.WaitGroup
	start := make(chan struct{})
	counts := map[int]int64{}
	var mu sync.Mutex
	for _, want := range []int{5, 11, 2, 8} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			ctx, c := db.WithCounter(t.Context())
			<-start
			for range want {
				var n int
				if err := pool.QueryRow(ctx, `select 1`).Scan(&n); err != nil {
					t.Error(err)
					return
				}
			}
			mu.Lock()
			counts[want] = c.Count()
			mu.Unlock()
		}()
	}
	close(start)
	wg.Wait()
	for want, got := range counts {
		if got != int64(want) {
			t.Errorf("a request with %d queries counted %d", want, got)
		}
	}
}

func TestQueryWithoutCounterWorks(t *testing.T) {
	pool := dbtest.New(t)
	for _, ctx := range []context.Context{context.Background(), t.Context()} {
		var n int
		if err := pool.QueryRow(ctx, `select 1`).Scan(&n); err != nil || n != 1 {
			t.Fatalf("query without a counter: %d, %v", n, err)
		}
		rows, err := pool.Query(ctx, `select 1`)
		if err != nil {
			t.Fatal(err)
		}
		rows.Close()
		if _, err := pool.Exec(ctx, `select 1`); err != nil {
			t.Fatal(err)
		}
	}
	if c := db.CounterFrom(context.Background()); c != nil {
		t.Errorf("CounterFrom(Background) = %v, want nil", c)
	}
}
