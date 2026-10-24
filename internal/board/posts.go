package board

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/db"
	"github.com/evanwtf/gobb/markup"
)

// UserCard is what an author block shows about a member.
type UserCard struct {
	ID   int64
	Name string
	// AvatarPath is a path under the data directory, or empty.
	AvatarPath string
	// RankTitle and RankImage describe the rank. RankImage is a path under the
	// data directory, or empty.
	RankTitle, RankImage string
	PostCount            int
	Joined               time.Time
}

// Attachment is a file that belongs to a post.
type Attachment struct {
	ID         int64
	Name, Mime string
	Comment    string
	Size       int64
	Downloads  int
	// Inline is true for an attachment that the tree of the post shows in an
	// img element. [Post].Attachments never lists one, so it is false there.
	Inline bool
}

// Post is one post of a topic page.
type Post struct {
	ID int64
	// Author is the name to show. ID is 0 for a guest and for a member who is
	// gone. Card is nil in both cases.
	Author  Author
	Card    *UserCard
	At      time.Time
	Subject string
	// Visibility is "approved", "unapproved" or "deleted".
	Visibility string
	Tree       *markup.Node
	// Signature is the signature tree of the author. It is nil unless the post
	// asks for the signature and the author has one.
	Signature  *markup.Node
	EditCount  int
	EditedAt   *time.Time
	EditedBy   *Author
	EditReason string
	// IP is the address of the author. It is empty unless the viewer has
	// mod.view_ip in the forum.
	IP string
	// Attachments lists the files that the tree does not show inline.
	Attachments []Attachment
}

// PollOption is one choice of a poll.
type PollOption struct {
	ID    int
	Text  string
	Votes int
}

// Poll is the poll of a topic, with its results.
type Poll struct {
	Question   string
	MaxOptions int
	EndsAt     *time.Time
	Options    []PollOption
	// Total is the sum of the votes of the options.
	Total int
}

// TopicPage is one page of a topic.
type TopicPage struct {
	Topic  TopicRow
	Forum  *Forum
	Crumbs []*Forum
	Posts  []Post
	// Poll is set on page 1 of a topic that has a poll.
	Poll        *Poll
	Page, Pages int
	// CanDownload is true when the viewer has account.download and the
	// attach.download right of the forum, so that /a/{id} serves them a file.
	CanDownload bool
}

// TopicView returns page number page (from 1) of topic topicID as v sees it.
//
// The viewer needs forum.read on the forum of the topic, and the right to see
// the topic itself (see [visible]). Anything else gives [ErrNotFound], so a
// hidden topic looks like a missing one. A moved-topic marker gives a
// [MovedError] when the viewer may see its target, and [ErrNotFound] when not.
// A page after the last also gives [ErrNotFound].
//
// The number of queries does not depend on the number of posts: the topic, the
// post count, the posts, the author cards, the attachments and the poll take one
// query each.
func TopicView(ctx context.Context, q db.Querier, v Viewer, topicID int64, page int) (TopicPage, error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return TopicPage{}, err
	}
	var (
		out     TopicPage
		forumID int64
	)
	t := &out.Topic
	err = q.QueryRow(ctx, `
select t.id, t.forum_id, t.title, t.kind, t.visibility, t.locked,
       coalesce(t.author_id, 0), coalesce(au.username, t.author_name),
       t.reply_count, t.view_count, coalesce(t.moved_to_topic_id, 0)
  from topics t
  left join users au on au.id = t.author_id
 where t.id = $5 and `+visible("t", 1), append(sc.args(), topicID)...).
		Scan(&t.ID, &forumID, &t.Title, &t.Kind, &t.Visibility, &t.Locked,
			&t.Author.ID, &t.Author.Name, &t.Replies, &t.Views, &t.MovedTo)
	if errors.Is(err, pgx.ErrNoRows) {
		return TopicPage{}, ErrNotFound
	}
	if err != nil {
		return TopicPage{}, fmt.Errorf("board: read topic %d: %w", topicID, err)
	}
	if t.MovedTo == t.ID {
		// A marker that points at itself would send the visitor round and round.
		return TopicPage{}, ErrNotFound
	}
	if t.MovedTo != 0 {
		var ok bool
		err := q.QueryRow(ctx, `select exists (select 1 from topics m where m.id = $5 and `+visible("m", 1)+`)`,
			append(sc.args(), t.MovedTo)...).Scan(&ok)
		if err != nil {
			return TopicPage{}, fmt.Errorf("board: read topic %d: %w", t.MovedTo, err)
		}
		if !ok {
			return TopicPage{}, ErrNotFound
		}
		return TopicPage{}, MovedError{To: t.MovedTo}
	}

	f, _, crumbs, err := locateForum(ctx, q, v, forumID, false)
	if err != nil {
		return TopicPage{}, err
	}
	out.Forum, out.Crumbs, out.Page = f, crumbs, page
	out.CanDownload = v.Perms.Has("account.download", 0) && v.Perms.Has("attach.download", forumID)

	var n int
	err = q.QueryRow(ctx, `select count(*) from posts p where p.topic_id = $5 and `+visible("p", 1),
		append(sc.args(), topicID)...).Scan(&n)
	if err != nil {
		return TopicPage{}, fmt.Errorf("board: count posts: %w", err)
	}
	out.Pages = pageCount(n, postsPerPage)
	if page < 1 || page > out.Pages {
		return TopicPage{}, ErrNotFound
	}

	showIP := v.Perms.Has("mod.view_ip", forumID)
	out.Posts, err = readPosts(ctx, q, sc, topicID, page, showIP)
	if err != nil {
		return TopicPage{}, err
	}
	if err := fillCards(ctx, q, out.Posts); err != nil {
		return TopicPage{}, err
	}
	if err := fillAttachments(ctx, q, out.Posts); err != nil {
		return TopicPage{}, err
	}
	if page == 1 {
		out.Poll, err = readPoll(ctx, q, topicID)
		if err != nil {
			return TopicPage{}, err
		}
	}
	return out, nil
}

