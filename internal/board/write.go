package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/counters"
	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/internal/postbody"
	"github.com/evanwtf/gobb/markup"
)

// The limits of the posting forms.
const (
	maxTitleRunes     = 120
	maxBodyBytes      = 60_000
	maxMessageBytes   = 60_000
	maxSignatureBytes = 2_000
	maxReasonRunes    = 255
	maxGuestNameRunes = 40
)

// duplicateWindow is how long after a post an identical post from the same
// author counts as the same post, sent twice.
const duplicateWindow = 60 * time.Second

// The messages that a page shows to the writer. Each one names the problem.
const (
	msgNoImages       = "You may not post images in this forum."
	msgNoSigImages    = "You may not use images in a signature."
	msgNoMsgImages    = "You may not put images in messages."
	msgGuestNameChars = "Use letters, digits, spaces and common punctuation only."
	msgGuestNameTaken = "That name belongs to a member. Sign in, or choose another name."
	msgGuestName      = "Enter a name of 1 to 40 characters, with no line breaks."
)

// InputError says that a field of what the writer sent is not acceptable. The
// message is for the writer, and it never holds text from the database. A page
// shows it with the form, and keeps the text that the writer sent.
type InputError struct {
	// Field is the name of the form field: "title", "body", "guest_name" or
	// "reason".
	Field   string
	Message string
}

func (e *InputError) Error() string { return "board: " + e.Field + ": " + e.Message }

func inputError(field, message string) error { return &InputError{Field: field, Message: message} }

// Body is a parsed post body: the tree that a page renders, the JSON and the
// plain text that the database stores, and the flags that made the tree.
type Body struct {
	Tree *markup.Node
	JSON []byte
	Text string
	// Flags are the flags that made the tree. SmileyList is empty.
	Flags markup.Options
}

// Draft is what a writer sends.
type Draft struct {
	// Title is the topic title. A reply ignores it, and so does an edit of a post
	// that is not the first post of its topic.
	Title string
	// Source is the Markdown text of the post.
	Source string
	// GuestName is the name of a guest. A member's write ignores it.
	GuestName string
	// IP is the address of the writer. It is stored with every new post, and a new
	// post without it is an error. For a guest it also keeps duplicates apart and
	// enforces address bans.
	IP netip.Addr
}

// parseHook, when set, runs in parseText just before the parse. A test
// sets it to hold a parse open, and to show what the database does meanwhile. It
// is nil in production.
var parseHook func()

// errStaleParse tells a write that what it parsed no longer matches the rows
// that it locked. The write starts again with fresh rows. It never leaves the
// package.
var errStaleParse = errors.New("board: the parsed text is stale")

// msgTryAgain is the answer to a write that the rows changed under twice.
const msgTryAgain = "The post changed while you wrote. Try again."

// ParseBody is the one path from member text to a stored tree. A post, an edit,
// a signature and a preview all call it. It parses src and checks the tree with
// [markup.Validate], and it returns the tree, its JSON and its plain text. It
// stores nothing and writes no HTML.
//
//   - flags nil means a new post: the flags come from the rights of v in forum
//     forumID (post.markdown and post.smilies), with URL linking on.
//   - flags non-nil are the stored flags of an edit. They win over the rights of
//     v, so an imported post keeps the shape it was written in.
//   - forumID 0 means a signature. flags must be the stored signature flags of
//     the member. No forum right applies, the limit is 2,000 bytes instead of
//     60,000, an empty text is allowed, and an image is not.
//
// A text over the limit, a text that the database cannot hold, an image without
// the right to post it, or a tree that fails [markup.Validate] gives an
// [*InputError] on the field "body".
func ParseBody(ctx context.Context, q db.Querier, v Viewer, forumID int64, src string, flags *markup.Options) (Body, error) {
	return ParseBodyKeeping(ctx, q, v, forumID, src, flags, nil)
}

// ParseBodyKeeping is [ParseBody] for an edit. stored is the tree of the post
// before the edit (nil for a new post). A writer without the right to post images
// may keep the images that stored holds, by address, and may add none: the
// refusal is the same [*InputError]. A writer with the right is not limited.
func ParseBodyKeeping(ctx context.Context, q db.Querier, v Viewer, forumID int64, src string, flags *markup.Options, stored *markup.Node) (Body, error) {
	kind := textPost
	if forumID == 0 {
		kind = textSignature
	}
	return parseText(ctx, q, v, kind, forumID, src, flags, stored)
}

// textKind says what a text is for. The kind picks the limit, the flags and the
// image rule. Every kind goes through [parseText], so one function turns member
// text into a stored tree.
type textKind int

const (
	// textPost is a post: the rights of the forum decide the flags and the
	// images.
	textPost textKind = iota
	// textSignature is a signature: the stored flags of the member apply.
	textSignature
	// textMessage is a private message: pm.markdown and pm.smilies set the flags,
	// and pm.images decides on images. No forum applies.
	textMessage
)

// ParseMessage is the path from the text of a private message to a stored tree.
// It is [ParseBody] with the rights of messages: the flags are pm.markdown and
// pm.smilies with URL linking on, an image needs pm.images, and the limit is
// 60,000 bytes. A viewer without pm.send gets [ErrForbidden], so a preview needs
// no check of its own. A fault gives an [*InputError] on the field "body". It
// stores nothing.
func ParseMessage(ctx context.Context, q db.Querier, v Viewer, src string) (Body, error) {
	if v.UserID == 0 || !v.Perms.Has("pm.send", 0) {
		return Body{}, ErrForbidden
	}
	return parseText(ctx, q, v, textMessage, 0, src, nil, nil)
}

