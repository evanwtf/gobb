package board

import (
	"context"
	"errors"
	"fmt"
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

// The admin's share of the forum tree: the reader of the whole tree, and the
// writes that create, edit, move, reorder and lock a forum. Each write goes
// through [AdminWrite], so it writes its mod_log row in the same transaction.
//
// Each write first locks every row of forums, in ID order, and decides on that
// snapshot. Two admins who change the tree at once then run one after the
// other, and a move cannot make a cycle that the other move hides. The same lock
// is the one that a new topic takes on its forum, so a forum cannot gain a topic
// while the write checks that it has none. The rows stay few (a board has tens of
// forums), and the writes are rare.
//
// Nothing caches the tree. The pages read the table on each request, and the
// counters of a forum (topic_count, post_count, last_post_id) belong to the forum
// itself: [Index] adds up the descendants when it reads. So no write here changes
// a counter, and a change shows on the next request.

// The limits of the text fields of a forum.
const (
	maxForumNameRunes        = 100
	maxForumDescriptionRunes = 500
	maxForumRulesRunes       = 5000
	maxForumLinkRunes        = 2000
)

// ForumKinds are the kinds of a forum row, in the order that a form lists them.
var ForumKinds = []string{"category", "forum", "link"}

// AdminForum is one forum as the admin pages see it: every forum, with no check
// of the grants.
type AdminForum struct {
	ID int64
	// ParentID is 0 for a top-level forum.
	ParentID int64
	// Kind is "category", "forum" or "link".
	Kind        string
	Name        string
	Description string
	// Rules is plain text, as the import stores it.
	Rules   string
	LinkURL string
	Locked  bool
	// Position orders the forums under one parent; the tree is in this order.
	Position int
	// Topics and Posts are the counters of this forum alone, with no descendants.
	Topics, Posts int
	// HasTopics is true when any topic row has this forum, whatever its state:
	// the counters count approved topics only.
	HasTopics bool
	// Depth is 0 for a top-level forum.
	Depth    int
	Children []*AdminForum
}

// Flatten returns the forums of the trees in tree order: a forum, then its
// subforums.
func Flatten(trees []*AdminForum) []*AdminForum {
	var out []*AdminForum
	var walk func(list []*AdminForum)
	walk = func(list []*AdminForum) {
		for _, f := range list {
			out = append(out, f)
			walk(f.Children)
		}
	}
	walk(trees)
	return out
}

// AdminForums returns the top-level forums with all their subforums, in position
// and then ID order. It lists every forum, whatever the grants: a forum with no
// grant is in the list too. A viewer who is not an admin gets [ErrForbidden]. A
// forum whose chain of parents loops is not reachable and is left out; the
// writes below cannot make such a loop.
func AdminForums(ctx context.Context, q db.Querier, v Viewer) ([]*AdminForum, error) {
	if v.UserID == 0 || !v.Perms.IsAdmin() {
		return nil, ErrForbidden
	}
	rows, err := q.Query(ctx, `
select f.id, coalesce(f.parent_id, 0), f.kind, f.name, f.description, f.rules, coalesce(f.link_url, ''),
       f.locked, f.position, f.topic_count, f.post_count,
       exists (select 1 from topics t where t.forum_id = f.id)
  from forums f
 order by f.position, f.id`)
	if err != nil {
		return nil, fmt.Errorf("board: list forums for the admin: %w", err)
	}
	defer rows.Close()
	byID := map[int64]*AdminForum{}
	var all []*AdminForum
	for rows.Next() {
		f := &AdminForum{}
		if err := rows.Scan(&f.ID, &f.ParentID, &f.Kind, &f.Name, &f.Description, &f.Rules, &f.LinkURL,
			&f.Locked, &f.Position, &f.Topics, &f.Posts, &f.HasTopics); err != nil {
			return nil, fmt.Errorf("board: list forums for the admin: %w", err)
		}
		byID[f.ID] = f
		all = append(all, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: list forums for the admin: %w", err)
	}
	var roots []*AdminForum
	for _, f := range all {
		if f.ParentID == 0 {
			roots = append(roots, f)
		} else if p := byID[f.ParentID]; p != nil {
			p.Children = append(p.Children, f)
		}
	}
	var setDepth func(list []*AdminForum, depth int)
	setDepth = func(list []*AdminForum, depth int) {
		for _, f := range list {
			f.Depth = depth
			setDepth(f.Children, depth+1)
		}
	}
	setDepth(roots, 0)
	return roots, nil
}

// ForumInput is what the admin sends for a forum.
type ForumInput struct {
	// Kind is "category", "forum" or "link".
	Kind string
	// Name is one line of 1 to 100 characters.
	Name string
	// Description is one line of at most 500 characters.
	Description string
	// Rules is plain text of at most 5000 characters, with line breaks.
	Rules string
	// LinkURL is the target of a link, an http or https address. Another kind
	// ignores it and stores none.
	LinkURL string
	// ParentID is the parent of a new forum, or 0 for a top-level forum.
	// [UpdateForum] ignores it: [MoveForum] changes the parent.
	ParentID int64
	// GrantsFrom is the forum whose grants a new forum copies, or 0 to copy none.
	// [UpdateForum] ignores it.
	GrantsFrom int64
}

// cleanForumInput trims and checks the text fields of in. It does not check the
// link address, which needs the stored one: see [checkLinkURL]. It returns the
// cleaned input, or an [*InputError] for the first field that fails.
func cleanForumInput(in ForumInput) (ForumInput, error) {
	var err error
	if !slices.Contains(ForumKinds, in.Kind) {
		return in, inputError("kind", "Pick a kind from the list.")
	}
	if in.Name, err = cleanLine(in.Name, "name", maxForumNameRunes); err != nil {
		return in, err
	}
	if in.Description, err = cleanText(in.Description, "description", maxForumDescriptionRunes, false); err != nil {
		return in, err
	}
	if in.Rules, err = cleanText(in.Rules, "rules", maxForumRulesRunes, true); err != nil {
		return in, err
	}
	in.LinkURL = strings.TrimSpace(in.LinkURL)
	if in.Kind != "link" {
		in.LinkURL = ""
	}
	return in, nil
}

// cleanText trims a text field and applies the rules: valid UTF-8, at most limit
// characters, no control character, and no hidden formatting character. A line
// break is a control character, so it passes only when multiline is true, and
// then as "\n": "\r\n" and "\r" become "\n", the form that the import stores. A
// tab passes in a multiline field. An empty text is fine. field is the name of
// the form field, and the word in the messages.
func cleanText(s, field string, limit int, multiline bool) (string, error) {
	if multiline {
		s = strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\r", "\n")
	}
	s = strings.TrimSpace(s)
	check := s
	if multiline {
		check = strings.NewReplacer("\n", "", "\t", "").Replace(s)
	}
	switch {
	case !utf8.ValidString(s) || account.HasControl(check):
		if multiline {
			return "", inputError(field, "The "+field+" may not hold control characters.")
		}
		return "", inputError(field, "The "+field+" may not hold control characters or line breaks.")
	case account.HasHiddenFormat(s):
		return "", inputError(field, "Remove the hidden formatting character.")
	case utf8.RuneCountInString(s) > limit:
		return "", inputError(field, fmt.Sprintf("The %s is longer than %d characters.", field, limit))
	}
	return s, nil
}

// checkLinkURL checks the target of a link forum. It must pass
// [markup.CheckLinkURL], the one rule for link targets, and be an http or https
// address. The stored address passes unchanged, so that an admin can edit a link
// that the import brought with another allowed form, such as a site path.
func checkLinkURL(u, stored string) error {
	if u == "" {
		return inputError("link_url", "Enter the address of the link.")
	}
	if u == stored {
		return nil
	}
	lower := strings.ToLower(u)
	switch {
	case utf8.RuneCountInString(u) > maxForumLinkRunes:
		return inputError("link_url", fmt.Sprintf("The address is longer than %d characters.", maxForumLinkRunes))
	case !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://"),
		!markup.CheckLinkURL(u):
		return inputError("link_url", "Enter a full web address that starts with http:// or https://.")
	}
	return nil
}

// treeRow is a forum row with what the checks of a tree change need.
type treeRow struct {
	id, parent int64 // parent is 0 for a top-level forum
	kind       string
	position   int
	children   []*treeRow
}

// forumTree is the whole table of forums, read under a row lock.
type forumTree map[int64]*treeRow

// lockTree locks every forum row until the end of the transaction, in ID order,
// and returns the tree. It takes the lock that a topic write takes on its forum
// ([lockForum]), so the two kinds of write wait for each other. All the admin
// writes lock in the same order, so they cannot wait for each other in a circle.
func lockTree(ctx context.Context, tx pgx.Tx) (forumTree, error) {
	rows, err := tx.Query(ctx, `select id, coalesce(parent_id, 0), kind, position from forums order by id for no key update`)
	if err != nil {
		return nil, fmt.Errorf("board: lock the forums: %w", err)
	}
	defer rows.Close()
	t := forumTree{}
	var all []*treeRow
	for rows.Next() {
		r := &treeRow{}
		if err := rows.Scan(&r.id, &r.parent, &r.kind, &r.position); err != nil {
			return nil, fmt.Errorf("board: lock the forums: %w", err)
		}
		t[r.id] = r
		all = append(all, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("board: lock the forums: %w", err)
	}
	for _, r := range all {
		if p := t[r.parent]; p != nil {
			p.children = append(p.children, r)
		}
	}
	for _, r := range t {
		sortRows(r.children)
	}
	return t, nil
}

// sortRows puts forums in the order of the pages: position, then ID.
func sortRows(list []*treeRow) {
	slices.SortFunc(list, func(a, b *treeRow) int {
		if a.position != b.position {
			return a.position - b.position
		}
		return int(a.id - b.id)
	})
}

// siblings returns the forums under parent (0 for the top level) in page order.
func (t forumTree) siblings(parent int64) []*treeRow {
	if parent != 0 {
		if p := t[parent]; p != nil {
			return p.children
		}
		return nil
	}
	var out []*treeRow
	for _, r := range t {
		if r.parent == 0 {
			out = append(out, r)
		}
	}
	sortRows(out)
	return out
}

// under reports whether forum id is below ancestor (or is it), by walking the
// parents. A chain that does not end gives true: it is not a safe place.
func (t forumTree) under(id, ancestor int64) bool {
	for range len(t) + 1 {
		if id == ancestor {
			return true
		}
		r := t[id]
		if r == nil || r.parent == 0 {
			return false
		}
		id = r.parent
	}
	return true
}

// depth is the count of ancestors of forum id.
func (t forumTree) depth(id int64) int {
	n := 0
	for r := t[id]; r != nil && r.parent != 0 && n <= len(t); r = t[r.parent] {
		n++
	}
	return n
}

// height is the count of levels below forum id: 0 for a forum with no subforum.
func (t forumTree) height(id int64) int {
	h := 0
	for _, c := range t[id].children {
		h = max(h, 1+t.height(c.id))
	}
	return h
}

// nextPosition returns the position after the last forum under parent. The
// positions are 32-bit; a column at the top is renumbered by the caller.
func (t forumTree) nextPosition(parent int64) int {
	last := 0
	for _, r := range t.siblings(parent) {
		last = max(last, r.position)
	}
	return last + 1
}

// checkParent decides whether forum moving (0 for a forum that is new) may sit
// under parent (0 for the top level). A parent that is missing, a link, the forum
// itself or one of its subforums, or a place so deep that the index would not list
// the forum, gives an [*InputError] on "parent".
func (t forumTree) checkParent(parent, moving int64) error {
	if parent == 0 {
		return nil
	}
	p := t[parent]
	switch {
	case p == nil:
		return inputError("parent", "Pick a parent from the list.")
	case p.kind == "link":
		return inputError("parent", "A link has no subforums. Pick a category or a forum.")
	case moving != 0 && t.under(parent, moving):
		return inputError("parent", "A forum cannot go under itself or under one of its subforums.")
	}
	below := 0
	if moving != 0 {
		below = t.height(moving)
	}
	// Index lists a forum only when its chain of parents is under maxDepth.
	if t.depth(parent)+1+below >= maxDepth {
		return inputError("parent", "The forums would nest too deep. Pick a place higher up.")
	}
	return nil
}

// forumIDOrNil returns id as a query argument: nil for 0, the top level.
func forumIDOrNil(id int64) any {
	if id == 0 {
		return nil
	}
	return id
}

// CreateForum adds a category, a forum or a link as the admin v, at the end of the
// forums under in.ParentID, and returns its ID. It copies the grants of
// in.GrantsFrom to the new forum, both the group grants and the user grants, so
// that the people who may see that forum may see this one: without a grant,
// nobody can see a forum. It writes one forum.create row.
//
// A parent that is missing or a link, a source of grants that is missing, and a
// field that fails the checks give an [*InputError]; nothing is written then.
func CreateForum(ctx context.Context, pool *pgxpool.Pool, v Viewer, in ForumInput, now time.Time) (int64, error) {
	in, err := cleanForumInput(in)
	if err != nil {
		return 0, err
	}
	if in.Kind == "link" {
		if err := checkLinkURL(in.LinkURL, ""); err != nil {
			return 0, err
		}
	}
	var id int64
	err = AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		tree, err := lockTree(ctx, tx)
		if err != nil {
			return Change{}, err
		}
		if err := tree.checkParent(in.ParentID, 0); err != nil {
			return Change{}, err
		}
		if in.GrantsFrom != 0 && tree[in.GrantsFrom] == nil {
			return Change{}, inputError("grants_from", "Pick a forum from the list, or none.")
		}
		pos := tree.nextPosition(in.ParentID)
		if pos > maxInt32 {
			if err := renumber(ctx, tx, tree.siblings(in.ParentID)); err != nil {
				return Change{}, err
			}
			pos = len(tree.siblings(in.ParentID)) + 1
		}
		var link any
		if in.Kind == "link" {
			link = in.LinkURL
		}
		err = tx.QueryRow(ctx, `
insert into forums (parent_id, kind, name, description, rules, link_url, position)
values ($1, $2, $3, $4, $5, $6, $7) returning id`,
			forumIDOrNil(in.ParentID), in.Kind, in.Name, in.Description, in.Rules, link, pos).Scan(&id)
		if err != nil {
			return Change{}, fmt.Errorf("board: create forum: %w", err)
		}
		details := map[string]any{"kind": in.Kind, "name": in.Name, "parent": in.ParentID}
		if in.GrantsFrom != 0 {
			n, err := copyGrants(ctx, tx, in.GrantsFrom, id)
			if err != nil {
				return Change{}, err
			}
			details["grants_from"] = in.GrantsFrom
			details["grants_copied"] = n
		}
		return Change{Action: "forum.create", TargetType: "forum", TargetID: id, Details: details}, nil
	})
	if err != nil {
		return 0, err
	}
	return id, nil
}

// maxInt32 is the top of the position column.
const maxInt32 = 1<<31 - 1

// copyGrants gives forum to the groups and users that hold a grant on from, with
// the same role or option and setting. It returns the count of grants copied.
func copyGrants(ctx context.Context, tx pgx.Tx, from, to int64) (int64, error) {
	var total int64
	for _, q := range []string{
		`insert into acl_group_grants (group_id, forum_id, role_id, option, setting)
		 select group_id, $2, role_id, option, setting from acl_group_grants where forum_id = $1`,
		`insert into acl_user_grants (user_id, forum_id, role_id, option, setting)
		 select user_id, $2, role_id, option, setting from acl_user_grants where forum_id = $1`,
	} {
		tag, err := tx.Exec(ctx, q, from, to)
		if err != nil {
			return 0, fmt.Errorf("board: copy grants of forum %d: %w", from, err)
		}
		total += tag.RowsAffected()
	}
	return total, nil
}

// renumber rewrites the positions of list to 1, 2, 3, ... in the order of list,
// in one statement.
func renumber(ctx context.Context, tx pgx.Tx, list []*treeRow) error {
	ids := make([]int64, len(list))
	pos := make([]int32, len(list))
	for i, r := range list {
		ids[i], pos[i] = r.id, int32(i+1)
	}
	if _, err := tx.Exec(ctx, `
update forums set position = n.pos
  from unnest($1::bigint[], $2::int[]) as n(id, pos)
 where forums.id = n.id`, ids, pos); err != nil {
		return fmt.Errorf("board: renumber forums: %w", err)
	}
	return nil
}

// UpdateForum changes the kind, name, description, rules and link address of
// forum id as the admin v. in.ParentID and in.GrantsFrom are ignored. It writes
// one forum.update row that names the fields that changed, and answers
// [ErrNoChange] when nothing changed.
//
// A forum with topics cannot become a category or a link, and a forum with
// subforums cannot become a link: each gives an [*InputError] on "kind". A missing
// forum is [ErrNotFound].
func UpdateForum(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, in ForumInput, now time.Time) error {
	in, err := cleanForumInput(in)
	if err != nil {
		return err
	}
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		tree, err := lockTree(ctx, tx)
		if err != nil {
			return Change{}, err
		}
		cur := tree[id]
		if cur == nil {
			return Change{}, ErrNotFound
		}
		var old ForumInput
		if err := tx.QueryRow(ctx, `select kind, name, description, rules, coalesce(link_url, '') from forums where id = $1`, id).
			Scan(&old.Kind, &old.Name, &old.Description, &old.Rules, &old.LinkURL); err != nil {
			return Change{}, fmt.Errorf("board: read forum %d: %w", id, err)
		}
		if in.Kind == "link" {
			if err := checkLinkURL(in.LinkURL, old.LinkURL); err != nil {
				return Change{}, err
			}
		}
		if in.Kind != old.Kind && in.Kind != "forum" {
			var topics bool
			if err := tx.QueryRow(ctx, `select exists (select 1 from topics where forum_id = $1)`, id).Scan(&topics); err != nil {
				return Change{}, fmt.Errorf("board: read topics of forum %d: %w", id, err)
			}
			if topics {
				return Change{}, inputError("kind", "This forum holds topics, so it cannot become a category or a link.")
			}
		}
		if in.Kind == "link" && len(cur.children) > 0 {
			return Change{}, inputError("kind", "This forum has subforums, so it cannot become a link.")
		}
		var fields []string
		details := map[string]any{}
		if in.Kind != old.Kind {
			fields = append(fields, "kind")
			details["kind_from"], details["kind_to"] = old.Kind, in.Kind
		}
		if in.Name != old.Name {
			fields = append(fields, "name")
			details["name_from"], details["name_to"] = old.Name, in.Name
		}
		if in.Description != old.Description {
			fields = append(fields, "description")
		}
		if in.Rules != old.Rules {
			fields = append(fields, "rules")
		}
		if in.LinkURL != old.LinkURL {
			fields = append(fields, "link_url")
		}
		if len(fields) == 0 {
			return Change{}, ErrNoChange
		}
		var link any
		if in.Kind == "link" {
			link = in.LinkURL
		}
		if _, err := tx.Exec(ctx, `update forums set kind = $2, name = $3, description = $4, rules = $5, link_url = $6 where id = $1`,
			id, in.Kind, in.Name, in.Description, in.Rules, link); err != nil {
			return Change{}, fmt.Errorf("board: update forum %d: %w", id, err)
		}
		details["fields"] = fields
		return Change{Action: "forum.update", TargetType: "forum", TargetID: id, Details: details}, nil
	})
}

