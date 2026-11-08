package board

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
)

// What the guest page cache of the web layer reads of the board. The cache
// lives in the memory of `gobb serve`; see package pagecache.

// PageEpoch returns the mark that the commands of other processes set after
// they change the board ([BumpPageEpoch]), or 0 when none has set one.
func PageEpoch(ctx context.Context, q db.Querier) (int64, error) {
	var v int64
	err := q.QueryRow(ctx, `select value from page_cache_epoch where id = 1`).Scan(&v)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("board: read the page cache epoch: %w", err)
	}
	return v, nil
}

// BumpPageEpoch sets a new random mark. A running server sees the change and
// empties its guest page cache. Every command that writes the board from
// outside the server calls it after the write.
func BumpPageEpoch(ctx context.Context, q db.Querier) error {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Errorf("board: bump the page cache epoch: %w", err)
	}
	// Never 0, which means "no mark".
	v := int64(binary.BigEndian.Uint64(b[:])>>1) | 1
	_, err := q.Exec(ctx, `
insert into page_cache_epoch (id, value) values (1, $1)
on conflict (id) do update set value = excluded.value`, v)
	if err != nil {
		return fmt.Errorf("board: bump the page cache epoch: %w", err)
	}
	return nil
}

// ForumIDs returns the IDs of the forums and categories that have pages, in the
// order of the board: by parent, then position. Link forums have none. The
// caller checks the rights by rendering the page as a guest.
func ForumIDs(ctx context.Context, q db.Querier) ([]int64, error) {
	rows, err := q.Query(ctx, `select id from forums where kind <> 'link' order by coalesce(parent_id, 0), position, id`)
	if err != nil {
		return nil, fmt.Errorf("board: list the forums: %w", err)
	}
	ids, err := pgx.CollectRows(rows, pgx.RowTo[int64])
	if err != nil {
		return nil, fmt.Errorf("board: list the forums: %w", err)
	}
	return ids, nil
}

// TopicByActivity is one topic in the order of [TopicsByActivity].
type TopicByActivity struct {
	ID     int64
	LastAt time.Time
}

// TopicsByActivity returns up to limit approved topics that are not moved
// stubs, newest activity first, that come after the topic after in that order.
// A zero after starts at the newest. The caller checks the rights by rendering
// the page as a guest.
func TopicsByActivity(ctx context.Context, q db.Querier, after TopicByActivity, limit int) ([]TopicByActivity, error) {
	start := after.ID == 0
	rows, err := q.Query(ctx, `
select id, last_post_at from topics
 where visibility = 'approved' and moved_to_topic_id is null
   and ($1::bool or (last_post_at, id) < ($2::timestamptz, $3::bigint))
 order by last_post_at desc, id desc
 limit $4`, start, after.LastAt, after.ID, limit)
	if err != nil {
		return nil, fmt.Errorf("board: list the topics by activity: %w", err)
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (TopicByActivity, error) {
		var t TopicByActivity
		err := row.Scan(&t.ID, &t.LastAt)
		return t, err
	})
	if err != nil {
		return nil, fmt.Errorf("board: list the topics by activity: %w", err)
	}
	return out, nil
}

// PostRefs returns the topic of post id and its author (0 for a guest's post),
// with no check of rights: the cache needs to know which pages a write changed.
// A post that does not exist gives [ErrNotFound].
func PostRefs(ctx context.Context, q db.Querier, id int64) (topicID, authorID int64, err error) {
	var author *int64
	err = q.QueryRow(ctx, `select topic_id, author_id from posts where id = $1`, id).Scan(&topicID, &author)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("board: find the topic of post %d: %w", id, err)
	}
	if author != nil {
		authorID = *author
	}
	return topicID, authorID, nil
}
