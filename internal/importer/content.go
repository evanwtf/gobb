package importer

import (
	"context"
	"fmt"
	"net/netip"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/postbody"
	"github.com/evanwtf/gobb/markup"
)

// batchSize is the number of rows that a bulk load sends in one COPY.
const batchSize = 1000

// maxBatchBytes is the most row bytes that a batch holds before a bulk load
// sends it, whatever the row count. A variable, so that a test can lower it.
var maxBatchBytes = 8 << 20

// bulk collects rows for one table and sends them with CopyFrom in batches. A
// batch is one COPY statement. It holds at most batchSize rows and about
// maxBatchBytes bytes of row text, so the memory it holds is bounded for any
// row size.
type bulk struct {
	tx    pgx.Tx
	file  string // the data file the rows come from, for error messages
	table string
	cols  []string
	rows  [][]any
	bytes int // the string bytes in rows
	sent  int // the number of batches sent so far
	first int // the line of the first row in rows
	last  int // the line of the last row in rows
}

func newBulk(tx pgx.Tx, file, table string, cols ...string) *bulk {
	return &bulk{tx: tx, file: file, table: table, cols: cols}
}

// add queues a row that came from the given line. It sends the batch when the
// batch is full.
func (b *bulk) add(ctx context.Context, line int, row ...any) error {
	if len(b.rows) == 0 {
		b.first = line
	}
	b.last = line
	b.rows = append(b.rows, row)
	b.bytes += rowBytes(row)
	if len(b.rows) >= batchSize || b.bytes > maxBatchBytes {
		return b.flush(ctx)
	}
	return nil
}

// rowBytes returns the number of bytes in the string values of a row. For a
// post or a message these are the source, the tree and the text, which are the
// bulk of the row, and a few short labels.
func rowBytes(row []any) int {
	n := 0
	for _, v := range row {
		if s, ok := v.(string); ok {
			n += len(s)
		}
	}
	return n
}

// flush sends the queued rows. The error names the lines of the batch, because
// the database does not say which row it refused.
func (b *bulk) flush(ctx context.Context) error {
	if len(b.rows) == 0 {
		return nil
	}
	_, err := b.tx.CopyFrom(ctx, pgx.Identifier{b.table}, b.cols, pgx.CopyFromRows(b.rows))
	if err != nil {
		return fmt.Errorf("%s.jsonl:%d-%d: insert into %s: %w", b.file, b.first, b.last, b.table, err)
	}
	// clear drops the sent rows, so a large row does not outlive its batch.
	clear(b.rows)
	b.rows = b.rows[:0]
	b.bytes = 0
	b.sent++
	return nil
}

// ipValue turns an optional IP address into a value for an inet column.
func ipValue(s *string) (any, error) {
	if s == nil {
		return nil, nil
	}
	a, err := netip.ParseAddr(*s)
	if err != nil {
		return nil, err
	}
	return a, nil
}

// bodyColumns parses a body and returns the JSON of its tree and its plain
// text. The error says which part failed.
func bodyColumns(b bundle.Body, smilies []markup.Smiley) (tree, text string, err error) {
	n, err := parseTree(b, smilies)
	if err != nil {
		return "", "", err
	}
	data, text, err := postbody.Columns(n)
	if err != nil {
		return "", "", err
	}
	return string(data), text, nil
}

