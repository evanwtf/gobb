package mail

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
)

const (
	// batchSize is the most rows one pass claims.
	batchSize = 10
	// lease is how long a claimed row stays out of other passes. A pass that dies
	// in the middle of a send leaves its row to the next pass after this time.
	lease = 5 * time.Minute
	// maxAttempts is the number of claims after which a row that fails is dropped.
	maxAttempts = 8
	// tickEvery is the time between two passes of [Queue.Run] when nothing wakes it.
	tickEvery = 30 * time.Second
	// keepFor is how long a sent or dropped row stays before [Cleanup] deletes it.
	keepFor = 30 * 24 * time.Hour
	// writeTimeout bounds the write of an outcome, which a stop of the server
	// does not cancel.
	writeTimeout = 10 * time.Second
)

// Enqueue adds m to the email queue, due at once, for the member userID. It runs
// [Check] first and inserts nothing when Check refuses m. sendBy is the time
// after which the message is useless: the worker drops it unsent then. The zero
// time means no limit. A userID of 0 means no member. q is a pool, or the
// transaction that also writes what the message is about. It returns the ID of
// the row.
func Enqueue(ctx context.Context, q db.Querier, m Message, userID int64, sendBy, now time.Time) (int64, error) {
	if err := Check(m); err != nil {
		return 0, err
	}
	var uid *int64
	if userID != 0 {
		uid = &userID
	}
	var by *time.Time
	if !sendBy.IsZero() {
		by = &sendBy
	}
	var id int64
	err := q.QueryRow(ctx,
		`insert into email_queue (to_addr, subject, body, created_at, next_attempt_at, user_id, send_by)
		 values ($1, $2, $3, $4, $4, $5, $6) returning id`,
		m.To, m.Subject, m.Body, now, uid, by).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("mail: enqueue: %w", err)
	}
	return id, nil
}

// Queue sends the messages in the email queue. Its worker holds no database
// connection while it talks to the SMTP server.
type Queue struct {
	Pool   *pgxpool.Pool
	Sender Sender
	// Now returns the time. It defaults to time.Now.
	Now func() time.Time
	// Wake, when it gets a value, starts a pass at once. It may be nil.
	Wake <-chan struct{}
}

func (q *Queue) now() time.Time {
	if q.Now != nil {
		return q.Now()
	}
	return time.Now()
}

// job is one claimed row.
type job struct {
	id       int64
	userID   *int64
	msg      Message
	attempts int
	sendBy   *time.Time
}

// userAttr is the log attribute for a row's member.
func (j job) userAttr() slog.Attr {
	if j.userID == nil {
		return slog.String("user_id", "none")
	}
	return slog.Int64("user_id", *j.userID)
}

// claim takes up to batchSize due rows in one statement: it leases each one and
// counts the claim as an attempt. The statement commits before claim returns, so
// no transaction is open during a send.
func (q *Queue) claim(ctx context.Context, now time.Time) ([]job, error) {
	rows, err := q.Pool.Query(ctx,
		`update email_queue set next_attempt_at = $2, attempts = attempts + 1
		 where id in (
		     select id from email_queue
		     where sent_at is null and dropped_at is null and next_attempt_at <= $1
		     order by next_attempt_at, id limit $3
		     for update skip locked)
		 returning id, user_id, to_addr, subject, body, attempts, send_by`,
		now, now.Add(lease), batchSize)
	if err != nil {
		return nil, fmt.Errorf("mail: claim: %w", err)
	}
	defer rows.Close()
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.userID, &j.msg.To, &j.msg.Subject, &j.msg.Body, &j.attempts, &j.sendBy); err != nil {
			return nil, fmt.Errorf("mail: claim: %w", err)
		}
		jobs = append(jobs, j)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("mail: claim: %w", err)
	}
	slices.SortFunc(jobs, func(a, b job) int { return cmp.Compare(a.id, b.id) })
	return jobs, nil
}

// retryDelay is the wait after a failed attempt: 1, 5 and 15 minutes after the
// first three, 30 minutes after any later one.
func retryDelay(attempts int) time.Duration {
	switch attempts {
	case 1:
		return time.Minute
	case 2:
		return 5 * time.Minute
	case 3:
		return 15 * time.Minute
	}
	return 30 * time.Minute
}

