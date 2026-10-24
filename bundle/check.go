package bundle

import (
	"cmp"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/evanwtf/gobb/internal/account"
)

// maxErrors is the number of errors that Check collects before it stops.
const maxErrors = 100

// Errors is the list of faults that [Check] found. It holds at most 100 errors,
// in file order.
type Errors []*Error

// Error returns the errors, one on each line.
func (e Errors) Error() string {
	lines := make([]string, len(e))
	for i, x := range e {
		lines[i] = x.Error()
	}
	return strings.Join(lines, "\n")
}

// Unwrap returns the errors, so that errors.As can find an *[Error] in the list.
func (e Errors) Unwrap() []error {
	out := make([]error, len(e))
	for i, x := range e {
		out[i] = x
	}
	return out
}

// errFull stops Each when Check has enough errors.
var errFull = errors.New("bundle: error limit reached")

// Check proves that the bundle in r is consistent across rows. It needs no
// database. It reads each data file once with [Each], in [Files] order, and it
// keeps only IDs, keys, small integers and the cleaned user names, never a row.
// A row that fails its own field checks stops Check, because the later rules
// would then report faults that the missing row causes.
//
// Check proves the rules that span rows:
//
//   - IDs are unique in each file, and so are the keys: the pair of a group
//     member, a watch, a bookmark and a read mark, the triple of a poll vote,
//     the code of a smiley, the pair (path, match) of a legacy route, the
//     prefix of a legacy file and the slug of a static page;
//   - every reference names a row that exists;
//   - each of the four built-in groups appears exactly once;
//   - at least one active user is an owner, and at least one active owner has a
//     password hash, so that an owner can log in;
//   - cleaned user names are unique;
//   - the forum parent chain has no cycle;
//   - a topic is in a forum of kind forum;
//   - a moved-topic marker has no posts and points at a topic that is not a
//     marker, and every other topic has a post;
//   - a poll has at most one row for a topic, an option has no more vote rows
//     than its votes, and a user has no more vote rows than max_options;
//   - a grant of a role fits its scope: a forum role needs a forum, a board
//     role needs forum_id 0, and a mod role fits both;
//   - a legacy route with a fixed target names a row that exists, and a page
//     route names a static page; a user route may name a gone user;
//   - every file that a row names exists under files/ with the stated size and
//     SHA-256, rows that name one path agree, and every file under files/ is
//     named by a row.
//
// Nothing references a gone user, except a user route with a fixed target.
//
// Check returns nil, or an [Errors] with up to 100 errors, sorted by file
// (in the order of [Files]) and line. Check stops at the 100th error it finds.
// Some rules are proved only when a file ends, so the 100 errors are the first
// 100 found, and a later error may sit earlier in file order than one of them.
// A fault in a whole file, such as a missing owner, has no line. A files/
// directory that is absent counts as an empty one. Check returns any other
// error unchanged when a file cannot be read.
func Check(r *Reader) error {
	w := &whole{
		r:     r,
		ranks: seenIDs{}, users: seenIDs{}, gone: seenIDs{}, groups: seenIDs{},
		posts: seenIDs{}, attachments: seenIDs{}, pms: seenIDs{},
		roles: map[int64]roleInfo{}, forums: map[int64]forumInfo{},
		topics: map[int64]topicInfo{}, polls: map[int64]pollInfo{},
		tallies: map[pollOption]tally{}, voters: map[pollVoter]int{},
		votes: map[voteKey]int{}, clean: map[string]int64{},
		builtin: map[string]int{}, named: map[string]*namedFile{},
		members: pairSet{}, watches: pairSet{}, bookmarks: pairSet{},
		topicMarks: pairSet{}, forumMarks: pairSet{},
		smileys: keySet{}, routes: keySet{}, prefixes: keySet{}, pages: keySet{},
	}
	if h := r.settings.HeaderImage; h != nil {
		w.nameFile("settings.json", 0, "header_image", *h)
	}

	scan(w, w.rank)
	scan(w, w.user)
	w.endUsers()
	scan(w, w.goneUser)
	scan(w, w.group)
	w.endGroups()
	scan(w, w.groupMember)
	scan(w, w.role)
	scan(w, w.grant)
	scan(w, w.forum)
	w.endForums()
	scan(w, w.topic)
	w.endTopics()
	scan(w, w.post)
	w.endPosts()
	scan(w, w.poll)
	scan(w, w.pollVote)
	scan(w, w.attachment)
	scan(w, w.pm)
	scan(w, w.topicWatch)
	scan(w, w.bookmark)
	scan(w, w.topicMark)
	scan(w, w.forumMark)
	scan(w, w.ban)
	scan(w, w.smiley)
	scan(w, w.route)
	scan(w, w.legacyFile)
	scan(w, w.staticPage)
	w.endPages()
	w.checkFiles()

	if w.fatal != nil {
		return w.fatal
	}
	if len(w.errs) == 0 {
		return nil
	}
	order := map[string]int{"settings.json": 0}
	for i, f := range Files {
		order[f+".jsonl"] = i + 1
	}
	rank := func(file string) int {
		if n, ok := order[file]; ok {
			return n
		}
		return len(Files) + 1
	}
	slices.SortStableFunc(w.errs, func(a, b *Error) int {
		return cmp.Or(cmp.Compare(rank(a.File), rank(b.File)), cmp.Compare(a.Line, b.Line))
	})
	return Errors(w.errs)
}