// parseText is the one function behind [ParseBody], [ParseBodyKeeping] and
// [ParseMessage]. forumID applies to textPost only.
func parseText(ctx context.Context, q db.Querier, v Viewer, kind textKind, forumID int64, src string, flags *markup.Options, stored *markup.Node) (Body, error) {
	signature := kind == textSignature
	if signature && flags == nil {
		return Body{}, errors.New("board: a signature needs its stored flags")
	}
	if err := checkSource(src, kind); err != nil {
		return Body{}, err
	}
	var opt markup.Options
	switch {
	case flags != nil:
		opt = *flags
	case kind == textMessage:
		opt = markup.Options{
			Markdown: v.Perms.Has("pm.markdown", 0),
			Smilies:  v.Perms.Has("pm.smilies", 0),
			Autolink: true,
		}
	default:
		opt = markup.Options{
			Markdown: v.Perms.Has("post.markdown", forumID),
			Smilies:  v.Perms.Has("post.smilies", forumID),
			Autolink: true,
		}
	}
	opt.SmileyList = nil
	if opt.Smilies {
		list, err := smileyList(ctx, q)
		if err != nil {
			return Body{}, err
		}
		opt.SmileyList = list
	}
	if parseHook != nil {
		parseHook()
	}
	tree := markup.Parse(src, opt)
	if err := markup.Validate(tree); err != nil {
		return Body{}, inputError("body", "The text is too complex to save. Make it simpler and try again.")
	}
	if images := imageSources(tree); len(images) > 0 {
		var may bool
		switch kind {
		case textSignature:
			// No forum applies to a signature. A member who may post images in some
			// forum may have them in the signature.
			var err error
			if may, err = mayPostImagesAnywhere(ctx, q, v); err != nil {
				return Body{}, err
			}
		case textMessage:
			may = v.Perms.Has("pm.images", 0)
		default:
			may = v.Perms.Has("post.images", forumID)
		}
		if !may {
			kept := map[string]bool{}
			for _, src := range imageSources(stored) {
				kept[src] = true
			}
			for _, src := range images {
				if !kept[src] {
					switch kind {
					case textSignature:
						return Body{}, inputError("body", msgNoSigImages)
					case textMessage:
						return Body{}, inputError("body", msgNoMsgImages)
					}
					return Body{}, inputError("body", msgNoImages)
				}
			}
		}
	}
	data, text, err := postbody.Columns(tree)
	if err != nil {
		return Body{}, err
	}
	opt.SmileyList = nil
	return Body{Tree: tree, JSON: data, Text: text, Flags: opt}, nil
}

// checkSource applies the limits to the text of a post, a signature or a
// message.
func checkSource(src string, kind textKind) error {
	signature := kind == textSignature
	limit, limitText := maxBodyBytes, "60,000"
	switch kind {
	case textSignature:
		limit, limitText = maxSignatureBytes, "2,000"
	case textMessage:
		limit, limitText = maxMessageBytes, "60,000"
	}
	switch {
	case !utf8.ValidString(src):
		return inputError("body", "The text is not valid UTF-8.")
	case strings.IndexByte(src, 0) >= 0:
		return inputError("body", "The text holds a null character.")
	case !signature && strings.TrimSpace(src) == "":
		return inputError("body", "Write some text first.")
	case len(src) > limit:
		return inputError("body", "The text is too long. The limit is "+limitText+" bytes.")
	}
	return nil
}

// imageSources returns the src of every img element of the tree, in no
// particular order. A nil tree has none.
func imageSources(n *markup.Node) []string {
	if n == nil {
		return nil
	}
	var out []string
	stack := []*markup.Node{n}
	for len(stack) > 0 {
		c := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if c.Kind == markup.Element && c.Name == "img" {
			out = append(out, c.Attrs["src"])
		}
		stack = append(stack, c.Children...)
	}
	return out
}

// mayPostImagesAnywhere reports whether v holds post.images in at least one
// forum.
func mayPostImagesAnywhere(ctx context.Context, q db.Querier, v Viewer) (bool, error) {
	rows, err := q.Query(ctx, `select id from forums`)
	if err != nil {
		return false, fmt.Errorf("board: list forum ids: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return false, fmt.Errorf("board: list forum ids: %w", err)
		}
		if v.Perms.Has("post.images", id) {
			return true, nil
		}
	}
	return false, rows.Err()
}

// hasControl reports whether s holds a control character.
func hasControl(s string) bool {
	return strings.IndexFunc(s, unicode.IsControl) >= 0
}

// cleanTitle trims a title and applies the rules: 1 to 120 characters, valid
// UTF-8, and no control character.
func cleanTitle(s string) (string, error) { return cleanLine(s, "title", maxTitleRunes) }

// cleanLine trims a one-line field and applies the rules of titles and subjects:
// 1 to limit characters, valid UTF-8, no control character and no hidden
// formatting character (the rule of [account.HiddenFormat], ruling R46). field is
// the name of the form field, and it is the word in the messages.
func cleanLine(s, field string, limit int) (string, error) {
	s = strings.TrimSpace(s)
	n := utf8.RuneCountInString(s)
	switch {
	case !utf8.ValidString(s) || hasControl(s):
		return "", inputError(field, "The "+field+" may not hold control characters or line breaks.")
	case account.HasHiddenFormat(s):
		return "", inputError(field, "Remove the hidden formatting character.")
	case n == 0:
		return "", inputError(field, "Enter a "+field+".")
	case n > limit:
		return "", inputError(field, fmt.Sprintf("The %s is longer than %d characters.", field, limit))
	}
	return s, nil
}

// cleanReason trims an edit reason and applies the rules: at most 255
// characters, valid UTF-8, and no control character.
func cleanReason(s string) (string, error) {
	s = strings.TrimSpace(s)
	switch {
	case !utf8.ValidString(s) || hasControl(s):
		return "", inputError("reason", "The reason may not hold control characters or line breaks.")
	case utf8.RuneCountInString(s) > maxReasonRunes:
		return "", inputError("reason", "The reason is too long. The limit is 255 characters.")
	}
	return s, nil
}

