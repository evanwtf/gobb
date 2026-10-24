// Package counters computes the numbers that gobb keeps in the rows of topics
// and forums: the reply count, the topic and post counts and the last post.
//
// The importer calls [All] once, after it has loaded every row. A write to the
// board calls [Topic] and [Forum] for the rows it changed. Both forms run one
// statement text, so they cannot disagree: the text has marks where the one-row
// form adds its filter, and the whole-board form leaves them empty. A test
// proves that the one-row forms give what [All] gives.
//
// A caller that runs the one-row forms in a transaction must first lock the
// topic or the forum row with select ... for update. The statement reads the
// posts and topics with the snapshot of its own start. After the lock, a later
// statement sees every change that the earlier lock holder committed.
package counters

import (
	"context"
	"fmt"
	"strings"

	"github.com/evanwtf/gobb/internal/db"
)

// topicSQL sets the counters of topics in one statement.
//
//   - The last post of a topic is its newest approved post, by created_at and
//     then id. A topic with no approved post uses its newest post of any
//     visibility.
//   - reply_count is the number of approved posts minus 1, and never below 0.
//   - A moved-topic marker has no last post, and its last_post_at is its
//     created_at.
//
// The marks are filled by [render]: {topic} filters the topics of the inner
// query, {posts} filters the posts of the "last post" query, and {approved}
// filters the posts of the "approved count" query.
const topicSQL = `
update topics t set
	last_post_id = x.last_id,
	last_post_at = coalesce(x.last_at, t.created_at),
	reply_count  = greatest(x.approved - 1, 0)
from (
	select tp.id,
	       case when tp.moved_to_topic_id is null then l.id end         as last_id,
	       case when tp.moved_to_topic_id is null then l.created_at end as last_at,
	       coalesce(c.n, 0)                                             as approved
	from topics tp
	left join (
		select distinct on (topic_id) topic_id, id, created_at
		from posts
		{posts}
		order by topic_id, (visibility = 'approved') desc, created_at desc, id desc
	) l on l.topic_id = tp.id
	left join (
		select topic_id, count(*) as n
		from posts
		where visibility = 'approved' {approved}
		group by topic_id
	) c on c.topic_id = tp.id
	{topic}
) x
where x.id = t.id`

// forumSQL sets the counters of forums in one statement.
//
//   - topic_count counts the approved topics that are not moved-topic markers.
//   - post_count counts the approved posts in approved topics.
//   - last_post_id is the newest of those posts, by created_at and then id.
//
// A forum counts its own topics only. Counts do not roll up to a parent.
//
// The marks are filled by [render]: {topics}, {posts} and {last} filter the
// three inner queries by forum, and {forum} filters the forum itself.
const forumSQL = `
update forums f set
	topic_count  = coalesce(tc.n, 0),
	post_count   = coalesce(pc.n, 0),
	last_post_id = lp.id
from forums fo
left join (
	select forum_id, count(*) as n
	from topics
	where visibility = 'approved' and moved_to_topic_id is null {topics}
	group by forum_id
) tc on tc.forum_id = fo.id
left join (
	select t.forum_id, count(*) as n
	from posts p join topics t on t.id = p.topic_id
	where p.visibility = 'approved' and t.visibility = 'approved' {posts}
	group by t.forum_id
) pc on pc.forum_id = fo.id
left join (
	select distinct on (t.forum_id) t.forum_id, p.id
	from posts p join topics t on t.id = p.topic_id
	where p.visibility = 'approved' and t.visibility = 'approved' {last}
	order by t.forum_id, p.created_at desc, p.id desc
) lp on lp.forum_id = fo.id
where fo.id = f.id {forum}`

// render fills the marks of a statement text. A mark that fills with "" leaves
// the whole-board form.
func render(sql string, marks ...string) string {
	return strings.NewReplacer(marks...).Replace(sql)
}

var (
	allTopics = render(topicSQL, "{posts}", "", "{approved}", "", "{topic}", "")
	oneTopic  = render(topicSQL,
		"{posts}", "where topic_id = $1",
		"{approved}", "and topic_id = $1",
		"{topic}", "where tp.id = $1")
	allForums = render(forumSQL, "{topics}", "", "{posts}", "", "{last}", "", "{forum}", "")
	oneForum  = render(forumSQL,
		"{topics}", "and forum_id = $1",
		"{posts}", "and t.forum_id = $1",
		"{last}", "and t.forum_id = $1",
		"{forum}", "and f.id = $1")
)

// All sets the counters of every topic and every forum. The importer runs it in
// the import transaction, after the last loader.
func All(ctx context.Context, q db.Querier) error {
	if _, err := q.Exec(ctx, allTopics); err != nil {
		return fmt.Errorf("counters: topic counters: %w", err)
	}
	if _, err := q.Exec(ctx, allForums); err != nil {
		return fmt.Errorf("counters: forum counters: %w", err)
	}
	return nil
}

// Topic sets the counters of topic topicID: the reply count and the last post.
// It changes no other row. A topic that does not exist is not an error.
func Topic(ctx context.Context, q db.Querier, topicID int64) error {
	if _, err := q.Exec(ctx, oneTopic, topicID); err != nil {
		return fmt.Errorf("counters: counters of topic %d: %w", topicID, err)
	}
	return nil
}

// Forum sets the counters of forum forumID: the topic count, the post count
// and the last post. It changes no other row. A forum that does not exist is not
// an error.
func Forum(ctx context.Context, q db.Querier, forumID int64) error {
	if _, err := q.Exec(ctx, oneForum, forumID); err != nil {
		return fmt.Errorf("counters: counters of forum %d: %w", forumID, err)
	}
	return nil
}