// seenIDs maps an ID to the line that holds it.
type seenIDs map[int64]int

// pairSet maps a pair of IDs to the line that holds it.
type pairSet map[[2]int64]int

// keySet maps a string key to the line that holds it.
type keySet map[string]int

type roleInfo struct {
	line int
	kind string
}

type forumInfo struct {
	line   int
	kind   string
	parent int64 // 0 when the forum has no parent
}

type topicInfo struct {
	line    int
	movedTo int64 // 0 when the topic is not a marker
	hasPost bool
}

type pollInfo struct {
	line int
	max  int
}

// pollOption names an option of the poll on a topic.
type pollOption struct {
	topic int64
	id    int
}

// tally counts the vote rows of one option against the votes it declares.
type tally struct{ allowed, used int }

// pollVoter names a user in the poll on a topic.
type pollVoter struct{ topic, user int64 }

// voteKey is the triple that is unique in poll_votes.
type voteKey struct {
	topic, user int64
	option      int
}

// lineRef is a reference that waits for a later file.
type lineRef struct {
	line int
	id   int64
}

type slugRef struct {
	line int
	slug string
}

// namedFile is a file that a row names. The first row that names it holds the
// reference.
type namedFile struct {
	file  string // data file of the first row, with its suffix
	line  int
	field string // field of the first row, for example "avatar"
	size  int64
	sha   string
}

// whole holds the state of one Check.
type whole struct {
	r     *Reader
	errs  []*Error
	halt  bool  // stop reading: the limit is reached, or a row failed
	fatal error // an error that is not a fault in the bundle

	ranks, users, gone, groups, posts, attachments, pms seenIDs
	roles                                               map[int64]roleInfo
	forums                                              map[int64]forumInfo
	forumOrder                                          []int64
	topics                                              map[int64]topicInfo
	topicOrder                                          []int64
	polls                                               map[int64]pollInfo
	tallies                                             map[pollOption]tally
	voters                                              map[pollVoter]int
	votes                                               map[voteKey]int

	clean       map[string]int64 // cleaned user name -> user ID
	activeOwner bool             // an active user is an owner
	loginOwner  bool             // an active owner has a password hash
	builtin     map[string]int   // built-in group name -> line

	members, watches, bookmarks, topicMarks, forumMarks pairSet
	smileys, routes, prefixes, pages                    keySet

	grantForums []lineRef // forum IDs that grants name, checked after forums
	pageRoutes  []slugRef // slugs that page routes name, checked after static_pages
	named       map[string]*namedFile
}