// smileyList reads the smilies of the board in the order of the smiley list.
func smileyList(ctx context.Context, q db.Querier) ([]markup.Smiley, error) {
	rows, err := q.Query(ctx, `select code, image_path, description from smilies order by position, id`)
	if err != nil {
		return nil, fmt.Errorf("board: read smilies: %w", err)
	}
	defer rows.Close()
	var out []markup.Smiley
	for rows.Next() {
		var code, image, desc string
		if err := rows.Scan(&code, &image, &desc); err != nil {
			return nil, fmt.Errorf("board: read smilies: %w", err)
		}
		out = append(out, markup.Smiley{Code: code, Image: "/files/" + strings.TrimLeft(image, "/"), Alt: desc})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: read smilies: %w", err)
	}
	return out, nil
}

// inTx runs fn in one transaction. It rolls back when fn fails.
func inTx(ctx context.Context, pool *pgxpool.Pool, fn func(tx pgx.Tx) error) error {
	return pgx.BeginFunc(ctx, pool, fn)
}

// inetArg returns the address as a query argument: without a zone or a 4-in-6
// form, or nil for the zero address.
func inetArg(ip netip.Addr) any {
	if !ip.IsValid() {
		return nil
	}
	return ip.Unmap().WithZone("")
}

// topicRef is the part of a topic row that the writes decide on.
type topicRef struct {
	id, forumID, authorID int64 // authorID is 0 for a guest
	visibility            string
	locked, moved         bool
	// before is the state the topic had before it was deleted: "approved",
	// "unapproved", or "" when it is not deleted or no state was kept.
	before string
}

// lockTopic reads topic id as v sees it, and locks its row until the end of the
// transaction. A topic that v may not see, or that is missing, gives
// [ErrNotFound]. The lock makes two writes to one topic run one after the other,
// so their counter updates cannot interleave. The statement that follows the
// lock reads what the earlier holder committed.
func lockTopic(ctx context.Context, tx pgx.Tx, sc scope, id int64) (topicRef, error) {
	return readTopic(ctx, tx, sc, id, true)
}

// readTopic is [lockTopic] with the lock optional. A page that only checks the
// rights of a writer (a form) reads without it.
func readTopic(ctx context.Context, q db.Querier, sc scope, id int64, lock bool) (topicRef, error) {
	t := topicRef{id: id}
	var moved *int64
	forUpdate := ""
	if lock {
		forUpdate = "\n   for no key update of t"
	}
	err := q.QueryRow(ctx, `
select t.forum_id, coalesce(t.author_id, 0), t.visibility, t.locked, t.moved_to_topic_id,
       coalesce(t.visibility_before_delete, '')
  from topics t
 where t.id = $5 and `+visible("t", 1)+forUpdate, append(sc.args(), id)...).
		Scan(&t.forumID, &t.authorID, &t.visibility, &t.locked, &moved, &t.before)
	if errors.Is(err, pgx.ErrNoRows) {
		return topicRef{}, ErrNotFound
	}
	if err != nil {
		return topicRef{}, fmt.Errorf("board: read topic %d: %w", id, err)
	}
	if moved != nil {
		// A marker whose target the viewer cannot see does not exist for the
		// viewer, as on the read side ([TopicView]): the answer is the one for a
		// missing topic. A marker that points at itself leads nowhere.
		if *moved == id {
			return topicRef{}, ErrNotFound
		}
		var seen bool
		err := q.QueryRow(ctx, `select exists (select 1 from topics m where m.id = $5 and `+visible("m", 1)+`)`,
			append(sc.args(), *moved)...).Scan(&seen)
		if err != nil {
			return topicRef{}, fmt.Errorf("board: read topic %d: %w", *moved, err)
		}
		if !seen {
			return topicRef{}, ErrNotFound
		}
	}
	t.moved = moved != nil
	return t, nil
}

// lockForum locks forum id and reports its kind and whether it is locked. A
// write takes it after the lock on a topic, and a write that holds a post lock
// takes it last. A write never waits for a topic or a post while it holds a
// forum, so two writes never wait for each other in a circle.
func lockForum(ctx context.Context, tx pgx.Tx, id int64) (kind string, locked bool, err error) {
	return forumRow(ctx, tx, id, true)
}

// forumRow reads the kind and the lock state of forum id, with the row lock
// optional. A missing forum gives [ErrNotFound].
func forumRow(ctx context.Context, q db.Querier, id int64, lock bool) (kind string, locked bool, err error) {
	sql := `select kind, locked from forums where id = $1`
	if lock {
		sql += ` for no key update`
	}
	err = q.QueryRow(ctx, sql, id).Scan(&kind, &locked)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, ErrNotFound
	}
	if err != nil {
		return "", false, fmt.Errorf("board: read forum %d: %w", id, err)
	}
	return kind, locked, nil
}

// readForum is the forum step of a new topic for viewer v: a forum that is
// missing, or that v cannot read, gives [ErrNotFound]. [NewTopic] calls it with
// the row lock, and [CanStartTopic] without.
func readForum(ctx context.Context, q db.Querier, v Viewer, id int64, lock bool) (kind string, locked bool, err error) {
	if !v.Perms.Has("forum.read", id) {
		// The same answer for a forum that is missing and for one that is hidden;
		// a forum that does not exist holds no grants either.
		return "", false, ErrNotFound
	}
	return forumRow(ctx, q, id, lock)
}

// author is who a new post belongs to.
type author struct {
	id   int64 // 0 for a guest
	name string
	ip   netip.Addr
}