// readPosts reads one page of the posts that sc lets the viewer see, in order.
func readPosts(ctx context.Context, q db.Querier, sc scope, topicID int64, page int, showIP bool) ([]Post, error) {
	rows, err := q.Query(ctx, `
select p.id, coalesce(p.author_id, 0), coalesce(u.username, p.author_name), p.created_at,
       p.subject, p.visibility, p.body_tree, p.enable_sig,
       p.edit_count, p.edited_at, coalesce(p.edited_by, 0), coalesce(eu.username, ''), p.edit_reason,
       case when $8::bool then coalesce(host(p.author_ip), '') else '' end
  from posts p
  left join users u on u.id = p.author_id
  left join users eu on eu.id = p.edited_by
 where p.topic_id = $5 and `+visible("p", 1)+`
 order by p.created_at, p.id
 limit $6 offset $7`, append(sc.args(), topicID, postsPerPage, (page-1)*postsPerPage, showIP)...)
	if err != nil {
		return nil, fmt.Errorf("board: read posts: %w", err)
	}
	defer rows.Close()
	var out []Post
	for rows.Next() {
		var (
			p        Post
			raw      []byte
			wantSig  bool
			editorID int64
			editor   string
		)
		if err := rows.Scan(&p.ID, &p.Author.ID, &p.Author.Name, &p.At, &p.Subject, &p.Visibility,
			&raw, &wantSig, &p.EditCount, &p.EditedAt, &editorID, &editor, &p.EditReason, &p.IP); err != nil {
			return nil, fmt.Errorf("board: read posts: %w", err)
		}
		p.Tree = decodeTree(raw, "post", p.ID)
		if editorID != 0 {
			p.EditedBy = &Author{ID: editorID, Name: editor}
		}
		if wantSig {
			// A marker: fillCards keeps the signature only for a post that asked.
			p.Signature = &markup.Node{}
		}
		out = append(out, p)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: read posts: %w", err)
	}
	return out, nil
}

// decodeTree reads a stored tree. A tree that does not decode shows as an empty
// post, and the log says which row it was, so one bad row does not take the page
// down.
func decodeTree(raw []byte, what string, id int64) *markup.Node {
	n, err := markup.Decode(raw)
	if err != nil {
		slog.Error("stored tree does not decode", "row", what, "id", id, "err", err)
		return &markup.Node{Kind: markup.Root}
	}
	return n
}