// Process runs one pass: it claims the due rows, sends each one and writes the
// outcome. It returns how many rows were sent, how many failed and wait for a
// retry, and how many it dropped. When ctx ends, it stops, leaves the row it was
// sending as claimed (its lease runs out, and a later pass tries it again) and
// returns the error of ctx. The end of ctx is not a failure of the message.
func (q *Queue) Process(ctx context.Context) (sent, failed, dropped int, err error) {
	jobs, err := q.claim(ctx, q.now())
	if err != nil {
		return 0, 0, 0, err
	}
	var writeErr error
	for _, j := range jobs {
		if ctx.Err() != nil {
			return sent, failed, dropped, ctx.Err()
		}
		outcome, werr := q.handle(ctx, j)
		if werr != nil {
			writeErr = werr
			continue
		}
		switch outcome {
		case outcomeSent:
			sent++
		case outcomeFailed:
			failed++
		case outcomeDropped:
			dropped++
		case outcomeStopped:
			return sent, failed, dropped, ctx.Err()
		}
	}
	return sent, failed, dropped, writeErr
}

type outcome int

const (
	outcomeSent outcome = iota
	outcomeFailed
	outcomeDropped
	outcomeStopped
)

// handle sends one claimed row and writes what came of it.
func (q *Queue) handle(ctx context.Context, j job) (outcome, error) {
	if j.sendBy != nil && !q.now().Before(*j.sendBy) {
		return outcomeDropped, q.drop(ctx, j, "expired")
	}
	err := q.Sender.Send(ctx, j.msg)
	if err == nil {
		return outcomeSent, q.markSent(ctx, j)
	}
	if ctx.Err() != nil {
		return outcomeStopped, nil
	}
	reason := Summary(err)
	if Permanent(err) || j.attempts >= maxAttempts {
		return outcomeDropped, q.drop(ctx, j, reason)
	}
	return outcomeFailed, q.retry(ctx, j, reason)
}

// write runs one outcome statement. The end of ctx does not cancel it: the
// message may be sent already, and the row has to say so.
func (q *Queue) write(ctx context.Context, sql string, args ...any) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), writeTimeout)
	defer cancel()
	if _, err := q.Pool.Exec(ctx, sql, args...); err != nil {
		return fmt.Errorf("mail: write outcome: %w", err)
	}
	return nil
}

func (q *Queue) markSent(ctx context.Context, j job) error {
	err := q.write(ctx, `update email_queue set sent_at = $2, body = '' where id = $1`, j.id, q.now())
	if err != nil {
		slog.Error("email sent, but the row was not updated", "id", j.id, j.userAttr(), "err", err)
		return err
	}
	slog.Info("email sent", "id", j.id, j.userAttr())
	return nil
}

func (q *Queue) retry(ctx context.Context, j job, reason string) error {
	next := q.now().Add(retryDelay(j.attempts))
	err := q.write(ctx, `update email_queue set next_attempt_at = $2, last_error = $3 where id = $1`,
		j.id, next, reason)
	if err != nil {
		slog.Error("email failure was not recorded", "id", j.id, j.userAttr(), "err", err)
		return err
	}
	slog.Warn("email failed", "id", j.id, j.userAttr(), "attempt", j.attempts, "reason", reason)
	return nil
}

func (q *Queue) drop(ctx context.Context, j job, reason string) error {
	err := q.write(ctx, `update email_queue set dropped_at = $2, body = '', last_error = $3 where id = $1`,
		j.id, q.now(), reason)
	if err != nil {
		slog.Error("email drop was not recorded", "id", j.id, j.userAttr(), "err", err)
		return err
	}
	slog.Warn("email dropped", "id", j.id, j.userAttr(), "attempt", j.attempts, "reason", reason)
	return nil
}

// Run runs one pass at once, then one on each tick of 30 s and on each value on
// Wake, until ctx ends. A pass that has started stops when ctx ends; no pass
// starts after that.
func (q *Queue) Run(ctx context.Context) {
	ticker := time.NewTicker(tickEvery)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if _, _, _, err := q.Process(ctx); err != nil && !errors.Is(err, context.Canceled) && ctx.Err() == nil {
			slog.Error("email queue pass failed", "err", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		case <-q.Wake:
		}
	}
}

// Cleanup deletes the queue rows that were sent or dropped more than 30 days
// before now, and the reset links that expired by now. It returns the number of
// rows it deleted, both kinds together.
func Cleanup(ctx context.Context, q db.Querier, now time.Time) (int64, error) {
	tag, err := q.Exec(ctx,
		`delete from email_queue where sent_at < $1 or dropped_at < $1`, now.Add(-keepFor))
	if err != nil {
		return 0, fmt.Errorf("mail: cleanup queue: %w", err)
	}
	n := tag.RowsAffected()
	tag, err = q.Exec(ctx, `delete from password_resets where expires_at <= $1`, now)
	if err != nil {
		return n, fmt.Errorf("mail: cleanup resets: %w", err)
	}
	return n + tag.RowsAffected(), nil
}