// resolveAuthor names the author of a new post. A member must be an active user
// of the board. A guest needs a valid name and an address that no ban holds.
// The address check is a refusal; the name check is an input error.
func resolveAuthor(ctx context.Context, tx db.Querier, v Viewer, d Draft, now time.Time) (author, error) {
	au := author{ip: d.IP}
	if v.UserID != 0 {
		err := tx.QueryRow(ctx, `select username from users where id = $1 and state = 'active'`, v.UserID).Scan(&au.name)
		if errors.Is(err, pgx.ErrNoRows) {
			return author{}, ErrForbidden
		}
		if err != nil {
			return author{}, fmt.Errorf("board: read member %d: %w", v.UserID, err)
		}
		au.id = v.UserID
		return au, nil
	}
	var banned bool
	err := tx.QueryRow(ctx, `
select exists (select 1 from bans where kind = 'ip' and (ends_at is null or ends_at > $1) and ip >>= $2::inet)`,
		now, inetArg(d.IP)).Scan(&banned)
	if err != nil {
		return author{}, fmt.Errorf("board: read bans: %w", err)
	}
	if banned {
		return author{}, ErrForbidden
	}
	au.name = strings.TrimSpace(d.GuestName)
	if n := utf8.RuneCountInString(au.name); !utf8.ValidString(au.name) || hasControl(au.name) || n < 1 || n > maxGuestNameRunes {
		return author{}, inputError("guest_name", msgGuestName)
	}
	// The page shows the name as written, and the comparison below reads its clean
	// form. A name that the clean form changes by more than case would show as
	// another name than the one compared: "\u202eecila" shows as "alice".
	if !account.PlainName(au.name) {
		return author{}, inputError("guest_name", msgGuestNameChars)
	}
	clean := account.CleanUsername(au.name)
	if clean == "" {
		return author{}, inputError("guest_name", msgGuestName)
	}
	var taken bool
	if err := tx.QueryRow(ctx, `select exists (select 1 from users where username_clean = $1)`, clean).Scan(&taken); err != nil {
		return author{}, fmt.Errorf("board: read usernames: %w", err)
	}
	if taken {
		return author{}, inputError("guest_name", msgGuestNameTaken)
	}
	return au, nil
}

// lockAuthor takes a lock on the writer, for the length of the transaction. Two
// posts from one writer then run one after the other, so the second finds the
// first when it looks for a duplicate. The key is the member, or for a guest the
// address.
func lockAuthor(ctx context.Context, tx pgx.Tx, v Viewer, ip netip.Addr) error {
	key := "gobb:post:ip:" + ip.Unmap().WithZone("").String()
	if v.UserID != 0 {
		key = "gobb:post:user:" + strconv.FormatInt(v.UserID, 10)
	}
	if _, err := tx.Exec(ctx, `select pg_advisory_xact_lock(hashtextextended($1, 0))`, key); err != nil {
		return fmt.Errorf("board: lock writer: %w", err)
	}
	return nil
}

// newPost holds the columns of a post to insert.
type newPost struct {
	topicID, forumID int64
	au               author
	now              time.Time
	subject, source  string
	body             Body
	visibility       string
	// sig is the post's enable_sig: the post shows the author's signature.
	sig bool
}

// carriesSignature reports whether a new post of v in the forum shows the
// signature of the author: the author is a member who holds post.signatures
// there. A guest has no signature. An edit keeps the flag the post has.
func carriesSignature(v Viewer, forumID int64) bool {
	return v.UserID != 0 && v.Perms.Has("post.signatures", forumID)
}

func insertPost(ctx context.Context, tx pgx.Tx, p newPost) (int64, error) {
	var authorID any
	if p.au.id != 0 {
		authorID = p.au.id
	}
	var id int64
	err := tx.QueryRow(ctx, `
insert into posts (topic_id, forum_id, author_id, author_name, author_ip, created_at, subject,
                   body_source, body_tree, body_text, enable_markdown, enable_smilies, enable_autolink,
                   enable_sig, visibility)
values ($1, $2, $3, $4, $5, $6, $7, $8, $9::jsonb, $10, $11, $12, $13, $15, $14)
returning id`,
		p.topicID, p.forumID, authorID, p.au.name, inetArg(p.au.ip), p.now, p.subject,
		p.source, string(p.body.JSON), p.body.Text, p.body.Flags.Markdown, p.body.Flags.Smilies, p.body.Flags.Autolink,
		p.visibility, p.sig).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("board: insert post: %w", err)
	}
	return id, nil
}

// afterNewPost updates the rows that depend on a new post: the counters of its
// topic and forum, the post count of a member who counts, and the topic mark of
// a member. Both rows were locked by the caller.
func afterNewPost(ctx context.Context, tx pgx.Tx, v Viewer, p newPost) error {
	if err := counters.Topic(ctx, tx, p.topicID); err != nil {
		return err
	}
	if err := counters.Forum(ctx, tx, p.forumID); err != nil {
		return err
	}
	if p.au.id == 0 {
		return nil
	}
	if p.visibility == "approved" && v.Perms.Has("post.counts", p.forumID) {
		if err := addPostCount(ctx, tx, p.au.id, 1); err != nil {
			return err
		}
	}
	// The writer's own post is not new to the writer.
	if _, err := tx.Exec(ctx, `
insert into topic_marks (user_id, topic_id, marked_at) values ($1, $2, $3)
on conflict (user_id, topic_id) do update set marked_at = greatest(topic_marks.marked_at, excluded.marked_at)`,
		p.au.id, p.topicID, p.now); err != nil {
		return fmt.Errorf("board: move topic mark: %w", err)
	}
	return nil
}

// addPostCount moves the post count of a member by delta, and never below 0.
func addPostCount(ctx context.Context, tx pgx.Tx, userID int64, delta int) error {
	if _, err := tx.Exec(ctx, `update users set post_count = greatest(post_count + $2, 0) where id = $1`, userID, delta); err != nil {
		return fmt.Errorf("board: update post count of %d: %w", userID, err)
	}
	return nil
}