// scan reads one data file with Each and calls fn for each row.
func scan[T Row](w *whole, fn func(line int, row T)) {
	if w.halt {
		return
	}
	err := Each(w.r, func(line int, row T) error {
		fn(line, row)
		if w.full() {
			return errFull
		}
		return nil
	})
	var e *Error
	switch {
	case err == nil:
	case errors.Is(err, errFull):
		w.halt = true
	case errors.As(err, &e):
		w.errs = append(w.errs, e)
		w.halt = true
	default:
		w.fatal = err
		w.halt = true
	}
}

func (w *whole) full() bool { return len(w.errs) >= maxErrors }

// bad records a fault, unless the list is full.
func (w *whole) bad(file string, line int, field, format string, args ...any) {
	if w.full() {
		return
	}
	w.errs = append(w.errs, &Error{File: file, Line: line, Field: field, Msg: fmt.Sprintf(format, args...)})
}

// unique records id in set. It reports a fault, and returns false, when set
// holds id already.
func (w *whole) unique(file string, line int, set seenIDs, id int64) bool {
	if first, dup := set[id]; dup {
		w.bad(file, line, "id", "id %d appears already, on line %d", id, first)
		return false
	}
	set[id] = line
	return true
}

// uniqueKey is unique for a string key. what names the key in the message.
func (w *whole) uniqueKey(file string, line int, field, what string, set keySet, key string) bool {
	if first, dup := set[key]; dup {
		w.bad(file, line, field, "%s appears already, on line %d", what, first)
		return false
	}
	set[key] = line
	return true
}

// uniquePair is unique for a pair of IDs.
func (w *whole) uniquePair(file string, line int, set pairSet, f1 string, a int64, f2 string, b int64) {
	k := [2]int64{a, b}
	if first, dup := set[k]; dup {
		w.bad(file, line, f1, "the pair (%s %d, %s %d) appears already, on line %d", f1, a, f2, b, first)
		return
	}
	set[k] = line
}

// ref reports a fault when set lacks id. target is the file that should hold it.
func (w *whole) ref(file string, line int, field string, set seenIDs, id int64, target string) bool {
	if _, ok := set[id]; ok {
		return true
	}
	w.bad(file, line, field, "no row in %s has id %d", target, id)
	return false
}

// optRef is ref for a field that may be null.
func (w *whole) optRef(file string, line int, field string, set seenIDs, id *int64, target string) {
	if id != nil {
		w.ref(file, line, field, set, *id, target)
	}
}

// nameFile records that a row names the file f. A second row that names the same
// path must agree with the first on the size and the SHA-256.
func (w *whole) nameFile(file string, line int, field string, f FileRef) {
	sha := f.SHA256
	n, ok := w.named[f.Path]
	if !ok {
		w.named[f.Path] = &namedFile{file: file, line: line, field: field, size: f.Size, sha: sha}
		return
	}
	at := n.file
	if n.line > 0 {
		at += ":" + strconv.Itoa(n.line)
	}
	if f.Size != n.size {
		w.bad(file, line, field+".size", "is %d; %s names files/%s with size %d", f.Size, at, f.Path, n.size)
	}
	if sha != n.sha {
		w.bad(file, line, field+".sha256", "is %s; %s names files/%s with SHA-256 %s", sha, at, f.Path, n.sha)
	}
}

func (w *whole) rank(line int, r Rank) {
	const file = "ranks.jsonl"
	w.unique(file, line, w.ranks, r.ID)
	if r.Image != nil {
		w.nameFile(file, line, "image", *r.Image)
	}
}

