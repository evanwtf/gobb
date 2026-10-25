package board

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// The limits of private messages.
const (
	messagesPerPage     = 25
	maxSubjectRunes     = 120
	maxRecipients       = 10
	maxRecipientBytes   = 255 // a longer item cannot be the name of a member (bundle name limit)
	maxNameShownRunes   = 40  // how much of an unknown name the refusal repeats
	msgReplyPrefix      = "Re: "
	msgAddRecipient     = "Add at least one member."
	msgTooManyRecipient = "Send to at most 10 members."
	msgToSelf           = "You cannot send a message to yourself."
)

// ErrSendMany is an [ErrForbidden] that says why: the member named more than one
// recipient and does not hold pm.send_many.
var ErrSendMany = fmt.Errorf("%w: more than one recipient needs pm.send_many", ErrForbidden)

// The rights, in short. Every right is board-wide.
//
//   - pm.read lets a member open the inbox, the sent list and one message.
//     Without it [Inbox], [Sent], [PeekMessage] and [ReplyDraft] give
//     [ErrForbidden], and [UnreadMessages] gives 0.
//   - pm.send lets a member send, and reply. Without it [SendMessage] and
//     [ReplyDraft] give [ErrForbidden]. pm.send_many allows more than one
//     recipient.
//   - pm.delete lets a member delete a message from the member's own view.
//
// A party to a message is its author while author_deleted is false, or a
// recipient while is_deleted is false. A member who is not a party to a message
// gets [ErrNotFound], the same as for a message that does not exist, before any
// right is read for that message. The answer for a right that the member lacks
// does not depend on the ID, so it tells nothing about a message.

// MessageRow is one line of the inbox or the sent list.
type MessageRow struct {
	ID      int64
	Subject string
	From    Author
	// To lists the recipients of kind "to". A blind-copy recipient is not in it.
	To     []Author
	At     time.Time
	Unread bool
}

// MessageList is one page of the inbox or the sent list, newest first.
type MessageList struct {
	Rows        []MessageRow
	Page, Pages int
}

// Message is one message as one party sees it.
type Message struct {
	ID      int64
	Subject string
	// From has ID 0 when the author is gone. The name stays, with no link.
	From Author
	To   []Author
	// Bcc is filled for the author, with every blind-copy recipient, and for a
	// blind-copy recipient, with that recipient only. It is empty for anyone else.
	Bcc []Author
	At  time.Time
	// Tree is the body. Signature is the signature of the author, or nil when the
	// message does not carry it, or the author is gone or has none.
	Tree, Signature *markup.Node
	// Flags are the flags that made Tree.
	Flags markup.Options
	// Mine is true when the viewer is the author.
	Mine bool
}

// MessageDraft is what a writer sends, and what a reply form starts with.
type MessageDraft struct {
	// To is the list of names, separated by commas.
	To      string
	Subject string
	// Source is the Markdown text of the message.
	Source string
	// IP is the address of the writer. SendMessage stores it, and refuses a draft
	// without it.
	IP netip.Addr
}

// mayRead reports whether v is a member with the right to read messages.
func mayRead(v Viewer) bool { return v.UserID != 0 && v.Perms.Has("pm.read", 0) }

// messageRowCols are the columns of a list row: the message, its author and its time.
const messageRowCols = `p.id, p.subject, coalesce(p.author_id, 0), coalesce(au.username, p.author_name), p.created_at`

// Inbox returns page of the messages that v received and did not delete, newest
// first, 25 a page. A page past the last gives [ErrNotFound]. A member without
// pm.read gets [ErrForbidden].
func Inbox(ctx context.Context, q db.Querier, v Viewer, page int) (MessageList, error) {
	return messageList(ctx, q, v, page, true)
}

// Sent returns page of the messages that v wrote and did not delete, newest
// first, 25 a page. Rows are never unread. A page past the last gives
// [ErrNotFound]. A member without pm.read gets [ErrForbidden].
func Sent(ctx context.Context, q db.Querier, v Viewer, page int) (MessageList, error) {
	return messageList(ctx, q, v, page, false)
}