// authorCounts reports whether the author of a post holds post.counts in the
// forum. It is the right of the author, not of the viewer who acts: the count
// moved when the author posted, and it moves back by the same rule.
func authorCounts(ctx context.Context, tx pgx.Tx, v Viewer, authorID, forumID int64) (bool, error) {
	if authorID == 0 {
		return false, nil
	}
	if authorID == v.UserID {
		return v.Perms.Has("post.counts", forumID), nil
	}
	subject, err := acldb.Load(ctx, tx, authorID)
	if errors.Is(err, acldb.ErrUnknownUser) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("board: load rights of %d: %w", authorID, err)
	}
	return acl.Resolve(subject).Has("post.counts", forumID), nil
}

// visibilityFor is the visibility of a new post: approved when the writer holds
// post.no_approval in the forum, held otherwise.
func visibilityFor(v Viewer, forumID int64) string {
	if v.Perms.Has("post.no_approval", forumID) {
		return "approved"
	}
	return "unapproved"
}

// NewTopic starts a topic in forum forumID with the first post d, and returns
// the ID of that post.
//
//   - The forum must exist and v must read it, or the result is [ErrNotFound].
//   - It must be a forum, not a category or a link; v needs topic.start in it;
//     and the forum must be unlocked, or v needs mod.lock. Else [ErrForbidden].
//   - A held topic (v lacks post.no_approval) is held as a whole.
//   - A topic with the same author, forum, title and text from the last 60
//     seconds is the same topic sent twice: the write returns its first post.
//
// now is the time of every row that the write makes.
func NewTopic(ctx context.Context, pool *pgxpool.Pool, v Viewer, forumID int64, d Draft, now time.Time) (int64, error) {
	if !d.IP.IsValid() {
		return 0, errors.New("board: a write needs the address of the writer")
	}
	// The parse costs the most of the write, so it runs before any row lock: a
	// slow parse must not hold up the writers of the forum. The rights are those
	// of v, read without a lock. The checks that decide whether the write may go
	// on run here as well as in the transaction, so a writer who is refused does
	// not pay for a parse. A parse error waits for its place in the transaction,
	// so the order of the answers stays as it was.
	kind, locked, err := readForum(ctx, pool, v, forumID, false)
	if err != nil {
		return 0, err
	}
	if !mayStart(v, forumID, kind, locked) {
		return 0, ErrForbidden
	}
	body, parseErr := ParseBody(ctx, pool, v, forumID, d.Source, nil)
	var postID int64
	err = inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := lockAuthor(ctx, tx, v, d.IP); err != nil {
			return err
		}
		kind, locked, err := readForum(ctx, tx, v, forumID, true)
		if err != nil {
			return err
		}
		if !mayStart(v, forumID, kind, locked) {
			return ErrForbidden
		}
		au, err := resolveAuthor(ctx, tx, v, d, now)
		if err != nil {
			return err
		}
		title, err := cleanTitle(d.Title)
		if err != nil {
			return err
		}
		if parseErr != nil {
			return parseErr
		}
		if postID, err = findDuplicateTopic(ctx, tx, au, forumID, title, d.Source, now); err != nil || postID != 0 {
			return err
		}
		vis := visibilityFor(v, forumID)
		var topicID int64
		err = tx.QueryRow(ctx, `
insert into topics (forum_id, title, author_id, author_name, created_at, last_post_at, kind, locked, visibility)
values ($1, $2, $3, $4, $5, $5, 'normal', false, $6) returning id`,
			forumID, title, nullID(au.id), au.name, now, vis).Scan(&topicID)
		if err != nil {
			return fmt.Errorf("board: insert topic: %w", err)
		}
		p := newPost{topicID: topicID, forumID: forumID, au: au, now: now, subject: title, source: d.Source, body: body, visibility: vis,
			sig: carriesSignature(v, forumID)}
		if postID, err = insertPost(ctx, tx, p); err != nil {
			return err
		}
		return afterNewPost(ctx, tx, v, p)
	})
	if err != nil {
		return 0, err
	}
	return postID, nil
}

// nullID turns the ID 0 into a null argument.
func nullID(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// findDuplicateTopic returns the first post of a topic that the same author
// started in the last 60 seconds with the same title and text, or 0.
func findDuplicateTopic(ctx context.Context, tx pgx.Tx, au author, forumID int64, title, source string, now time.Time) (int64, error) {
	who := `t.author_id = $5`
	arg := any(au.id)
	if au.id == 0 {
		who, arg = `t.author_id is null and p.author_ip = $5::inet`, inetArg(au.ip)
	}
	var id int64
	err := tx.QueryRow(ctx, `
select p.id from topics t join posts p on p.topic_id = t.id
 where t.forum_id = $1 and t.title = $2 and p.body_source = $3
   and t.created_at > $4 and t.visibility <> 'deleted' and p.visibility <> 'deleted'
   and `+who+`
   and p.id = (select f.id from posts f where f.topic_id = t.id order by f.created_at, f.id limit 1)
 order by t.id limit 1`, forumID, title, source, now.Add(-duplicateWindow), arg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("board: look for a duplicate topic: %w", err)
	}
	return id, nil
}

// findDuplicateReply returns a post that the same author made in topic topicID
// in the last 60 seconds with the same text, or 0.
func findDuplicateReply(ctx context.Context, tx pgx.Tx, au author, topicID int64, source string, now time.Time) (int64, error) {
	who := `author_id = $4`
	arg := any(au.id)
	if au.id == 0 {
		who, arg = `author_id is null and author_ip = $4::inet`, inetArg(au.ip)
	}
	var id int64
	err := tx.QueryRow(ctx, `
select id from posts
 where topic_id = $1 and body_source = $2 and created_at > $3 and visibility <> 'deleted' and `+who+`
 order by id limit 1`, topicID, source, now.Add(-duplicateWindow), arg).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("board: look for a duplicate post: %w", err)
	}
	return id, nil
}