func (w *whole) user(line int, u User) {
	const file = "users.jsonl"
	if !w.unique(file, line, w.users, u.ID) {
		return
	}
	w.optRef(file, line, "rank_id", w.ranks, u.RankID, "ranks.jsonl")
	if u.Avatar != nil {
		w.nameFile(file, line, "avatar", *u.Avatar)
	}
	c := account.CleanUsername(u.Username)
	if other, taken := w.clean[c]; taken {
		w.bad(file, line, "username", "user %d and user %d have the same cleaned username %q", u.ID, other, c)
	} else {
		w.clean[c] = u.ID
	}
	if u.Owner && u.State == "active" {
		w.activeOwner = true
		if u.Password.Hash != "" {
			w.loginOwner = true
		}
	}
}

func (w *whole) endUsers() {
	switch {
	case w.halt:
	case !w.activeOwner:
		w.bad("users.jsonl", 0, "owner", "no active user is an owner; the board needs one")
	case !w.loginOwner:
		w.bad("users.jsonl", 0, "owner", "no active owner has a password hash; the board needs an owner who can log in")
	}
}

func (w *whole) goneUser(line int, g GoneUser) {
	const file = "gone_users.jsonl"
	if uline, ok := w.users[g.ID]; ok {
		w.bad(file, line, "id", "id %d is also a user, on users.jsonl line %d", g.ID, uline)
		return
	}
	w.unique(file, line, w.gone, g.ID)
}

func (w *whole) group(line int, g Group) {
	const file = "groups.jsonl"
	w.unique(file, line, w.groups, g.ID)
	if g.Builtin == nil {
		return
	}
	if first, dup := w.builtin[*g.Builtin]; dup {
		w.bad(file, line, "builtin", "the built-in %s group appears already, on line %d", *g.Builtin, first)
		return
	}
	w.builtin[*g.Builtin] = line
}

func (w *whole) endGroups() {
	if w.halt {
		return
	}
	for _, name := range builtinGroups {
		if _, ok := w.builtin[name]; !ok {
			w.bad("groups.jsonl", 0, "builtin", "no group is the built-in %s group; each of the four appears exactly once", name)
		}
	}
}

func (w *whole) groupMember(line int, m GroupMember) {
	const file = "group_members.jsonl"
	w.ref(file, line, "group_id", w.groups, m.GroupID, "groups.jsonl")
	w.ref(file, line, "user_id", w.users, m.UserID, "users.jsonl")
	w.uniquePair(file, line, w.members, "group_id", m.GroupID, "user_id", m.UserID)
}

func (w *whole) role(line int, r Role) {
	const file = "roles.jsonl"
	if _, dup := w.roles[r.ID]; dup {
		w.bad(file, line, "id", "id %d appears already, on line %d", r.ID, w.roles[r.ID].line)
		return
	}
	w.roles[r.ID] = roleInfo{line: line, kind: r.Kind}
}

func (w *whole) grant(line int, g Grant) {
	const file = "grants.jsonl"
	w.optRef(file, line, "group_id", w.groups, g.GroupID, "groups.jsonl")
	w.optRef(file, line, "user_id", w.users, g.UserID, "users.jsonl")
	if g.ForumID != 0 {
		w.grantForums = append(w.grantForums, lineRef{line, g.ForumID})
	}
	if g.RoleID == nil {
		return
	}
	role, ok := w.roles[*g.RoleID]
	switch {
	case !ok:
		w.bad(file, line, "role_id", "no row in roles.jsonl has id %d", *g.RoleID)
	case role.kind == "forum" && g.ForumID == 0:
		w.bad(file, line, "forum_id", "role %d is a forum role; set forum_id to a forum", *g.RoleID)
	case role.kind == "board" && g.ForumID != 0:
		w.bad(file, line, "forum_id", "role %d is a board role; set forum_id to 0", *g.RoleID)
	}
}

func (w *whole) forum(line int, f Forum) {
	const file = "forums.jsonl"
	if first, dup := w.forums[f.ID]; dup {
		w.bad(file, line, "id", "id %d appears already, on line %d", f.ID, first.line)
		return
	}
	fi := forumInfo{line: line, kind: f.Kind}
	if f.ParentID != nil {
		fi.parent = *f.ParentID
	}
	w.forums[f.ID] = fi
	w.forumOrder = append(w.forumOrder, f.ID)
}

