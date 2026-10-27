package board

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/db"
)

// A conversation is a first message and the replies to it, in pms.conversation_id.
// A member sees the messages of a conversation that the member is a party to, and
// only those: a blind-copy recipient sees the messages that went to them, and a
// message the member deleted is gone from their view. A conversation with no such
// message does not exist for the member.

// ConversationRow is one line of the list of conversations.
type ConversationRow struct {
	// ID is the conversation ID. Href opens the conversation at its last message.
	ID int64
	// Subject is the subject of the first message the viewer sees.
	Subject string
	// With is everyone in the visible messages but the viewer: the authors and the
	// recipients of kind "to", in name order.
	With []Author
	// Last is the last message the viewer sees: its ID, author and time.
	LastID   int64
	LastFrom Author
	LastAt   time.Time
	// Messages counts the messages the viewer sees; Unread, those of them unread.
	Messages, Unread int
}

// ConversationList is one page of the conversations of a member, the one with
// the newest message first.
type ConversationList struct {
	Rows        []ConversationRow
	Page, Pages int
}

// visibleSQL selects the messages that member $1 is a party to, with the
// conversation and whether the message is unread for the member.
const visibleSQL = `
select p.id, p.conversation_id as cid,
       coalesce(r.user_id is not null and not r.is_deleted and not r.is_read, false) as unread
  from pms p
  left join pm_recipients r on r.pm_id = p.id and r.user_id = $1
 where (p.author_id = $1 and not p.author_deleted) or (r.user_id is not null and not r.is_deleted)`