// Reply adds the post d to topic topicID and returns its ID.
//
//   - A topic that v cannot see is [ErrNotFound], as a missing one is. So is a
//     moved-topic marker, whatever its target: a marker is no reply target.
//   - v needs topic.reply in the forum. The topic must be approved. The topic and
//     its forum must be unlocked, or v needs mod.lock. Else [ErrForbidden].
//   - A post with the same author, topic and text from the last 60 seconds is
//     the same post sent twice: the write returns the earlier post. For a
//     guest, the same author is the same address.
//
// now is the time of every row that the write makes.
func Reply(ctx context.Context, pool *pgxpool.Pool, v Viewer, topicID int64, d Draft, now time.Time) (int64, error) {
	if !d.IP.IsValid() {
		return 0, errors.New("board: a write needs the address of the writer")
	}
	// As in [NewTopic], the parse runs before any row lock. The forum of the
	// topic decides the flags, so the topic is read first, without a lock, and the
	// transaction checks that the forum is the one that was parsed for. A topic
	// does not change its forum, so the second try is for a race that cannot
	// happen today.
	for attempt := 0; ; attempt++ {
		sc, err := newScope(ctx, pool, v)
		if err != nil {
			return 0, err
		}
		t, err := readTopic(ctx, pool, sc, topicID, false)
		if err != nil {
			return 0, err
		}
		if t.moved {
			return 0, ErrNotFound
		}
		if !mayReplyTo(v, t.forumID, t.visibility) {
			return 0, ErrForbidden
		}
		body, parseErr := ParseBody(ctx, pool, v, t.forumID, d.Source, nil)
		postID, err := replyTx(ctx, pool, v, topicID, t.forumID, d, body, parseErr, now)
		if errors.Is(err, errStaleParse) {
			if attempt == 1 {
				return 0, inputError("body", msgTryAgain)
			}
			continue
		}
		return postID, err
	}
}

// replyTx is the transaction of [Reply]. body was parsed for forum parsedForum.
func replyTx(ctx context.Context, pool *pgxpool.Pool, v Viewer, topicID, parsedForum int64, d Draft, body Body, parseErr error, now time.Time) (int64, error) {
	var postID int64
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		if err := lockAuthor(ctx, tx, v, d.IP); err != nil {
			return err
		}
		sc, err := newScope(ctx, tx, v)
		if err != nil {
			return err
		}
		t, err := lockTopic(ctx, tx, sc, topicID)
		if err != nil {
			return err
		}
		if t.moved {
			// A marker is never a reply target, whatever its target: the answer is
			// the one for a missing topic.
			return ErrNotFound
		}
		if !mayReplyTo(v, t.forumID, t.visibility) {
			return ErrForbidden
		}
		// The forum lock comes after the checks that need no forum row, so a writer
		// who is refused does not hold up the writers of the forum.
		_, forumLocked, err := lockForum(ctx, tx, t.forumID)
		if err != nil {
			return err
		}
		if err := lockStopError(v, t.forumID, t.locked, forumLocked); err != nil {
			return err
		}
		au, err := resolveAuthor(ctx, tx, v, d, now)
		if err != nil {
			return err
		}
		if t.forumID != parsedForum {
			return errStaleParse
		}
		if parseErr != nil {
			return parseErr
		}
		if postID, err = findDuplicateReply(ctx, tx, au, topicID, d.Source, now); err != nil || postID != 0 {
			return err
		}
		var title string
		if err := tx.QueryRow(ctx, `select title from topics where id = $1`, topicID).Scan(&title); err != nil {
			return fmt.Errorf("board: read topic %d: %w", topicID, err)
		}
		p := newPost{
			topicID: topicID, forumID: t.forumID, au: au, now: now, subject: "Re: " + title,
			source: d.Source, body: body, visibility: visibilityFor(v, t.forumID),
			sig: carriesSignature(v, t.forumID),
		}
		if postID, err = insertPost(ctx, tx, p); err != nil {
			return err
		}
		return afterNewPost(ctx, tx, v, p)
	})
	if err != nil {
		return 0, err
	}
	return postID, nil
}

// postRef is the part of a post row that the writes decide on.
type postRef struct {
	id, topicID, forumID, authorID int64 // authorID is 0 for a guest
	visibility                     string
	before                         string // as in topicRef
	flags                          markup.Options
	first                          bool // the first post of its topic
}

// lockPost reads post id as v sees it, with its topic, and locks the topic and
// then the post. A post or a topic that v may not see, or that is missing, gives
// [ErrNotFound]. A write that changes a counter of the forum then takes
// [lockForum], after it has checked the rights of v.
func lockPost(ctx context.Context, tx pgx.Tx, v Viewer, id int64) (postRef, topicRef, error) {
	return readPost(ctx, tx, v, id, true)
}