// endForums resolves the parent of each forum, finds parent cycles, and checks
// the forum IDs that grants name.
func (w *whole) endForums() {
	if w.halt {
		return
	}
	const file = "forums.jsonl"
	for _, id := range w.forumOrder {
		if fi := w.forums[id]; fi.parent != 0 {
			if _, ok := w.forums[fi.parent]; !ok {
				w.bad(file, fi.line, "parent_id", "no row in forums.jsonl has id %d", fi.parent)
			}
		}
	}
	state := map[int64]uint8{} // 1: on the current path, 2: done
	for _, id := range w.forumOrder {
		if state[id] != 0 {
			continue
		}
		var path []int64
		for cur := id; cur != 0; {
			if state[cur] == 2 {
				break
			}
			if state[cur] == 1 {
				w.reportCycle(path[slices.Index(path, cur):])
				break
			}
			state[cur] = 1
			path = append(path, cur)
			fi, ok := w.forums[cur]
			if !ok {
				break
			}
			cur = fi.parent
		}
		for _, p := range path {
			state[p] = 2
		}
	}
	for _, g := range w.grantForums {
		if _, ok := w.forums[g.id]; !ok {
			w.bad("grants.jsonl", g.line, "forum_id", "no row in forums.jsonl has id %d", g.id)
		}
	}
}

// reportCycle reports a parent cycle on its forum with the lowest line. cycle
// lists the forums, each the parent of the one before it.
func (w *whole) reportCycle(cycle []int64) {
	start := 0
	for i, id := range cycle {
		if w.forums[id].line < w.forums[cycle[start]].line {
			start = i
		}
	}
	const show = 20 // a long cycle would make a long message
	ids := make([]string, 0, show+1)
	for i := range min(len(cycle), show) {
		ids = append(ids, strconv.FormatInt(cycle[(start+i)%len(cycle)], 10))
	}
	if len(cycle) > show {
		ids = append(ids, "…")
	} else {
		ids = append(ids, ids[0])
	}
	first := cycle[start]
	w.bad("forums.jsonl", w.forums[first].line, "parent_id", "the parent chain loops: %s", strings.Join(ids, ", "))
}

func (w *whole) topic(line int, t Topic) {
	const file = "topics.jsonl"
	if first, dup := w.topics[t.ID]; dup {
		w.bad(file, line, "id", "id %d appears already, on line %d", t.ID, first.line)
		return
	}
	ti := topicInfo{line: line}
	if t.MovedTo != nil {
		ti.movedTo = *t.MovedTo
	}
	w.topics[t.ID] = ti
	w.topicOrder = append(w.topicOrder, t.ID)
	switch f, ok := w.forums[t.ForumID]; {
	case !ok:
		w.bad(file, line, "forum_id", "no row in forums.jsonl has id %d", t.ForumID)
	case f.kind != "forum":
		w.bad(file, line, "forum_id", "forum %d has kind %s; a topic needs a forum of kind forum", t.ForumID, f.kind)
	}
	w.optRef(file, line, "author_id", w.users, t.AuthorID, "users.jsonl")
}

// endTopics resolves the target of each moved-topic marker.
func (w *whole) endTopics() {
	if w.halt {
		return
	}
	for _, id := range w.topicOrder {
		ti := w.topics[id]
		if ti.movedTo == 0 {
			continue
		}
		switch target, ok := w.topics[ti.movedTo]; {
		case !ok:
			w.bad("topics.jsonl", ti.line, "moved_to", "no row in topics.jsonl has id %d", ti.movedTo)
		case target.movedTo != 0:
			w.bad("topics.jsonl", ti.line, "moved_to", "topic %d is itself a moved-topic marker; point at the topic it moved to", ti.movedTo)
		}
	}
}

