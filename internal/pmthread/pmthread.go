// Package pmthread joins private-message replies to the conversations they
// answer, by people and subject. `gobb messages thread` and `gobb import
// --thread-messages` both use it, so the two follow one rule.
package pmthread

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"unicode"

	"github.com/jackc/pgx/v5"
)

// key is what a reply shares with the message it answers: the same people and
// the same subject.
type key struct {
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

// Join joins old replies to the conversations they answer, in tx. It reads the
// messages in ID order. A message whose subject starts with "Re:", and that
// starts a conversation of its own, joins the conversation of the newest earlier
// message with the same subject (without the prefixes, in any case) and the same
// people: the author and the recipients of kind "to". A message that has joined a
// conversation stays in it. A message in the conversation of a message that
// joins one moves with it, so a reply sent before the run stays with the talk.
// Join needs every conversation_id to name a message with the same or a lower
// ID that starts its own conversation. It returns the count of messages that it
// joined, or would join when dryRun is true. A second run joins nothing new.
func Join(ctx context.Context, tx pgx.Tx, dryRun bool) (int, error) {
	// The lock keeps a new message from slipping in between the read and the
	// writes.
	if _, err := tx.Exec(ctx, `lock table pms in share row exclusive mode`); err != nil {
		return 0, fmt.Errorf("pmthread: lock messages: %w", err)
	}
	rows, err := tx.Query(ctx, `
select p.id, p.conversation_id, p.subject,
       coalesce(p.author_id::text, 'name:' || p.author_name) || '|' ||
       coalesce((select string_agg(r.user_id::text, ',' order by r.user_id)
                   from pm_recipients r where r.pm_id = p.id and r.kind = 'to'), '')
  from pms p
 order by p.id`)
	if err != nil {
		return 0, fmt.Errorf("pmthread: read messages: %w", err)
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
		return 0, fmt.Errorf("pmthread: read messages: %w", err)
	}
	latest := map[key]int64{}
	// moved maps the ID of a message that joined a conversation to that
	// conversation. A later message whose conversation_id is that ID follows it.
	// The rows come in ID order and a conversation_id is never above its own
	// ID, so the first message is always seen before the rest.
	moved := map[int64]int64{}
	var ids, cids []int64
	for _, m := range all {
		base, reply := baseSubject(m.subject)
		// The people are the same set whoever wrote: the author and the "to"
		// recipients, sorted.
		k := key{people: peopleKey(m.people), subject: base}
		if cid, ok := moved[m.cid]; ok {
			ids, cids = append(ids, m.id), append(cids, cid)
			m.cid = cid
		} else if cid, ok := latest[k]; ok && reply && m.cid == m.id {
			ids, cids = append(ids, m.id), append(cids, cid)
			moved[m.id] = cid
			m.cid = cid
		}
		latest[k] = m.cid
	}
	if dryRun || len(ids) == 0 {
		return len(ids), nil
	}
	if _, err := tx.Exec(ctx, `update pms p set conversation_id = j.cid
  from unnest($1::bigint[], $2::bigint[]) as j(id, cid) where p.id = j.id`, ids, cids); err != nil {
		return 0, fmt.Errorf("pmthread: join messages: %w", err)
	}
	return len(ids), nil
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