// fillCards sets Card on the posts of members, and Signature on the posts that
// ask for one. It reads the cards with one query.
func fillCards(ctx context.Context, q db.Querier, posts []Post) error {
	ids := []int64{}
	seen := map[int64]bool{}
	for _, p := range posts {
		if p.Author.ID != 0 && !seen[p.Author.ID] {
			seen[p.Author.ID] = true
			ids = append(ids, p.Author.ID)
		}
	}
	cards := map[int64]*UserCard{}
	sigs := map[int64]*markup.Node{}
	if len(ids) > 0 {
		rows, err := q.Query(ctx, `
select `+cardColumns+`,
       u.signature_tree
  from users u`+cardJoins+`
 where u.id = any($1)`, ids)
		if err != nil {
			return fmt.Errorf("board: read author cards: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			c := &UserCard{}
			var sig []byte
			if err := scanCard(rows, c, &sig); err != nil {
				return fmt.Errorf("board: read author cards: %w", err)
			}
			cards[c.ID] = c
			if len(sig) > 0 {
				if n := decodeTree(sig, "signature of user", c.ID); len(n.Children) > 0 {
					sigs[c.ID] = n
				}
			}
		}
		if err := rows.Err(); err != nil {
			return fmt.Errorf("board: read author cards: %w", err)
		}
	}
	for i := range posts {
		p := &posts[i]
		p.Card = cards[p.Author.ID]
		if p.Signature != nil {
			p.Signature = sigs[p.Author.ID]
		}
	}
	return nil
}

// attachmentsSQL reads the attachments of the posts $1.
const attachmentsSQL = `
select post_id, id, real_name, mime, size, download_count, comment
  from attachments where post_id = any($1) order by post_id, position, id`

// fillAttachments reads the attachments of the posts with one query and sets
// Attachments to those that the tree of the post does not show inline.
func fillAttachments(ctx context.Context, q db.Querier, posts []Post) error {
	if len(posts) == 0 {
		return nil
	}
	ids := make([]int64, len(posts))
	index := map[int64]int{}
	for i, p := range posts {
		ids[i] = p.ID
		index[p.ID] = i
	}
	rows, err := q.Query(ctx, attachmentsSQL, ids)
	if err != nil {
		return fmt.Errorf("board: read attachments: %w", err)
	}
	defer rows.Close()
	inline := map[int64]map[int64]bool{}
	for rows.Next() {
		var (
			postID int64
			a      Attachment
		)
		if err := rows.Scan(&postID, &a.ID, &a.Name, &a.Mime, &a.Size, &a.Downloads, &a.Comment); err != nil {
			return fmt.Errorf("board: read attachments: %w", err)
		}
		i := index[postID]
		if inline[postID] == nil {
			inline[postID] = inlineAttachments(posts[i].Tree)
		}
		if inline[postID][a.ID] {
			continue
		}
		posts[i].Attachments = append(posts[i].Attachments, a)
	}
	return rows.Err()
}

// inlineAttachments returns the IDs of the attachments that tree shows in an
// img element whose src is exactly /a/{id}.
func inlineAttachments(tree *markup.Node) map[int64]bool {
	out := map[int64]bool{}
	seen := map[*markup.Node]bool{}
	stack := []*markup.Node{tree}
	for len(stack) > 0 {
		n := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if n == nil || seen[n] {
			continue
		}
		seen[n] = true
		if n.Kind == markup.Element && n.Name == "img" {
			if rest, ok := strings.CutPrefix(n.Attrs["src"], "/a/"); ok {
				if id, err := strconv.ParseInt(rest, 10, 64); err == nil && strconv.FormatInt(id, 10) == rest {
					out[id] = true
				}
			}
		}
		stack = append(stack, n.Children...)
	}
	return out
}

// readPoll reads the poll of a topic and its options with one query. It
// returns nil when the topic has no poll.
func readPoll(ctx context.Context, q db.Querier, topicID int64) (*Poll, error) {
	rows, err := q.Query(ctx, `
select p.question, p.max_options, p.ends_at, o.option_id, o.text, o.vote_count
  from polls p
  left join poll_options o on o.topic_id = p.topic_id
 where p.topic_id = $1
 order by o.option_id`, topicID)
	if err != nil {
		return nil, fmt.Errorf("board: read poll: %w", err)
	}
	defer rows.Close()
	var poll *Poll
	for rows.Next() {
		var (
			question string
			maxOpts  int
			ends     *time.Time
			optID    *int
			text     *string
			votes    *int
		)
		if err := rows.Scan(&question, &maxOpts, &ends, &optID, &text, &votes); err != nil {
			return nil, fmt.Errorf("board: read poll: %w", err)
		}
		if poll == nil {
			poll = &Poll{Question: question, MaxOptions: maxOpts, EndsAt: ends}
		}
		if optID != nil && text != nil && votes != nil {
			poll.Options = append(poll.Options, PollOption{ID: *optID, Text: *text, Votes: *votes})
			poll.Total += *votes
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: read poll: %w", err)
	}
	return poll, nil
}

// PostLocation returns the topic that holds post postID and the page of that
// topic on which v finds the post. The page counts only the posts that v sees.
// A post that v may not see, or whose topic v may not see, gives [ErrNotFound],
// before the caller can redirect.
func PostLocation(ctx context.Context, q db.Querier, v Viewer, postID int64) (topicID int64, page int, err error) {
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return 0, 0, err
	}
	var before int
	err = q.QueryRow(ctx, `
select p.topic_id,
       (select count(*) from posts o
         where o.topic_id = p.topic_id and `+visible("o", 1)+`
           and (o.created_at, o.id) <= (p.created_at, p.id))
  from posts p
  join topics t on t.id = p.topic_id
 where p.id = $5 and `+visible("p", 1)+` and `+visible("t", 1), append(sc.args(), postID)...).
		Scan(&topicID, &before)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ErrNotFound
	}
	if err != nil {
		return 0, 0, fmt.Errorf("board: locate post %d: %w", postID, err)
	}
	return topicID, (before-1)/postsPerPage + 1, nil
}