func loadTopics(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	r.topicForum = map[int64]int64{}
	cp := newBulk(tx, "topics", "topics",
		"id", "forum_id", "title", "author_id", "author_name", "created_at",
		"last_post_id", "last_post_at", "reply_count", "view_count", "kind",
		"locked", "visibility", "moved_to_topic_id")
	n := 0
	err := bundle.Each(r.Reader, func(line int, x bundle.Topic) error {
		r.topicForum[x.ID] = x.ForumID
		// The counters step sets last_post_id, last_post_at and reply_count.
		if err := cp.add(ctx, line, x.ID, x.ForumID, x.Title, x.AuthorID, x.AuthorName,
			x.CreatedAt, nil, x.CreatedAt, 0, x.ViewCount, x.Kind, x.Locked,
			x.Visibility, x.MovedTo); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadPosts(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "posts", "posts",
		"id", "topic_id", "forum_id", "author_id", "author_name", "author_ip",
		"created_at", "subject", "body_source", "body_tree", "body_text",
		"enable_markdown", "enable_smilies", "enable_autolink", "enable_sig",
		"visibility", "edit_count", "edited_at", "edited_by", "edit_reason")
	n := 0
	err := bundle.Each(r.Reader, func(line int, p bundle.Post) error {
		forum, ok := r.topicForum[p.TopicID]
		if !ok {
			return idErr("posts", line, p.ID, "topic_id", fmt.Errorf("topic %d is not loaded", p.TopicID))
		}
		ip, err := ipValue(p.AuthorIP)
		if err != nil {
			return idErr("posts", line, p.ID, "author_ip", err)
		}
		tree, text, err := bodyColumns(p.Body, r.smilies)
		if err != nil {
			return idErr("posts", line, p.ID, "body", err)
		}
		if err := cp.add(ctx, line, p.ID, p.TopicID, forum, p.AuthorID, p.AuthorName, ip,
			p.CreatedAt, p.Subject, p.Body.Source, tree, text,
			p.Body.Markdown, p.Body.Smilies, p.Body.Autolink, p.ShowSignature,
			p.Visibility, p.EditCount, p.EditedAt, p.EditedBy, p.EditReason); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadPolls(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	polls := newBulk(tx, "polls", "polls",
		"topic_id", "question", "max_options", "ends_at", "can_change_vote")
	options := newBulk(tx, "polls", "poll_options", "topic_id", "option_id", "text", "vote_count")
	n := 0
	err := bundle.Each(r.Reader, func(line int, p bundle.Poll) error {
		if err := polls.add(ctx, line, p.TopicID, p.Question, p.MaxOptions, p.EndsAt, p.CanChangeVote); err != nil {
			return err
		}
		for _, o := range p.Options {
			if err := options.add(ctx, line, p.TopicID, o.ID, o.Text, o.Votes); err != nil {
				return err
			}
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	// The rows of polls go first. The constraints are deferred anyway.
	if err := polls.flush(ctx); err != nil {
		return n, err
	}
	return n, options.flush(ctx)
}

func loadPollVotes(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "poll_votes", "poll_votes", "topic_id", "option_id", "user_id", "voter_ip")
	n := 0
	err := bundle.Each(r.Reader, func(line int, v bundle.PollVote) error {
		ip, err := ipValue(v.VoterIP)
		if err != nil {
			return rowErr("poll_votes", line, fmt.Sprintf("topic %d, user %d", v.TopicID, v.UserID), "voter_ip", err)
		}
		if err := cp.add(ctx, line, v.TopicID, v.OptionID, v.UserID, ip); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadAttachments(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "attachments", "attachments",
		"id", "post_id", "position", "uploader_id", "real_name", "stored_path", "mime",
		"size", "sha256", "width", "height", "download_count", "comment", "created_at")
	n := 0
	err := bundle.Each(r.Reader, func(line int, a bundle.Attachment) error {
		if err := cp.add(ctx, line, a.ID, a.PostID, a.Position, a.UploaderID, a.Name,
			a.File.Path, a.Mime, a.File.Size, a.File.SHA256, a.Width, a.Height,
			a.DownloadCount, a.Comment, a.CreatedAt); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadPMs(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	pms := newBulk(tx, "pms", "pms",
		"id", "author_id", "author_name", "author_ip", "created_at", "subject", "body_source",
		"body_tree", "body_text", "enable_markdown", "enable_smilies",
		"enable_autolink", "enable_sig", "author_deleted")
	rcpts := newBulk(tx, "pms", "pm_recipients", "pm_id", "user_id", "kind", "is_read", "is_deleted")
	n := 0
	err := bundle.Each(r.Reader, func(line int, m bundle.PM) error {
		ip, err := ipValue(m.AuthorIP)
		if err != nil {
			return idErr("pms", line, m.ID, "author_ip", err)
		}
		tree, text, err := bodyColumns(m.Body, r.smilies)
		if err != nil {
			return idErr("pms", line, m.ID, "body", err)
		}
		if err := pms.add(ctx, line, m.ID, m.AuthorID, m.AuthorName, ip, m.CreatedAt, m.Subject,
			m.Body.Source, tree, text, m.Body.Markdown, m.Body.Smilies, m.Body.Autolink,
			m.ShowSignature, m.AuthorDeleted); err != nil {
			return err
		}
		for _, c := range m.Recipients {
			if err := rcpts.add(ctx, line, m.ID, c.UserID, c.Kind, c.Read, c.Deleted); err != nil {
				return err
			}
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	if err := pms.flush(ctx); err != nil {
		return n, err
	}
	return n, rcpts.flush(ctx)
}

func loadTopicWatches(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "topic_watches", "topic_watches", "user_id", "topic_id", "notify_pending")
	n := 0
	err := bundle.Each(r.Reader, func(line int, w bundle.TopicWatch) error {
		if err := cp.add(ctx, line, w.UserID, w.TopicID, w.NotifyPending); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadBookmarks(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "bookmarks", "bookmarks", "user_id", "topic_id")
	n := 0
	err := bundle.Each(r.Reader, func(line int, b bundle.Bookmark) error {
		if err := cp.add(ctx, line, b.UserID, b.TopicID); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadTopicMarks(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "topic_marks", "topic_marks", "user_id", "topic_id", "marked_at")
	n := 0
	err := bundle.Each(r.Reader, func(line int, m bundle.TopicMark) error {
		if err := cp.add(ctx, line, m.UserID, m.TopicID, m.MarkedAt); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}

func loadForumMarks(ctx context.Context, tx pgx.Tx, r *source) (int, error) {
	cp := newBulk(tx, "forum_marks", "forum_marks", "user_id", "forum_id", "marked_at")
	n := 0
	err := bundle.Each(r.Reader, func(line int, m bundle.ForumMark) error {
		if err := cp.add(ctx, line, m.UserID, m.ForumID, m.MarkedAt); err != nil {
			return err
		}
		n++
		return nil
	})
	if err != nil {
		return n, err
	}
	return n, cp.flush(ctx)
}