// readPost is [lockPost] with the locks optional. A page that only checks the
// rights of a writer (a form) reads without them.
func readPost(ctx context.Context, q db.Querier, v Viewer, id int64, lock bool) (postRef, topicRef, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return postRef{}, topicRef{}, err
	}
	var topicID int64
	err = q.QueryRow(ctx, `select p.topic_id from posts p where p.id = $5 and `+visible("p", 1), append(sc.args(), id)...).Scan(&topicID)
	if errors.Is(err, pgx.ErrNoRows) {
		return postRef{}, topicRef{}, ErrNotFound
	}
	if err != nil {
		return postRef{}, topicRef{}, fmt.Errorf("board: read post %d: %w", id, err)
	}
	t, err := readTopic(ctx, q, sc, topicID, lock)
	if err != nil {
		return postRef{}, topicRef{}, err
	}
	p := postRef{id: id, topicID: topicID}
	forUpdate := ""
	if lock {
		forUpdate = "\n   for no key update of p"
	}
	err = q.QueryRow(ctx, `
select p.forum_id, coalesce(p.author_id, 0), p.visibility, coalesce(p.visibility_before_delete, ''),
       p.enable_markdown, p.enable_smilies, p.enable_autolink,
       p.id = (select f.id from posts f where f.topic_id = p.topic_id order by f.created_at, f.id limit 1)
  from posts p
 where p.id = $5 and p.topic_id = $6 and `+visible("p", 1)+forUpdate, append(sc.args(), id, topicID)...).
		Scan(&p.forumID, &p.authorID, &p.visibility, &p.before, &p.flags.Markdown, &p.flags.Smilies, &p.flags.Autolink, &p.first)
	if errors.Is(err, pgx.ErrNoRows) {
		return postRef{}, topicRef{}, ErrNotFound
	}
	if err != nil {
		return postRef{}, topicRef{}, fmt.Errorf("board: lock post %d: %w", id, err)
	}
	return p, t, nil
}

// logMod writes one row of the moderation log for the member v.
func logMod(ctx context.Context, tx pgx.Tx, v Viewer, action, targetType string, target int64, details string, now time.Time) error {
	if _, err := tx.Exec(ctx, `
insert into mod_log (actor_id, action, target_type, target_id, details, created_at)
values ($1, $2, $3, $4, $5::jsonb, $6)`, nullID(v.UserID), action, targetType, target, details, now); err != nil {
		return fmt.Errorf("board: write the moderation log: %w", err)
	}
	return nil
}

// EditPost replaces the text of post postID with d.Source, and for the first
// post of a topic also the title of the topic with d.Title.
//
//   - A post that v cannot see is [ErrNotFound]. A guest, or a member who is
//     neither the author with post.edit_own in an unlocked topic nor a holder of
//     mod.edit in the forum, gets [ErrForbidden].
//   - The stored flags of the post stay. The text is parsed with them.
//   - edit_count grows by one; edited_at and edited_by are set.
//   - A moderator's edit writes a mod_log row. When the post belongs to another
//     member, the row holds the reason and the post stores it. A moderator who
//     edits the own post by mod.edit alone gives no reason: it is not checked,
//     stored or logged. An author's own edit writes no row.
//   - edit_reason always belongs to the last editor. An edit that gives no
//     reason clears the old one, so a moderator's reason, or one that the import
//     brought, never shows under the name of a later editor.
//
// now is the time of the edit.
func EditPost(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, d Draft, reason string, now time.Time) error {
	// As in [NewTopic], the parse runs before any row lock. It needs the stored
	// flags and the stored tree of the post, so they are read first, without a
	// lock. The transaction reads edit_count under the lock on the post and
	// compares it: an edit that committed in between changes it. The write then
	// parses again, once, with the fresh values. A second change gives up.
	for attempt := 0; ; attempt++ {
		p, t, err := readPost(ctx, pool, v, postID, false)
		if err != nil {
			return err
		}
		if _, err := editRights(v, p, t); err != nil {
			return err
		}
		stored, editCount, err := readStored(ctx, pool, postID)
		if err != nil {
			return err
		}
		body, parseErr := ParseBodyKeeping(ctx, pool, v, p.forumID, d.Source, &p.flags, stored)
		err = editTx(ctx, pool, v, postID, d, reason, now, parsed{p: p, editCount: editCount, body: body, err: parseErr})
		if errors.Is(err, errStaleParse) {
			if attempt == 1 {
				return inputError("body", msgTryAgain)
			}
			continue
		}
		return err
	}
}

// parsed is a body that [EditPost] parsed before the transaction, with what it
// was parsed from.
type parsed struct {
	p         postRef
	editCount int64
	body      Body
	err       error // the parse error, which the transaction returns in its turn
}

// sameFlags reports whether two sets of post flags are equal. The smiley list
// plays no part: a post row does not hold it.
func sameFlags(a, b markup.Options) bool {
	return a.Markdown == b.Markdown && a.Smilies == b.Smilies && a.Autolink == b.Autolink
}

// readStored reads the tree and the edit count of a post.
func readStored(ctx context.Context, q db.Querier, postID int64) (*markup.Node, int64, error) {
	var storedJSON []byte
	var editCount int64
	if err := q.QueryRow(ctx, `select body_tree, edit_count from posts where id = $1`, postID).Scan(&storedJSON, &editCount); err != nil {
		return nil, 0, fmt.Errorf("board: read post %d: %w", postID, err)
	}
	stored, err := markup.Decode(storedJSON)
	if err != nil {
		return nil, 0, fmt.Errorf("board: read post %d: %w", postID, err)
	}
	return stored, editCount, nil
}

