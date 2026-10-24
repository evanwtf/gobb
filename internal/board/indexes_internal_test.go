package board

import (
	"context"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/internal/boardtest"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// explain returns the plan of sql with the given arguments, with sequential
// scans turned off. The plan then shows which index can serve the query. (A
// fixture of a few rows would make the planner prefer a sequential scan whatever
// the indexes are.)
func explain(t *testing.T, sql string, args ...any) string {
	t.Helper()
	ctx := context.Background()
	pool, _ := boardtest.Load(t, bundletest.Small())
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	if _, err := conn.Exec(ctx, `set enable_seqscan = off`); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.Query(ctx, "explain "+sql, args...)
	if err != nil {
		t.Fatalf("explain: %v", err)
	}
	defer rows.Close()
	var sb strings.Builder
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			t.Fatal(err)
		}
		sb.WriteString(line + "\n")
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return sb.String()
}

// The attachments of a page of posts come from an index on post_id.
func TestAttachmentsQueryUsesIndex(t *testing.T) {
	plan := explain(t, attachmentsSQL, []int64{1, 2, 3})
	if !strings.Contains(plan, "attachments_post_idx") {
		t.Errorf("the plan does not use attachments_post_idx:\n%s", plan)
	}
}

// The global topics come from a partial index. The index has the order of the
// query, so a large table gives an ordered scan with no sort. The query reads
// the four scope arguments and no forum.
func TestGlobalTopicsQueryUsesIndex(t *testing.T) {
	sc := scope{read: []int64{2}, approve: []int64{}, del: []int64{}}
	plan := explain(t, topicSelect+`where `+globalTopics+globalOrder, sc.args()...)
	if !strings.Contains(plan, "topics_global_idx") {
		t.Errorf("the plan does not use topics_global_idx:\n%s", plan)
	}
}

// The announcements of a forum come from a partial index too.
func TestAnnounceTopicsQueryUsesIndex(t *testing.T) {
	sc := scope{read: []int64{2}, approve: []int64{}, del: []int64{}}
	args := append(sc.args(), int64(2))
	plan := explain(t, topicSelect+`where `+announceTopics+announceOrder, args...)
	if !strings.Contains(plan, "topics_announce_idx") {
		t.Errorf("the plan does not use topics_announce_idx:\n%s", plan)
	}
}