func messageList(ctx context.Context, q db.Querier, v Viewer, page int, inbox bool) (MessageList, error) {
	if !mayRead(v) {
		return MessageList{}, ErrForbidden
	}
	from, where, unread := `pms p join pm_recipients r on r.pm_id = p.id`, `r.user_id = $1 and not r.is_deleted`, `not r.is_read`
	if !inbox {
		from, where, unread = `pms p`, `p.author_id = $1 and not p.author_deleted`, `false`
	}
	var n int
	if err := q.QueryRow(ctx, `select count(*) from `+from+` where `+where, v.UserID).Scan(&n); err != nil {
		return MessageList{}, fmt.Errorf("board: count messages: %w", err)
	}
	out := MessageList{Page: page, Pages: pageCount(n, messagesPerPage)}
	if page < 1 || page > out.Pages {
		return MessageList{}, ErrNotFound
	}
	rows, err := q.Query(ctx, `
select `+messageRowCols+`, `+unread+`
  from `+from+` left join users au on au.id = p.author_id
 where `+where+`
 order by p.id desc
 limit $2 offset $3`, v.UserID, messagesPerPage, (page-1)*messagesPerPage)
	if err != nil {
		return MessageList{}, fmt.Errorf("board: read messages: %w", err)
	}
	defer rows.Close()
	index := map[int64]int{}
	var ids []int64
	for rows.Next() {
		var r MessageRow
		if err := rows.Scan(&r.ID, &r.Subject, &r.From.ID, &r.From.Name, &r.At, &r.Unread); err != nil {
			return MessageList{}, fmt.Errorf("board: read messages: %w", err)
		}
		index[r.ID] = len(out.Rows)
		ids = append(ids, r.ID)
		out.Rows = append(out.Rows, r)
	}
	if err := rows.Err(); err != nil {
		return MessageList{}, fmt.Errorf("board: read messages: %w", err)
	}
	rows.Close()
	if len(ids) == 0 {
		return out, nil
	}
	// Only the recipients of kind "to" go in a list. The blind copies never do.
	rcpt, err := q.Query(ctx, `
select r.pm_id, u.id, u.username
  from pm_recipients r join users u on u.id = r.user_id
 where r.pm_id = any($1) and r.kind = 'to'
 order by r.pm_id, u.username_clean, u.id`, ids)
	if err != nil {
		return MessageList{}, fmt.Errorf("board: read recipients: %w", err)
	}
	defer rcpt.Close()
	for rcpt.Next() {
		var id int64
		var a Author
		if err := rcpt.Scan(&id, &a.ID, &a.Name); err != nil {
			return MessageList{}, fmt.Errorf("board: read recipients: %w", err)
		}
		i := index[id]
		out.Rows[i].To = append(out.Rows[i].To, a)
	}
	if err := rcpt.Err(); err != nil {
		return MessageList{}, fmt.Errorf("board: read recipients: %w", err)
	}
	return out, nil
}

// UnreadMessages counts the messages in the inbox of v that v has not opened.
// A guest, and a member without pm.read, get 0.
func UnreadMessages(ctx context.Context, q db.Querier, v Viewer) (int, error) {
	if !mayRead(v) {
		return 0, nil
	}
	var n int
	err := q.QueryRow(ctx, `select count(*) from pm_recipients where user_id = $1 and not is_deleted and not is_read`,
		v.UserID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("board: count unread messages: %w", err)
	}
	return n, nil
}

// storedMessage is a message row with the standing of one viewer.
type storedMessage struct {
	id                          int64
	authorID                    int64 // 0 when gone
	authorName                  string
	subject, source             string
	at                          time.Time
	tree                        []byte
	markdown, smilies, autolink bool
	sig                         bool
	// asAuthor and asRecipient say in which way the viewer is a party.
	asAuthor, asRecipient bool
	kind                  string // the kind of the viewer's recipient row
}