// editTx is the transaction of [EditPost]. It gives [errStaleParse] when the
// post is not the one that pre was parsed from.
func editTx(ctx context.Context, pool *pgxpool.Pool, v Viewer, postID int64, d Draft, reason string, now time.Time, pre parsed) error {
	return inTx(ctx, pool, func(tx pgx.Tx) error {
		p, t, err := lockPost(ctx, tx, v, postID)
		if err != nil {
			return err
		}
		own, err := editRights(v, p, t)
		if err != nil {
			return err
		}
		// The reason belongs to an edit of another member's post. A moderator who
		// edits the own post by mod.edit alone gives none: it is not asked for, so
		// it is not checked, stored or logged, and the log row has an empty one.
		moderated := !own
		askReason := moderated && p.authorID != v.UserID
		if askReason {
			if reason, err = cleanReason(reason); err != nil {
				return err
			}
		} else {
			reason = ""
		}
		var title string
		if p.first {
			if title, err = cleanTitle(d.Title); err != nil {
				return err
			}
		}
		// The row lock on the post is held: nothing changes edit_count until the
		// end of this transaction.
		var editCount int64
		if err := tx.QueryRow(ctx, `select edit_count from posts where id = $1`, postID).Scan(&editCount); err != nil {
			return fmt.Errorf("board: read post %d: %w", postID, err)
		}
		if editCount != pre.editCount || p.forumID != pre.p.forumID || !sameFlags(p.flags, pre.p.flags) {
			return errStaleParse
		}
		if pre.err != nil {
			return pre.err
		}
		body := pre.body
		_, err = tx.Exec(ctx, `
update posts set body_source = $2, body_tree = $3::jsonb, body_text = $4,
       edit_count = edit_count + 1, edited_at = $5, edited_by = $6,
       edit_reason = $7
 where id = $1`, postID, d.Source, string(body.JSON), body.Text, now, v.UserID, reason)
		if err != nil {
			return fmt.Errorf("board: edit post %d: %w", postID, err)
		}
		if p.first {
			if err := retitle(ctx, tx, t.id, postID, title); err != nil {
				return err
			}
		}
		if moderated {
			return logMod(ctx, tx, v, "post.edit", "post", postID, jsonObject("reason", reason), now)
		}
		return nil
	})
}

// retitle gives topic topicID the title. Its first post, firstPostID, takes the
// title as its subject. The old title must leave every place that holds a copy
// of it, because a moderator may rename a topic to remove text from it:
//   - a reply whose subject is "Re: " and the old title takes "Re: " and the
//     new one. A reply with another subject is the writer's own and stays;
//   - a moved-topic marker that points at the topic takes the new title.
//
// The search column of a post follows its subject, so search no longer finds the
// old words. The caller holds the lock on the topic and on the first post.
func retitle(ctx context.Context, tx pgx.Tx, topicID, firstPostID int64, title string) error {
	var old string
	if err := tx.QueryRow(ctx, `select title from topics where id = $1`, topicID).Scan(&old); err != nil {
		return fmt.Errorf("board: read topic %d: %w", topicID, err)
	}
	if _, err := tx.Exec(ctx, `update posts set subject = $2 where id = $1`, firstPostID, title); err != nil {
		return fmt.Errorf("board: edit post %d: %w", firstPostID, err)
	}
	if _, err := tx.Exec(ctx, `update topics set title = $2 where id = $1`, topicID, title); err != nil {
		return fmt.Errorf("board: edit topic %d: %w", topicID, err)
	}
	if old == title {
		return nil
	}
	if _, err := tx.Exec(ctx, `
update posts set subject = 'Re: ' || $3
 where topic_id = $1 and id <> $2 and subject = 'Re: ' || $4`, topicID, firstPostID, title, old); err != nil {
		return fmt.Errorf("board: rename the replies of topic %d: %w", topicID, err)
	}
	if _, err := tx.Exec(ctx, `update topics set title = $2 where moved_to_topic_id = $1`, topicID, title); err != nil {
		return fmt.Errorf("board: rename the markers of topic %d: %w", topicID, err)
	}
	return nil
}

// QuoteSource returns the Markdown of a quote of post postID, in the form
//
//	> **Name** [wrote](/p/ID):
//	>
//	> the lines of the post
//
// followed by a blank line, so that the writer types below it.
//
// Only an approved post in an approved topic that is no moved-topic marker can
// be quoted, and v must be able to read it. The rights of v to reply belong to
// the topic that the reply goes into, which the caller checks with [CanReply]: a
// quote can come from another forum and from a locked topic. A post that v cannot
// see, an unapproved post, a deleted post, and a post in a deleted or held topic
// give [ErrNotFound], even to a moderator who sees them on the page: the text of
// a hidden post never reaches a form.
func QuoteSource(ctx context.Context, q db.Querier, v Viewer, postID int64) (string, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return "", err
	}
	var name, text string
	err = q.QueryRow(ctx, `
select coalesce(u.username, p.author_name), p.body_source
  from posts p
  join topics t on t.id = p.topic_id
  left join users u on u.id = p.author_id
 where p.id = $5 and p.visibility = 'approved' and t.visibility = 'approved' and t.moved_to_topic_id is null
   and `+visible("p", 1)+` and `+visible("t", 1), append(sc.args(), postID)...).Scan(&name, &text)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("board: read post %d: %w", postID, err)
	}
	return quoteBlock("**"+escapeMarkdown(name)+"** [wrote](/p/"+strconv.FormatInt(postID, 10)+"):", text), nil
}

// quoteBlock writes a quotation: the line head, an empty quote line, and the
// lines of text, each behind "> ", then a blank line for the writer to start on.
// A post quote and a message quote both use it. The caller escapes what it puts
// in head.
func quoteBlock(head, text string) string {
	var sb strings.Builder
	sb.WriteString("> " + head + "\n>\n")
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		if line == "" {
			sb.WriteString(">\n")
			continue
		}
		sb.WriteString("> " + line + "\n")
	}
	sb.WriteString("\n")
	return sb.String()
}

// escapeMarkdown writes s so that Markdown reads it as plain text: a backslash
// before each ASCII punctuation character, and a space for a control character.
func escapeMarkdown(s string) string {
	var sb strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsControl(r):
			sb.WriteByte(' ')
		case r < utf8.RuneSelf && unicode.IsPunct(r) || r < utf8.RuneSelf && unicode.IsSymbol(r):
			sb.WriteByte('\\')
			sb.WriteRune(r)
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}

// jsonObject returns a JSON object with one string value.
func jsonObject(key, value string) string {
	data, err := json.Marshal(map[string]string{key: value})
	if err != nil {
		// A map of two strings always encodes.
		panic("board: encode log details: " + err.Error())
	}
	return string(data)
}