// Conversations returns page of the conversations of v, 25 a page. A page past
// the last gives [ErrNotFound]; page 1 of an empty list is empty. A member
// without pm.read gets [ErrForbidden].
func Conversations(ctx context.Context, q db.Querier, v Viewer, page int) (ConversationList, error) {
	if !mayRead(v) {
		return ConversationList{}, ErrForbidden
	}
	var n int
	if err := q.QueryRow(ctx, `select count(distinct cid) from (`+visibleSQL+`) v`, v.UserID).Scan(&n); err != nil {
		return ConversationList{}, fmt.Errorf("board: count conversations: %w", err)
	}
	out := ConversationList{Page: page, Pages: pageCount(n, messagesPerPage)}
	if page < 1 || page > out.Pages {
		return ConversationList{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `
with v as (`+visibleSQL+`),
c as (
  select cid, min(id) as first_id, max(id) as last_id, count(*) as n, count(*) filter (where unread) as unread
    from v group by cid
)
select c.cid, f.subject, c.last_id, coalesce(l.author_id, 0), coalesce(lu.username, l.author_name), l.created_at,
       c.n, c.unread
  from c
  join pms f on f.id = c.first_id
  join pms l on l.id = c.last_id
  left join users lu on lu.id = l.author_id
 order by c.last_id desc
 limit $2 offset $3`, v.UserID, messagesPerPage, (page-1)*messagesPerPage)
	if err != nil {
		return ConversationList{}, fmt.Errorf("board: read conversations: %w", err)
	}
	defer rows.Close()
	index := map[int64]int{}
	var cids []int64
	for rows.Next() {
		var r ConversationRow
		if err := rows.Scan(&r.ID, &r.Subject, &r.LastID, &r.LastFrom.ID, &r.LastFrom.Name, &r.LastAt,
			&r.Messages, &r.Unread); err != nil {
			return ConversationList{}, fmt.Errorf("board: read conversations: %w", err)
		}
		index[r.ID] = len(out.Rows)
		cids = append(cids, r.ID)
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return ConversationList{}, fmt.Errorf("board: read conversations: %w", err)
	}
	rows.Close()
	if len(cids) == 0 {
		return out, nil
	}
	// The people of each conversation, from the messages the viewer sees only. A
	// blind copy never names anyone.
	people, err := q.Query(ctx, `
with v as (`+visibleSQL+`),
p as (
  select v.cid, m.author_id as uid, m.author_name as name from v join pms m on m.id = v.id
  union
  select v.cid, r.user_id, '' from v join pm_recipients r on r.pm_id = v.id and r.kind = 'to'
)
select distinct p.cid, coalesce(p.uid, 0), coalesce(u.username, p.name), coalesce(u.username_clean, lower(p.name))
  from p left join users u on u.id = p.uid
 where p.cid = any($2) and p.uid is distinct from $1
 order by 1, 4, 2`, v.UserID, cids)
	if err != nil {
		return ConversationList{}, fmt.Errorf("board: read the people of conversations: %w", err)
	}
	defer people.Close()
	for people.Next() {
		var cid int64
		var a Author
		var clean string
		if err := people.Scan(&cid, &a.ID, &a.Name, &clean); err != nil {
			return ConversationList{}, fmt.Errorf("board: read the people of conversations: %w", err)
		}
		i := index[cid]
		out.Rows[i].With = append(out.Rows[i].With, a)
	}
	if err := people.Err(); err != nil {
		return ConversationList{}, fmt.Errorf("board: read the people of conversations: %w", err)
	}
	return out, nil
}

// Conversation is one page of a conversation as one member sees it, oldest
// message first.
type Conversation struct {
	ID int64
	// Subject is the subject of the first message the viewer sees.
	Subject  string
	Messages []Message
	// Unread holds the IDs of the messages on the page that are unread for the
	// viewer. [MarkMessagesRead] takes them.
	Unread      []int64
	Page, Pages int
	// LastID is the last message of the whole conversation that the viewer sees.
	// A reply answers it.
	LastID int64
}

// seenMessage is a message that a member sees, and whether it is unread for them.
type seenMessage struct {
	id     int64
	unread bool
}

// ReadConversation returns the conversation of message id as v sees it. page 0
// is the page that holds message id; a page past the last gives [ErrNotFound]. A
// viewer who is not a party to message id gets [ErrNotFound], and a member
// without pm.read gets [ErrForbidden], whatever the ID. It writes nothing.
func ReadConversation(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, page int) (Conversation, error) {
	if !mayRead(v) {
		return Conversation{}, ErrForbidden
	}
	m, err := loadParty(ctx, pool, v, id, false)
	if err != nil {
		return Conversation{}, err
	}
	rows, err := pool.Query(ctx, `select v.id, v.unread from (`+visibleSQL+`) v where v.cid = $2 order by v.id`,
		v.UserID, m.conversationID)
	if err != nil {
		return Conversation{}, fmt.Errorf("board: read conversation %d: %w", m.conversationID, err)
	}
	ids, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (seenMessage, error) {
		var r seenMessage
		return r, row.Scan(&r.id, &r.unread)
	})
	if err != nil {
		return Conversation{}, fmt.Errorf("board: read conversation %d: %w", m.conversationID, err)
	}
	out := Conversation{ID: m.conversationID, Pages: pageCount(len(ids), messagesPerPage), LastID: ids[len(ids)-1].id}
	if page == 0 {
		at := slices.IndexFunc(ids, func(r seenMessage) bool { return r.id == id })
		page = at/messagesPerPage + 1
	}
	if page < 1 || page > out.Pages {
		return Conversation{}, ErrNotFound
	}
	out.Page = page
	first, err := readMessage(ctx, pool, v, ids[0].id)
	if err != nil {
		return Conversation{}, err
	}
	out.Subject = first.Subject
	end := min(page*messagesPerPage, len(ids))
	for _, r := range ids[(page-1)*messagesPerPage : end] {
		msg := first
		if r.id != first.ID {
			if msg, err = readMessage(ctx, pool, v, r.id); err != nil {
				return Conversation{}, err
			}
		}
		out.Messages = append(out.Messages, msg)
		if r.unread {
			out.Unread = append(out.Unread, r.id)
		}
	}
	return out, nil
}