// loadParty reads message id as v may see it. It gives [ErrNotFound] when v is
// not a party. The party rule is in the query, so a message of someone else is
// neither loaded nor locked. When lock is true, it locks the message row to the
// end of the transaction.
func loadParty(ctx context.Context, q db.Querier, v Viewer, id int64, lock bool) (storedMessage, error) {
	suffix := ""
	if lock {
		suffix = ` for no key update of p`
	}
	var m storedMessage
	var authorDeleted bool
	var rKind *string
	var rDeleted *bool
	err := q.QueryRow(ctx, `
select p.id, coalesce(p.author_id, 0), coalesce(au.username, p.author_name), p.subject, p.body_source,
       p.created_at, p.body_tree, p.enable_markdown, p.enable_smilies, p.enable_autolink, p.enable_sig,
       p.author_deleted, r.kind, r.is_deleted
  from pms p
  left join users au on au.id = p.author_id
  left join pm_recipients r on r.pm_id = p.id and r.user_id = $2
 where p.id = $1
   and ((p.author_id = $2 and not p.author_deleted) or (r.user_id is not null and not r.is_deleted))`+suffix, id, v.UserID).Scan(
		&m.id, &m.authorID, &m.authorName, &m.subject, &m.source, &m.at, &m.tree,
		&m.markdown, &m.smilies, &m.autolink, &m.sig, &authorDeleted, &rKind, &rDeleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return storedMessage{}, ErrNotFound
	}
	if err != nil {
		return storedMessage{}, fmt.Errorf("board: read message %d: %w", id, err)
	}
	m.asAuthor = v.UserID != 0 && m.authorID == v.UserID && !authorDeleted
	m.asRecipient = rKind != nil && rDeleted != nil && !*rDeleted
	if rKind != nil {
		m.kind = *rKind
	}
	if !m.asAuthor && !m.asRecipient {
		return storedMessage{}, ErrNotFound
	}
	return m, nil
}

// PeekMessage returns message id to a party and writes nothing. A viewer who is
// not a party gets [ErrNotFound]. A member without pm.read gets [ErrForbidden],
// whatever the ID. A page that a person opens calls it, and then
// [MarkMessageRead] once the page is built. A request that is not a person opening
// the page (an image load, a prefetch) calls only this one.
//
// Bcc holds every blind-copy recipient for the author, only the viewer for a
// blind-copy recipient, and nothing for anyone else.
func PeekMessage(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64) (Message, error) {
	if !mayRead(v) {
		return Message{}, ErrForbidden
	}
	return readMessage(ctx, pool, v, id)
}

// MarkMessageRead marks message id read for v, when v is a recipient who has not
// deleted it. It does nothing for any other viewer, and gives [ErrForbidden] to a
// member without pm.read.
func MarkMessageRead(ctx context.Context, q db.Querier, v Viewer, id int64) error {
	if !mayRead(v) {
		return ErrForbidden
	}
	_, err := q.Exec(ctx, `update pm_recipients set is_read = true where pm_id = $1 and user_id = $2 and not is_deleted and not is_read`,
		id, v.UserID)
	if err != nil {
		return fmt.Errorf("board: mark message %d read: %w", id, err)
	}
	return nil
}

func readMessage(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64) (Message, error) {
	m, err := loadParty(ctx, pool, v, id, false)
	if err != nil {
		return Message{}, err
	}
	out := Message{
		ID: m.id, Subject: m.subject, From: Author{ID: m.authorID, Name: m.authorName}, At: m.at,
		Tree:  decodeTree(m.tree, "message", m.id),
		Flags: markup.Options{Markdown: m.markdown, Smilies: m.smilies, Autolink: m.autolink},
		Mine:  m.asAuthor,
	}
	rows, err := pool.Query(ctx, `
select r.kind, u.id, u.username
  from pm_recipients r join users u on u.id = r.user_id
 where r.pm_id = $1
 order by u.username_clean, u.id`, id)
	if err != nil {
		return Message{}, fmt.Errorf("board: read recipients of message %d: %w", id, err)
	}
	defer rows.Close()
	for rows.Next() {
		var kind string
		var a Author
		if err := rows.Scan(&kind, &a.ID, &a.Name); err != nil {
			return Message{}, fmt.Errorf("board: read recipients of message %d: %w", id, err)
		}
		switch {
		case kind == "to":
			out.To = append(out.To, a)
		case m.asAuthor, a.ID == v.UserID:
			// A blind copy shows to the author, and to the recipient it went to.
			out.Bcc = append(out.Bcc, a)
		}
	}
	if err := rows.Err(); err != nil {
		return Message{}, fmt.Errorf("board: read recipients of message %d: %w", id, err)
	}
	rows.Close()
	if m.sig && m.authorID != 0 {
		var sig []byte
		err := pool.QueryRow(ctx, `select signature_tree from users where id = $1`, m.authorID).Scan(&sig)
		if err != nil {
			return Message{}, fmt.Errorf("board: read signature of user %d: %w", m.authorID, err)
		}
		if len(sig) > 0 {
			if n := decodeTree(sig, "signature of user", m.authorID); len(n.Children) > 0 {
				out.Signature = n
			}
		}
	}
	return out, nil
}

