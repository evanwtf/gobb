package counters_test

import (
	"context"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
	"github.com/evanwtf/gobb/internal/counters"
)

// snapshot returns every counter column of every topic and forum, one line per
// row, so that two snapshots compare with ==.
func snapshot(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var sb strings.Builder
	rows, err := pool.Query(ctx, `
select id, coalesce(last_post_id, 0), last_post_at, reply_count from topics order by id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, last int64
		var at any
		var replies int
		if err := rows.Scan(&id, &last, &at, &replies); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "topic %d last=%d at=%v replies=%d\n", id, last, at, replies)
	}
	rows.Close()
	rows, err = pool.Query(ctx, `
select id, topic_count, post_count, coalesce(last_post_id, 0) from forums order by id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id, last int64
		var topics, posts int
		if err := rows.Scan(&id, &topics, &posts, &last); err != nil {
			t.Fatal(err)
		}
		fmt.Fprintf(&sb, "forum %d topics=%d posts=%d last=%d\n", id, topics, posts, last)
	}
	rows.Close()
	return sb.String()
}

func TestCountersOneRowEqualsAll(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Large(12, 5))
	rng := rand.New(rand.NewPCG(7, 11))
	vis := []string{"approved", "unapproved", "deleted"}

	for round := 0; round < 25; round++ {
		touchedTopics := map[int64]bool{}
		touchedForums := map[int64]bool{}
		for range 1 + rng.IntN(4) {
			if rng.IntN(4) == 0 {
				id := int64(1 + rng.IntN(12))
				var forum int64
				err := pool.QueryRow(ctx, `update topics set visibility = $2 where id = $1 returning forum_id`,
					id, vis[rng.IntN(3)]).Scan(&forum)
				if err != nil {
					t.Fatal(err)
				}
				touchedTopics[id] = true
				touchedForums[forum] = true
				continue
			}
			id := int64(1 + rng.IntN(60))
			var topic, forum int64
			err := pool.QueryRow(ctx, `update posts set visibility = $2 where id = $1 returning topic_id, forum_id`,
				id, vis[rng.IntN(3)]).Scan(&topic, &forum)
			if err != nil {
				t.Fatal(err)
			}
			touchedTopics[topic] = true
			touchedForums[forum] = true
		}
		for id := range touchedTopics {
			if err := counters.Topic(ctx, pool, id); err != nil {
				t.Fatal(err)
			}
		}
		for id := range touchedForums {
			if err := counters.Forum(ctx, pool, id); err != nil {
				t.Fatal(err)
			}
		}
		got := snapshot(t, pool)
		if err := counters.All(ctx, pool); err != nil {
			t.Fatal(err)
		}
		if want := snapshot(t, pool); got != want {
			t.Fatalf("round %d: one-row forms differ from All\n--- one-row\n%s--- all\n%s", round, got, want)
		}
	}
}

func TestCountersOneRowTouchesOneRow(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	if err := counters.All(ctx, pool); err != nil {
		t.Fatal(err)
	}
	// Make a topic and a forum wrong, then fix a different one of each.
	if _, err := pool.Exec(ctx, `update topics set reply_count = 999 where id = 1`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `update forums set post_count = 999 where id = 2`); err != nil {
		t.Fatal(err)
	}
	if err := counters.Topic(ctx, pool, 2); err != nil {
		t.Fatal(err)
	}
	if err := counters.Forum(ctx, pool, 3); err != nil {
		t.Fatal(err)
	}
	var replies, posts int
	if err := pool.QueryRow(ctx, `select reply_count from topics where id = 1`).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select post_count from forums where id = 2`).Scan(&posts); err != nil {
		t.Fatal(err)
	}
	if replies != 999 || posts != 999 {
		t.Errorf("topic 1 reply_count = %d, forum 2 post_count = %d: a one-row form changed another row", replies, posts)
	}
	// And the row it was asked about is right.
	if err := counters.Topic(ctx, pool, 1); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, `select reply_count from topics where id = 1`).Scan(&replies); err != nil {
		t.Fatal(err)
	}
	if replies != 2 {
		t.Errorf("topic 1 reply_count = %d, want 2 (posts 1, 2 and 4 are approved)", replies)
	}
}

func TestCountersUnknownRow(t *testing.T) {
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	before := snapshot(t, pool)
	if err := counters.Topic(ctx, pool, 4040); err != nil {
		t.Errorf("Topic of a missing row = %v, want nil", err)
	}
	if err := counters.Forum(ctx, pool, 4040); err != nil {
		t.Errorf("Forum of a missing row = %v, want nil", err)
	}
	if after := snapshot(t, pool); after != before {
		t.Errorf("a missing row changed the counters:\n%s\n%s", before, after)
	}
}
