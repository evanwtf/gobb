package importer

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// topicCounters sets the counters of every topic in one statement.
//
//   - The last post of a topic is its newest approved post, by created_at and
//     then id. A topic with no approved post uses its newest post of any
//     visibility.
//   - reply_count is the number of approved posts minus 1, and never below 0.
//   - A moved-topic marker has no last post, and its last_post_at is its
//     created_at.
const topicCounters = `
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
		order by topic_id, (visibility = 'approved') desc, created_at desc, id desc
	) l on l.topic_id = tp.id
	left join (
		select topic_id, count(*) as n
		from posts
		where visibility = 'approved'
		group by topic_id
	) c on c.topic_id = tp.id
) x
where x.id = t.id`

// forumCounters sets the counters of every forum in one statement.
//
//   - topic_count counts the approved topics that are not moved-topic markers.
//   - post_count counts the approved posts in approved topics.
//   - last_post_id is the newest of those posts, by created_at and then id.
//
// A forum counts its own topics only. Counts do not roll up to a parent.
const forumCounters = `
update forums f set
	topic_count  = coalesce(tc.n, 0),
	post_count   = coalesce(pc.n, 0),
	last_post_id = lp.id
from forums fo
left join (
	select forum_id, count(*) as n
	from topics
	where visibility = 'approved' and moved_to_topic_id is null
	group by forum_id
) tc on tc.forum_id = fo.id
left join (
	select t.forum_id, count(*) as n
	from posts p join topics t on t.id = p.topic_id
	where p.visibility = 'approved' and t.visibility = 'approved'
	group by t.forum_id
) pc on pc.forum_id = fo.id
left join (
	select distinct on (t.forum_id) t.forum_id, p.id
	from posts p join topics t on t.id = p.topic_id
	where p.visibility = 'approved' and t.visibility = 'approved'
	order by t.forum_id, p.created_at desc, p.id desc
) lp on lp.forum_id = fo.id
where fo.id = f.id`

// computeCounters sets the values that gobb computes from the rows: the topic
// counters and the forum counters. It runs in the import transaction, after the
// last loader.
func computeCounters(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, topicCounters); err != nil {
		return fmt.Errorf("importer: topic counters: %w", err)
	}
	if _, err := tx.Exec(ctx, forumCounters); err != nil {
		return fmt.Errorf("importer: forum counters: %w", err)
	}
	return nil
}