// DeleteMessage deletes message id from the view of v. The recipient row of v
// gets is_deleted, and the message gets author_deleted when v wrote it. Both
// happen when both apply. No row is removed, so the other parties keep the
// message. A viewer who is not a party gets [ErrNotFound]. A party without
// pm.delete gets [ErrForbidden].
func DeleteMessage(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64) error {
	if v.UserID == 0 {
		return ErrForbidden
	}
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		m, err := loadParty(ctx, tx, v, id, true)
		if err != nil {
			return err
		}
		if !v.Perms.Has("pm.delete", 0) {
			return ErrForbidden
		}
		if m.asRecipient {
			if _, err := tx.Exec(ctx, `update pm_recipients set is_deleted = true where pm_id = $1 and user_id = $2`,
				id, v.UserID); err != nil {
				return fmt.Errorf("board: delete message %d for user %d: %w", id, v.UserID, err)
			}
		}
		if m.asAuthor {
			if _, err := tx.Exec(ctx, `update pms set author_deleted = true where id = $1`, id); err != nil {
				return fmt.Errorf("board: delete message %d for its author: %w", id, err)
			}
		}
		return nil
	})
}

// ReplyDraft returns the start of a reply to message id. When the viewer is the
// author, To holds the names of the "to" recipients, separated by commas, without
// the blind-copy recipients and without the viewer. Else To is the name of the
// author, and it is empty when the author is gone. The subject is "Re: " and the
// subject of the message, unless that starts with "Re: " in any case already, and
// at most 120 characters. With quote, Source is "> **{author}** wrote:" and the
// text of the message with "> " before each line. A viewer who is not a party
// gets [ErrNotFound]. A member without pm.read or pm.send gets [ErrForbidden].
func ReplyDraft(ctx context.Context, q db.Querier, v Viewer, id int64, quote bool) (MessageDraft, error) {
	if !mayRead(v) || !v.Perms.Has("pm.send", 0) {
		return MessageDraft{}, ErrForbidden
	}
	m, err := loadParty(ctx, q, v, id, false)
	if err != nil {
		return MessageDraft{}, err
	}
	d := MessageDraft{Subject: m.subject}
	switch {
	case m.asAuthor:
		// A reply to one's own message goes on to the people it went to. A blind
		// copy is not among them.
		if d.To, err = visibleRecipientNames(ctx, q, id, v.UserID); err != nil {
			return MessageDraft{}, err
		}
	case m.authorID != 0:
		d.To = m.authorName
	}
	if len(d.Subject) < len(msgReplyPrefix) || !strings.EqualFold(d.Subject[:len(msgReplyPrefix)], msgReplyPrefix) {
		d.Subject = msgReplyPrefix + d.Subject
	}
	if utf8.RuneCountInString(d.Subject) > maxSubjectRunes {
		d.Subject = string([]rune(d.Subject)[:maxSubjectRunes])
	}
	if quote {
		d.Source = quoteBlock("**"+escapeMarkdown(m.authorName)+"** wrote:", m.source)
	}
	return d, nil
}

// visibleRecipientNames returns the names of the recipients of kind "to" of
// message id, except the member self, as a To field: separated by commas, in the
// order that the message shows them.
func visibleRecipientNames(ctx context.Context, q db.Querier, id, self int64) (string, error) {
	rows, err := q.Query(ctx, `
select u.username
  from pm_recipients r join users u on u.id = r.user_id
 where r.pm_id = $1 and r.kind = 'to' and r.user_id <> $2
 order by u.username_clean, u.id`, id, self)
	if err != nil {
		return "", fmt.Errorf("board: read recipients of message %d: %w", id, err)
	}
	defer rows.Close()
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return "", fmt.Errorf("board: read recipients of message %d: %w", id, err)
		}
		names = append(names, n)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("board: read recipients of message %d: %w", id, err)
	}
	return strings.Join(names, ", "), nil
}

