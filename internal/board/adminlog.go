package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
)

// The admin area's share of the board: the one helper that every write of an
// admin page goes through, and the reader of the moderation log.

// logPerPage is the count of rows on one page of the log.
const logPerPage = 50

// Change is what an admin write did, for its mod_log row.
type Change struct {
	// Action names the write, such as "settings.update".
	Action string
	// TargetType and TargetID name what the write changed, such as "forum" and 3.
	TargetType string
	TargetID   int64
	// Details are the keys of the row's details. [AdminWrite] adds "via": "web".
	Details map[string]any
}

// ErrNoChange means that an admin write found nothing to change. [AdminWrite]
// rolls back and writes no mod_log row.
var ErrNoChange = errors.New("board: nothing to change")

// AdminWrite runs write in one transaction for the admin v, then writes one
// mod_log row for the [Change] that write returns, with v as the actor and
// {"via":"web"} added to the details, in the same transaction. A viewer
// without the board-wide admin right gets [ErrForbidden], and write does not
// run. An error from write rolls everything back and comes back as it is.
func AdminWrite(ctx context.Context, pool *pgxpool.Pool, v Viewer, now time.Time, write func(tx pgx.Tx) (Change, error)) error {
	if v.UserID == 0 || !v.Perms.IsAdmin() {
		return ErrForbidden
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		c, err := write(tx)
		if err != nil {
			return err
		}
		details := map[string]any{}
		for k, val := range c.Details {
			details[k] = val
		}
		details["via"] = "web"
		b, err := json.Marshal(details)
		if err != nil {
			return fmt.Errorf("board: encode log details: %w", err)
		}
		return logMod(ctx, tx, v, c.Action, c.TargetType, c.TargetID, string(b), now)
	})
}

// LogFilter narrows the log. A zero field matches every row.
type LogFilter struct {
	// Action matches the action exactly.
	Action string
	// Actor is a member ID, or [LogNoActor] for the rows with no actor: the
	// command line, a reset link, and members who are gone.
	Actor int64
	// TargetType and TargetID match the target. TargetID needs TargetType.
	TargetType string
	TargetID   int64
}

// LogNoActor is the [LogFilter.Actor] of the rows with no actor.
const LogNoActor = -1

// LogDetail is one key of a row's details, with its value as text.
type LogDetail struct {
	Key, Value string
}

// LogEntry is one row of the moderation log.
type LogEntry struct {
	ID int64
	// Actor is the member who acted. Its ID is 0 for a row with no actor.
	Actor Author
	// Via is the "via" of the details: "web", "cli" or "email", or empty.
	Via        string
	Action     string
	TargetType string
	TargetID   int64
	// TargetName is the username, the topic title or the forum name of the
	// target, or empty when the target is of another type or is gone.
	TargetName string
	// Details holds the other keys of the details, sorted by key.
	Details []LogDetail
	At      time.Time
	// UndoUntil is the end of the undo window, and UndoneAt the time of the
	// undo. Each is nil when the row has none.
	UndoUntil, UndoneAt *time.Time
}

// LogPage is one page of the log, newest first.
type LogPage struct {
	Entries     []LogEntry
	Page, Pages int
}

// ModLog returns page page of the moderation log, newest first, 50 rows a
// page, filtered by f. A page past the last is [ErrNotFound]; page 1 of an
// empty log is not. It does not check the viewer: the caller is the admin
// area, behind its gate.
func ModLog(ctx context.Context, q db.Querier, f LogFilter, page int) (LogPage, error) {
	// The casts fix the types of the parameters: without them, "$2 = 0" makes $2
	// an int4, and an ID above 2^31-1 fails to encode.
	where := `($1::text = '' or l.action = $1::text)
   and ($2::bigint = 0 or ($2::bigint = -1 and l.actor_id is null) or l.actor_id = $2::bigint)
   and ($3::text = '' or l.target_type = $3::text)
   and ($4::bigint = 0 or l.target_id = $4::bigint)`
	args := []any{f.Action, f.Actor, f.TargetType, f.TargetID}
	var n int
	if err := q.QueryRow(ctx, `select count(*) from mod_log l where `+where, args...).Scan(&n); err != nil {
		return LogPage{}, fmt.Errorf("board: count the log: %w", err)
	}
	out := LogPage{Page: page, Pages: pageCount(n, logPerPage)}
	if page < 1 || page > out.Pages {
		return LogPage{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `
select l.id, coalesce(l.actor_id, 0), coalesce(a.username, ''), l.action, l.target_type, l.target_id,
       coalesce(case l.target_type when 'user' then tu.username when 'topic' then tt.title when 'forum' then tf.name end, ''),
       l.details, l.created_at, l.undo_until, l.undone_at
  from mod_log l
  left join users a on a.id = l.actor_id
  left join users tu on l.target_type = 'user' and tu.id = l.target_id
  left join topics tt on l.target_type = 'topic' and tt.id = l.target_id
  left join forums tf on l.target_type = 'forum' and tf.id = l.target_id
 where `+where+`
 order by l.id desc
 limit $5 offset $6`, append(args, logPerPage, (page-1)*logPerPage)...)
	if err != nil {
		return LogPage{}, fmt.Errorf("board: read the log: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			e       LogEntry
			details map[string]any
		)
		if err := rows.Scan(&e.ID, &e.Actor.ID, &e.Actor.Name, &e.Action, &e.TargetType, &e.TargetID,
			&e.TargetName, &details, &e.At, &e.UndoUntil, &e.UndoneAt); err != nil {
			return LogPage{}, fmt.Errorf("board: read the log: %w", err)
		}
		e.Via, e.Details = splitDetails(details)
		out.Entries = append(out.Entries, e)
	}
	if err := rows.Err(); err != nil {
		return LogPage{}, fmt.Errorf("board: read the log: %w", err)
	}
	return out, nil
}

// splitDetails takes "via" out of the details of a row and returns the other
// keys sorted, each value as text: a string as it is, anything else as JSON.
func splitDetails(details map[string]any) (via string, rest []LogDetail) {
	for k, v := range details {
		text, ok := v.(string)
		if !ok {
			b, err := json.Marshal(v)
			if err != nil {
				continue
			}
			text = string(b)
		}
		if k == "via" {
			via = text
			continue
		}
		rest = append(rest, LogDetail{Key: k, Value: text})
	}
	sort.Slice(rest, func(i, j int) bool { return rest[i].Key < rest[j].Key })
	return via, rest
}

// LogChoices returns the actions and the target types that the log holds, each
// sorted, for the filters of the log page.
func LogChoices(ctx context.Context, q db.Querier) (actions, types []string, err error) {
	for _, c := range []struct {
		col string
		out *[]string
	}{{"action", &actions}, {"target_type", &types}} {
		rows, err := q.Query(ctx, `select distinct `+c.col+` from mod_log where `+c.col+` <> '' order by 1`)
		if err != nil {
			return nil, nil, fmt.Errorf("board: list the log choices: %w", err)
		}
		if *c.out, err = pgx.CollectRows(rows, pgx.RowTo[string]); err != nil {
			return nil, nil, fmt.Errorf("board: list the log choices: %w", err)
		}
	}
	return actions, types, nil
}