// MoveForum moves forum id under parent (0 for the top level) as the admin v, to
// the end of the forums there, and writes one forum.move row. The move keeps the
// subforums of the forum with it. A parent that is a link, the forum itself, one of
// its subforums, or so deep that the index would not list the subtree, gives an
// [*InputError] on "parent", and nothing changes. A move to the parent that the
// forum has gives [ErrNoChange]. A missing forum is [ErrNotFound].
func MoveForum(ctx context.Context, pool *pgxpool.Pool, v Viewer, id, parent int64, now time.Time) error {
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		tree, err := lockTree(ctx, tx)
		if err != nil {
			return Change{}, err
		}
		cur := tree[id]
		if cur == nil {
			return Change{}, ErrNotFound
		}
		if cur.parent == parent {
			return Change{}, ErrNoChange
		}
		if err := tree.checkParent(parent, id); err != nil {
			return Change{}, err
		}
		pos := tree.nextPosition(parent)
		if pos > maxInt32 {
			if err := renumber(ctx, tx, tree.siblings(parent)); err != nil {
				return Change{}, err
			}
			pos = len(tree.siblings(parent)) + 1
		}
		if _, err := tx.Exec(ctx, `update forums set parent_id = $2, position = $3 where id = $1`, id, forumIDOrNil(parent), pos); err != nil {
			return Change{}, fmt.Errorf("board: move forum %d: %w", id, err)
		}
		return Change{Action: "forum.move", TargetType: "forum", TargetID: id,
			Details: map[string]any{"from": cur.parent, "to": parent}}, nil
	})
}