// cleanSubject trims a subject and applies the rules of titles: 1 to 120
// characters, valid UTF-8, and no control character.
func cleanSubject(s string) (string, error) { return cleanLine(s, "subject", maxSubjectRunes) }

// splitRecipients splits the To field into names: at commas, trimmed, with
// empty items dropped, and each name once by its clean form. It returns the
// names as written, and their clean forms. An item that cannot be a member name
// keeps an empty clean form.
func splitRecipients(to string) (names, cleans []string) {
	seen := map[string]bool{}
	for item := range strings.SplitSeq(to, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		clean := ""
		if utf8.ValidString(item) && len(item) <= maxRecipientBytes {
			clean = account.CleanUsername(item)
		}
		if clean != "" {
			if seen[clean] {
				continue
			}
			seen[clean] = true
		}
		names, cleans = append(names, item), append(cleans, clean)
		if len(names) > maxRecipients+1 {
			break // more than the limit; the rest does not change the answer
		}
	}
	return names, cleans
}

// recipientNames reads the To field. A name may hold a comma, so the whole
// trimmed field comes first: when it is the clean name of an active member, it
// is the one recipient. Otherwise the field splits at the commas (see
// [splitRecipients]). A list that is also the name of a member, such as
// "Smith, Alice, bob" for a member of that name, reads as that member.
func recipientNames(ctx context.Context, q db.Querier, to string) (names, cleans []string, err error) {
	if whole := strings.TrimSpace(to); strings.Contains(whole, ",") && utf8.ValidString(whole) && len(whole) <= maxRecipientBytes {
		clean := account.CleanUsername(whole)
		var ok bool
		if err := q.QueryRow(ctx, `select exists (select 1 from users where username_clean = $1 and state = 'active')`, clean).Scan(&ok); err != nil {
			return nil, nil, fmt.Errorf("board: look up recipient: %w", err)
		}
		if ok && clean != "" {
			return []string{whole}, []string{clean}, nil
		}
	}
	names, cleans = splitRecipients(to)
	return names, cleans, nil
}

// shownName cuts a name that a writer typed down to what a refusal repeats.
func shownName(s string) string {
	s = strings.ToValidUTF8(s, "�")
	if utf8.RuneCountInString(s) > maxNameShownRunes {
		s = string([]rune(s)[:maxNameShownRunes]) + "…"
	}
	return s
}