func (w *whole) post(line int, p Post) {
	const file = "posts.jsonl"
	w.unique(file, line, w.posts, p.ID)
	switch ti, ok := w.topics[p.TopicID]; {
	case !ok:
		w.bad(file, line, "topic_id", "no row in topics.jsonl has id %d", p.TopicID)
	case ti.movedTo != 0:
		w.bad(file, line, "topic_id", "topic %d is a moved-topic marker; it has no posts", p.TopicID)
	case !ti.hasPost:
		ti.hasPost = true
		w.topics[p.TopicID] = ti
	}
	w.optRef(file, line, "author_id", w.users, p.AuthorID, "users.jsonl")
	w.optRef(file, line, "edited_by", w.users, p.EditedBy, "users.jsonl")
}

// endPosts reports each topic that is not a marker and has no post.
func (w *whole) endPosts() {
	if w.halt {
		return
	}
	for _, id := range w.topicOrder {
		if ti := w.topics[id]; ti.movedTo == 0 && !ti.hasPost {
			w.bad("topics.jsonl", ti.line, "id", "topic %d has no post; only a moved-topic marker has none", id)
		}
	}
}

func (w *whole) poll(line int, p Poll) {
	const file = "polls.jsonl"
	if !w.topicRef(file, line, "topic_id", p.TopicID) {
		return
	}
	if first, dup := w.polls[p.TopicID]; dup {
		w.bad(file, line, "topic_id", "topic %d has a poll already, on line %d", p.TopicID, first.line)
		return
	}
	w.polls[p.TopicID] = pollInfo{line: line, max: p.MaxOptions}
	for _, o := range p.Options {
		w.tallies[pollOption{p.TopicID, o.ID}] = tally{allowed: o.Votes}
	}
}

// topicRef reports a fault when no topic has id.
func (w *whole) topicRef(file string, line int, field string, id int64) bool {
	if _, ok := w.topics[id]; ok {
		return true
	}
	w.bad(file, line, field, "no row in topics.jsonl has id %d", id)
	return false
}

func (w *whole) pollVote(line int, v PollVote) {
	const file = "poll_votes.jsonl"
	w.ref(file, line, "user_id", w.users, v.UserID, "users.jsonl")
	poll, ok := w.polls[v.TopicID]
	if !ok {
		w.bad(file, line, "topic_id", "topic %d has no poll", v.TopicID)
		return
	}
	opt := pollOption{v.TopicID, v.OptionID}
	t, ok := w.tallies[opt]
	if !ok {
		w.bad(file, line, "option_id", "the poll on topic %d has no option %d", v.TopicID, v.OptionID)
		return
	}
	k := voteKey{v.TopicID, v.UserID, v.OptionID}
	if first, dup := w.votes[k]; dup {
		w.bad(file, line, "user_id", "the triple (topic_id %d, option_id %d, user_id %d) appears already, on line %d", v.TopicID, v.OptionID, v.UserID, first)
		return
	}
	w.votes[k] = line
	t.used++
	w.tallies[opt] = t
	if t.used == t.allowed+1 {
		w.bad(file, line, "option_id", "option %d of the poll on topic %d has votes %d; this is vote row %d for it", v.OptionID, v.TopicID, t.allowed, t.used)
	}
	who := pollVoter{v.TopicID, v.UserID}
	w.voters[who]++
	if n := w.voters[who]; n == poll.max+1 {
		w.bad(file, line, "user_id", "user %d has vote row %d in the poll on topic %d; max_options is %d", v.UserID, n, v.TopicID, poll.max)
	}
}

func (w *whole) attachment(line int, a Attachment) {
	const file = "attachments.jsonl"
	w.unique(file, line, w.attachments, a.ID)
	w.optRef(file, line, "post_id", w.posts, a.PostID, "posts.jsonl")
	w.optRef(file, line, "uploader_id", w.users, a.UploaderID, "users.jsonl")
	w.nameFile(file, line, "file", a.File)
}