// AttachmentMeta describes a file that the viewer may download.
type AttachmentMeta struct {
	// Path is the path of the file under the data directory.
	Path, Mime, Name string
	Size             int64
}

// AttachmentFile checks whether v may download attachment id.
//
//   - The attachment does not exist, its post is not visible to v, or its forum
//     is not readable by v: [ErrNotFound].
//   - An attachment with no post shows only to its uploader and to an admin;
//     anyone else gets [ErrNotFound].
//   - The viewer needs account.download, and attach.download in the forum of the
//     post. A viewer who lacks one gets [ErrForbidden].
//
// It does not change the download count.
func AttachmentFile(ctx context.Context, q db.Querier, v Viewer, id int64) (AttachmentMeta, error) {
	var (
		m        AttachmentMeta
		postID   int64
		uploader int64
	)
	err := q.QueryRow(ctx, `
select stored_path, mime, real_name, size, coalesce(post_id, 0), coalesce(uploader_id, 0)
  from attachments where id = $1`, id).Scan(&m.Path, &m.Mime, &m.Name, &m.Size, &postID, &uploader)
	if errors.Is(err, pgx.ErrNoRows) {
		return AttachmentMeta{}, ErrNotFound
	}
	if err != nil {
		return AttachmentMeta{}, fmt.Errorf("board: read attachment %d: %w", id, err)
	}
	if postID == 0 {
		if !(v.UserID != 0 && v.UserID == uploader) && !v.Perms.IsAdmin() {
			return AttachmentMeta{}, ErrNotFound
		}
		if !v.Perms.Has("account.download", 0) {
			return AttachmentMeta{}, ErrForbidden
		}
		return m, nil
	}
	sc, err := newScope(ctx, q, v)
	if err != nil {
		return AttachmentMeta{}, err
	}
	var forumID int64
	err = q.QueryRow(ctx, `
select p.forum_id from posts p join topics t on t.id = p.topic_id
 where p.id = $5 and `+visible("p", 1)+` and `+visible("t", 1), append(sc.args(), postID)...).Scan(&forumID)
	if errors.Is(err, pgx.ErrNoRows) {
		return AttachmentMeta{}, ErrNotFound
	}
	if err != nil {
		return AttachmentMeta{}, fmt.Errorf("board: read post %d: %w", postID, err)
	}
	if !v.Perms.Has("account.download", 0) || !v.Perms.Has("attach.download", forumID) {
		return AttachmentMeta{}, ErrForbidden
	}
	return m, nil
}

// SmileyLookup reads the smilies with one query and returns a lookup for
// [markup.RenderContext]. The image of a smiley is the site path /files/ plus
// its image path, without the base path: the renderer adds that. Alt is the
// description of the smiley.
func SmileyLookup(ctx context.Context, q db.Querier) (func(code string) (markup.Smiley, bool), error) {
	list, err := smileyList(ctx, q)
	if err != nil {
		return nil, err
	}
	byCode := make(map[string]markup.Smiley, len(list))
	for _, s := range list {
		byCode[s.Code] = s
	}
	return func(code string) (markup.Smiley, bool) {
		s, ok := byCode[code]
		return s, ok
	}, nil
}