// SendMessage stores a new message from v and returns its ID. Each recipient gets
// a row of kind "to". Nobody is stored as a blind copy by this function.
//
// The checks, in order: the right to send (pm.send, else [ErrForbidden]); the To
// field (field "to"; more than one name without pm.send_many is [ErrSendMany], an [ErrForbidden]);
// the subject (field "subject"); then the body (field "body"; see
// [ParseMessage]). A message whose author, recipients, subject and text equal
// those of one that the author sent less than 60 seconds before now, and kept,
// is not stored again: the earlier ID comes back.
func SendMessage(ctx context.Context, pool *pgxpool.Pool, v Viewer, d MessageDraft, now time.Time) (int64, error) {
	if v.UserID == 0 || !v.Perms.Has("pm.send", 0) {
		return 0, ErrForbidden
	}
	if !d.IP.IsValid() {
		return 0, errors.New("board: a write needs the address of the writer")
	}
	names, cleans, err := recipientNames(ctx, pool, d.To)
	if err != nil {
		return 0, err
	}
	switch {
	case len(names) == 0:
		return 0, inputError("to", msgAddRecipient)
	case len(names) > maxRecipients:
		return 0, inputError("to", msgTooManyRecipient)
	case len(names) > 1 && !v.Perms.Has("pm.send_many", 0):
		return 0, ErrSendMany
	}
	// The parse costs the most, so it runs before the lock. Its error waits for
	// its place in the order of the answers.
	body, parseErr := ParseMessage(ctx, pool, v, d.Source)

	var msgID int64
	err = inTx(ctx, pool, func(tx pgx.Tx) error {
		// The lock on the author's row makes two sends from one member run one after
		// the other, so the second finds the first when it looks for a duplicate.
		var author string
		err := tx.QueryRow(ctx, `select username from users where id = $1 and state = 'active' for no key update`,
			v.UserID).Scan(&author)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrForbidden
		}
		if err != nil {
			return fmt.Errorf("board: lock member %d: %w", v.UserID, err)
		}
		if err := refuseBanned(ctx, tx, v.UserID, now); err != nil {
			return err
		}
		ids, err := resolveRecipients(ctx, tx, v, names, cleans)
		if err != nil {
			return err
		}
		subject, err := cleanSubject(d.Subject)
		if err != nil {
			return err
		}
		if parseErr != nil {
			return parseErr
		}
		if msgID, err = findDuplicateMessage(ctx, tx, v.UserID, ids, subject, d.Source, now); err != nil || msgID != 0 {
			return err
		}
		err = tx.QueryRow(ctx, `
insert into pms (author_id, author_name, author_ip, created_at, subject, body_source, body_tree, body_text,
                 enable_markdown, enable_smilies, enable_autolink, enable_sig, author_deleted)
values ($1, $2, $3, $4, $5, $6, $7::jsonb, $8, $9, $10, $11, true, false)
returning id`,
			v.UserID, author, inetArg(d.IP), now, subject, d.Source, string(body.JSON), body.Text,
			body.Flags.Markdown, body.Flags.Smilies, body.Flags.Autolink).Scan(&msgID)
		if err != nil {
			return fmt.Errorf("board: insert message: %w", err)
		}
		if _, err := tx.Exec(ctx, `insert into pm_recipients (pm_id, user_id, kind) select $1, u, 'to' from unnest($2::bigint[]) as u`,
			msgID, ids); err != nil {
			return fmt.Errorf("board: insert recipients: %w", err)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return msgID, nil
}

// resolveRecipients turns the names into member IDs, sorted. A name that no
// active member bears, and the name of v, are errors on the field "to". The
// first fault in the order of the names wins.
func resolveRecipients(ctx context.Context, q db.Querier, v Viewer, names, cleans []string) ([]int64, error) {
	rows, err := q.Query(ctx, `select username_clean, id from users where username_clean = any($1) and state = 'active'`, cleans)
	if err != nil {
		return nil, fmt.Errorf("board: look up recipients: %w", err)
	}
	defer rows.Close()
	byClean := map[string]int64{}
	for rows.Next() {
		var clean string
		var id int64
		if err := rows.Scan(&clean, &id); err != nil {
			return nil, fmt.Errorf("board: look up recipients: %w", err)
		}
		byClean[clean] = id
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: look up recipients: %w", err)
	}
	var ids []int64
	for i, name := range names {
		id, ok := byClean[cleans[i]]
		if !ok || cleans[i] == "" {
			return nil, inputError("to", "No member named “"+shownName(name)+"”.")
		}
		if id == v.UserID {
			return nil, inputError("to", msgToSelf)
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return slices.Compact(ids), nil
}

// findDuplicateMessage returns the ID of a message that the author kept and sent
// in the 60 seconds before now with the same recipients, subject and text, or 0.
// recipients must be sorted. The caller holds the lock on the author.
func findDuplicateMessage(ctx context.Context, tx pgx.Tx, authorID int64, recipients []int64, subject, source string, now time.Time) (int64, error) {
	var id int64
	err := tx.QueryRow(ctx, `
select p.id
  from pms p
 where p.author_id = $1 and not p.author_deleted and p.created_at > $2
   and p.subject = $3 and p.body_source = $4
   and (select coalesce(array_agg(r.user_id order by r.user_id), '{}') from pm_recipients r where r.pm_id = p.id) = $5::bigint[]
 order by p.id
 limit 1`, authorID, now.Add(-duplicateWindow), subject, source, recipients).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("board: look for a duplicate message: %w", err)
	}
	return id, nil
}