func (w *whole) pm(line int, m PM) {
	const file = "pms.jsonl"
	w.unique(file, line, w.pms, m.ID)
	w.optRef(file, line, "author_id", w.users, m.AuthorID, "users.jsonl")
	for i, r := range m.Recipients {
		w.ref(file, line, "recipients["+strconv.Itoa(i)+"].user_id", w.users, r.UserID, "users.jsonl")
	}
}

func (w *whole) topicWatch(line int, x TopicWatch) {
	const file = "topic_watches.jsonl"
	w.ref(file, line, "user_id", w.users, x.UserID, "users.jsonl")
	w.topicRef(file, line, "topic_id", x.TopicID)
	w.uniquePair(file, line, w.watches, "user_id", x.UserID, "topic_id", x.TopicID)
}

func (w *whole) bookmark(line int, x Bookmark) {
	const file = "bookmarks.jsonl"
	w.ref(file, line, "user_id", w.users, x.UserID, "users.jsonl")
	w.topicRef(file, line, "topic_id", x.TopicID)
	w.uniquePair(file, line, w.bookmarks, "user_id", x.UserID, "topic_id", x.TopicID)
}

func (w *whole) topicMark(line int, x TopicMark) {
	const file = "topic_marks.jsonl"
	w.ref(file, line, "user_id", w.users, x.UserID, "users.jsonl")
	w.topicRef(file, line, "topic_id", x.TopicID)
	w.uniquePair(file, line, w.topicMarks, "user_id", x.UserID, "topic_id", x.TopicID)
}

func (w *whole) forumMark(line int, x ForumMark) {
	const file = "forum_marks.jsonl"
	w.ref(file, line, "user_id", w.users, x.UserID, "users.jsonl")
	if _, ok := w.forums[x.ForumID]; !ok {
		w.bad(file, line, "forum_id", "no row in forums.jsonl has id %d", x.ForumID)
	}
	w.uniquePair(file, line, w.forumMarks, "user_id", x.UserID, "forum_id", x.ForumID)
}

func (w *whole) ban(line int, b Ban) {
	w.optRef("bans.jsonl", line, "user_id", w.users, b.UserID, "users.jsonl")
}

func (w *whole) smiley(line int, s Smiley) {
	const file = "smilies.jsonl"
	w.uniqueKey(file, line, "code", "code "+strconv.Quote(s.Code), w.smileys, s.Code)
	w.nameFile(file, line, "image", s.Image)
}

// routeKey returns the key of a route: its path, its match pairs and its
// id_param. Values never hold control characters, so the NUL and SOH separators
// keep the key unambiguous.
func routeKey(r LegacyRoute) string {
	var b strings.Builder
	b.WriteString(r.Path)
	for _, k := range sortedKeys(r.Match) {
		b.WriteByte(0)
		b.WriteString(k)
		b.WriteByte(0)
		b.WriteString(r.Match[k])
	}
	if r.IDParam != nil {
		b.WriteByte(1)
		b.WriteString(*r.IDParam)
	}
	return b.String()
}

func (w *whole) route(line int, r LegacyRoute) {
	const file = "legacy_routes.jsonl"
	if first, dup := w.routes[routeKey(r)]; dup {
		w.bad(file, line, "path", "the triple (path, match, id_param) appears already, on line %d", first)
	} else {
		w.routes[routeKey(r)] = line
	}
	if r.TargetSlug != nil {
		w.pageRoutes = append(w.pageRoutes, slugRef{line, *r.TargetSlug})
	}
	if r.TargetID == nil {
		return
	}
	id := *r.TargetID
	switch r.TargetKind {
	case "forum":
		if _, ok := w.forums[id]; !ok {
			w.bad(file, line, "target_id", "no row in forums.jsonl has id %d", id)
		}
	case "topic":
		if _, ok := w.topics[id]; !ok {
			w.bad(file, line, "target_id", "no row in topics.jsonl has id %d", id)
		}
	case "post":
		w.ref(file, line, "target_id", w.posts, id, "posts.jsonl")
	case "attachment":
		w.ref(file, line, "target_id", w.attachments, id, "attachments.jsonl")
	case "avatar":
		w.ref(file, line, "target_id", w.users, id, "users.jsonl")
	case "user":
		_, user := w.users[id]
		_, gone := w.gone[id]
		if !user && !gone {
			w.bad(file, line, "target_id", "no row in users.jsonl or gone_users.jsonl has id %d", id)
		}
	}
}

