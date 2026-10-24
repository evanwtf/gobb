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
	plan := explain(t, topicSQL(`where `+globalTopics, globalOrder, ``), sc.args()...)
	if !strings.Contains(plan, "topics_global_idx") {
		t.Errorf("the plan does not use topics_global_idx:\n%s", plan)
	}
}

// The announcements of a forum come from a partial index too.
func TestAnnounceTopicsQueryUsesIndex(t *testing.T) {
	sc := scope{read: []int64{2}, approve: []int64{}, del: []int64{}}
	args := append(sc.args(), int64(2))
	plan := explain(t, topicSQL(`where `+announceTopics, announceOrder, ``), args...)
	if !strings.Contains(plan, "topics_announce_idx") {
		t.Errorf("the plan does not use topics_announce_idx:\n%s", plan)
	}
}

// The badge query of the index and the "new since" queries ask for the topics
// newer than a time. An index on last_post_at bounds the scan to those topics:
// topics_last_post_idx, or topics_forum_last_post_idx when the viewer reads few
// forums.
func TestUnreadQueriesUseIndex(t *testing.T) {
	sc := scope{read: []int64{2}, approve: []int64{}, del: []int64{}, userID: 3}
	for name, sql := range map[string]string{
		"forums with unread": unreadForumsSQL,
		"new since, count":   `select count(*) from topics t where ` + newSinceWhere,
	} {
		if plan := explain(t, sql, sc.args()...); !strings.Contains(plan, "last_post_idx") || strings.Contains(plan, "Seq Scan on topics") {
			t.Errorf("%s: the plan does not use an index on last_post_at:\n%s", name, plan)
		}
	}
	args := append(sc.args(), 50, 0)
	plan := explain(t, topicSQL(`where `+newSinceWhere, newSinceOrder, `limit $5 offset $6`), args...)
	if !strings.Contains(plan, "last_post_idx") || strings.Contains(plan, "Seq Scan on topics") {
		t.Errorf("new since, page: the plan does not use an index on last_post_at:\n%s", plan)
	}
	// A page of topics reaches the marks of the member by primary key, row by
	// row, and never reads all of them.
	if !strings.Contains(plan, "topic_marks_pkey") || strings.Contains(plan, "Hash Cond: (topic_marks.topic_id") {
		t.Errorf("new since, page: the marks are not read by primary key:\n%s", plan)
	}
}