// ReorderForum moves forum id one place up or down among the forums under its
// parent, as the admin v. dir is "up" or "down"; any other value is an
// [*InputError] on "dir". One transaction rewrites the positions of all the
// siblings to 1, 2, 3, ..., so no two share a position afterwards. The first forum
// cannot go up and the last cannot go down: [ErrNoChange]. It writes one
// forum.reorder row. A missing forum is [ErrNotFound].
func ReorderForum(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, dir string, now time.Time) error {
	if dir != "up" && dir != "down" {
		return inputError("dir", "Pick up or down.")
	}
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		tree, err := lockTree(ctx, tx)
		if err != nil {
			return Change{}, err
		}
		cur := tree[id]
		if cur == nil {
			return Change{}, ErrNotFound
		}
		sibs := slices.Clone(tree.siblings(cur.parent))
		i := slices.Index(sibs, cur)
		j := i - 1
		if dir == "down" {
			j = i + 1
		}
		if j < 0 || j >= len(sibs) {
			return Change{}, ErrNoChange
		}
		sibs[i], sibs[j] = sibs[j], sibs[i]
		if err := renumber(ctx, tx, sibs); err != nil {
			return Change{}, err
		}
		return Change{Action: "forum.reorder", TargetType: "forum", TargetID: id,
			Details: map[string]any{"parent": cur.parent, "dir": dir, "from": i + 1, "to": j + 1}}, nil
	})
}

// SetForumLock locks or unlocks forum id as the admin v, and writes a forum.lock
// or forum.unlock row. A forum that is in the state already gives [ErrNoChange].
// Only a forum holds topics, so a category or a link cannot be locked: an
// [*InputError] on "locked". It can be unlocked, because the import may have
// locked one. A missing forum is [ErrNotFound].
func SetForumLock(ctx context.Context, pool *pgxpool.Pool, v Viewer, id int64, locked bool, now time.Time) error {
	action := "forum.unlock"
	if locked {
		action = "forum.lock"
	}
	return AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		kind, was, err := lockForum(ctx, tx, id)
		switch {
		case errors.Is(err, ErrNotFound):
			return Change{}, ErrNotFound
		case err != nil:
			return Change{}, err
		case was == locked:
			return Change{}, ErrNoChange
		case locked && kind != "forum":
			return Change{}, inputError("locked", "Only a forum holds topics, so only a forum can be locked.")
		}
		if _, err := tx.Exec(ctx, `update forums set locked = $2 where id = $1`, id, locked); err != nil {
			return Change{}, fmt.Errorf("board: set the lock of forum %d: %w", id, err)
		}
		return Change{Action: action, TargetType: "forum", TargetID: id}, nil
	})
}