func (w *whole) legacyFile(line int, f LegacyFile) {
	w.uniqueKey("legacy_files.jsonl", line, "prefix", "prefix "+strconv.Quote(f.Prefix), w.prefixes, f.Prefix)
}

func (w *whole) staticPage(line int, p StaticPage) {
	w.uniqueKey("static_pages.jsonl", line, "slug", "slug "+strconv.Quote(p.Slug), w.pages, p.Slug)
}

// endPages checks the slugs that page routes name.
func (w *whole) endPages() {
	if w.halt {
		return
	}
	for _, p := range w.pageRoutes {
		if _, ok := w.pages[p.slug]; !ok {
			w.bad("legacy_routes.jsonl", p.line, "target_slug", "no row in static_pages.jsonl has slug %s", p.slug)
		}
	}
}

// checkFiles proves the files rule. It lists files/ once, and reads each file
// that a row names once.
func (w *whole) checkFiles() {
	if w.halt {
		return
	}
	root := filepath.Join(w.r.dir, "files")
	seen := map[string]bool{}
	// Open accepts a bundle with no files/ directory: it is an empty tree.
	if _, err := os.Lstat(root); errors.Is(err, fs.ErrNotExist) {
		w.reportMissing(seen)
		return
	}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if w.full() {
			return filepath.SkipAll
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if n, ok := w.named[rel]; ok {
				// A row names a directory: it is there, but it is not a file.
				seen[rel] = true
				w.bad(n.file, n.line, n.field+".path", "files/%s is not a regular file", rel)
			}
			return nil
		}
		n, ok := w.named[rel]
		if !ok {
			w.bad("files/"+rel, 0, "", "no row names this file")
			return nil
		}
		seen[rel] = true
		if !d.Type().IsRegular() {
			w.bad(n.file, n.line, n.field+".path", "files/%s is not a regular file", rel)
			return nil
		}
		return w.verifyFile(path, rel, n)
	})
	if err != nil {
		w.fatal = err
		return
	}
	w.reportMissing(seen)
}

// reportMissing reports each named path that the walk did not see.
func (w *whole) reportMissing(seen map[string]bool) {
	if w.full() {
		return
	}
	var missing []string
	for p := range w.named {
		if !seen[p] {
			missing = append(missing, p)
		}
	}
	slices.SortFunc(missing, func(a, b string) int {
		na, nb := w.named[a], w.named[b]
		return cmp.Or(cmp.Compare(na.file, nb.file), cmp.Compare(na.line, nb.line), cmp.Compare(a, b))
	})
	for _, p := range missing {
		n := w.named[p]
		w.bad(n.file, n.line, n.field+".path", "files/%s does not exist", p)
	}
}

// verifyFile compares the size and the SHA-256 of the file at path with the
// first row that names it.
func (w *whole) verifyFile(path, rel string, n *namedFile) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	size, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if size != n.size {
		w.bad(n.file, n.line, n.field+".size", "is %d; files/%s has %d bytes", n.size, rel, size)
		return nil
	}
	if sum := hex.EncodeToString(h.Sum(nil)); sum != n.sha {
		w.bad(n.file, n.line, n.field+".sha256", "is %s; the SHA-256 of files/%s is %s", n.sha, rel, sum)
	}
	return nil
}