// MarkMessagesRead marks the messages ids read for v, where v is a recipient who
// has not deleted them. It does nothing for any other message, and gives
// [ErrForbidden] to a member without pm.read.
func MarkMessagesRead(ctx context.Context, q db.Querier, v Viewer, ids []int64) error {
	if !mayRead(v) {
		return ErrForbidden
	}
	if len(ids) == 0 {
		return nil
	}
	_, err := q.Exec(ctx, `update pm_recipients set is_read = true
 where pm_id = any($1) and user_id = $2 and not is_deleted and not is_read`, ids, v.UserID)
	if err != nil {
		return fmt.Errorf("board: mark messages read: %w", err)
	}
	return nil
}

// threadKey is what a reply shares with the message it answers: the same people
// and the same subject.
type threadKey struct {
	people  string
	subject string
}

// baseSubject is subject without its "Re:" prefixes and outer space, folded to
// lower case. reply reports whether there was a prefix.
func baseSubject(subject string) (base string, reply bool) {
	s := strings.TrimSpace(subject)
	for len(s) >= 3 && strings.EqualFold(s[:3], "re:") {
		s = strings.TrimSpace(s[3:])
		reply = true
	}
	return strings.ToLower(strings.TrimFunc(s, unicode.IsSpace)), reply
}

// ThreadMessages joins old replies to the conversations they answer. It reads
// the messages in ID order. A message whose subject starts with "Re:", and that
// starts a conversation of its own, joins the conversation of the newest earlier
// message with the same subject (without the prefixes, in any case) and the same
// people: the author and the recipients of kind "to". A message that has joined a
// conversation stays in it. It returns the count of messages that it joined, or
// would join when dryRun is true. A second run joins nothing new.
func ThreadMessages(ctx context.Context, pool *pgxpool.Pool, dryRun bool) (int, error) {
	joined := 0
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		// The lock keeps a new message from slipping in between the read and the
		// writes.
		if _, err := tx.Exec(ctx, `lock table pms in share row exclusive mode`); err != nil {
			return fmt.Errorf("board: lock messages: %w", err)
		}
		rows, err := tx.Query(ctx, `
select p.id, p.conversation_id, p.subject,
       coalesce(p.author_id::text, 'name:' || p.author_name) || '|' ||
       coalesce((select string_agg(r.user_id::text, ',' order by r.user_id)
                   from pm_recipients r where r.pm_id = p.id and r.kind = 'to'), '')
  from pms p
 order by p.id`)
		if err != nil {
			return fmt.Errorf("board: read messages: %w", err)
		}
		type msg struct {
			id, cid         int64
			subject, people string
		}
		all, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (msg, error) {
			var m msg
			return m, row.Scan(&m.id, &m.cid, &m.subject, &m.people)
		})
		if err != nil {
			return fmt.Errorf("board: read messages: %w", err)
		}
		latest := map[threadKey]int64{}
		var ids, cids []int64
		for _, m := range all {
			base, reply := baseSubject(m.subject)
			// The people are the same set whoever wrote: the author and the "to"
			// recipients, sorted.
			k := threadKey{people: peopleKey(m.people), subject: base}
			if cid, ok := latest[k]; ok && reply && m.cid == m.id {
				ids, cids = append(ids, m.id), append(cids, cid)
				m.cid = cid
			}
			latest[k] = m.cid
		}
		joined = len(ids)
		if dryRun || joined == 0 {
			return nil
		}
		if _, err := tx.Exec(ctx, `update pms p set conversation_id = j.cid
  from unnest($1::bigint[], $2::bigint[]) as j(id, cid) where p.id = j.id`, ids, cids); err != nil {
			return fmt.Errorf("board: join messages: %w", err)
		}
		return nil
	})
	return joined, err
}

// peopleKey turns "author|r1,r2" into the sorted set of everyone, so that a reply
// (author and recipient swapped) has the same key as the message it answers.
func peopleKey(s string) string {
	author, rcpts, _ := strings.Cut(s, "|")
	all := []string{author}
	if rcpts != "" {
		all = append(all, strings.Split(rcpts, ",")...)
	}
	slices.Sort(all)
	return strings.Join(slices.Compact(all), ",")
}
