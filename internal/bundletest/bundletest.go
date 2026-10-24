// Package bundletest builds small bundles for tests. [Small] returns a valid
// board that a test changes to make one fault, and [Write] puts a board on disk
// with [bundle.Writer].
//
// Every name, address and file in a board is made up. Emails use example.com, IP
// addresses are 10.x or in 2001:db8::/32, and the images are tiny byte literals.
package bundletest

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/bundle"
)

// Board is a whole bundle in memory: one slice for each data file, the settings
// and the files that the rows name. A test changes a board, then calls [Write].
// Write does not check the rows against each other, so a test can make a
// cross-row fault with a plain edit. Write does check each row alone, as
// [bundle.Add] does, and fails the test on a row that breaks a single-row rule.
type Board struct {
	// Source is the "source" label of the manifest.
	Source string
	// Settings is settings.json.
	Settings bundle.Settings

	Ranks        []bundle.Rank
	Users        []bundle.User
	GoneUsers    []bundle.GoneUser
	Groups       []bundle.Group
	GroupMembers []bundle.GroupMember
	Roles        []bundle.Role
	Grants       []bundle.Grant
	Forums       []bundle.Forum
	Topics       []bundle.Topic
	Posts        []bundle.Post
	Polls        []bundle.Poll
	PollVotes    []bundle.PollVote
	Attachments  []bundle.Attachment
	PMs          []bundle.PM
	TopicWatches []bundle.TopicWatch
	Bookmarks    []bundle.Bookmark
	TopicMarks   []bundle.TopicMark
	ForumMarks   []bundle.ForumMark
	Bans         []bundle.Ban
	Smilies      []bundle.Smiley
	LegacyRoutes []bundle.LegacyRoute
	LegacyFiles  []bundle.LegacyFile
	StaticPages  []bundle.StaticPage

	// Files maps a path under files/ to the content of the file. [Write] writes
	// each entry, whether or not a row names it.
	Files map[string][]byte
}

// Ref returns the file reference for content at path: the path, the size and
// the SHA-256.
func Ref(path string, content []byte) bundle.FileRef {
	sum := sha256.Sum256(content)
	return bundle.FileRef{Path: path, Size: int64(len(content)), SHA256: hex.EncodeToString(sum[:])}
}

// Write writes b into a new directory under t.TempDir() with [bundle.Create],
// [bundle.Add] and AddFile, and returns the bundle directory. The test fails on
// any error. Write sets created_at to the current time; a golden comparison
// must ignore that field.
func Write(t testing.TB, b *Board) string {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "bundle")
	w, err := bundle.Create(dir, b.Source, b.Settings)
	if err != nil {
		t.Fatalf("bundletest: Create: %v", err)
	}
	addAll(t, w, b.Ranks)
	addAll(t, w, b.Users)
	addAll(t, w, b.GoneUsers)
	addAll(t, w, b.Groups)
	addAll(t, w, b.GroupMembers)
	addAll(t, w, b.Roles)
	addAll(t, w, b.Grants)
	addAll(t, w, b.Forums)
	addAll(t, w, b.Topics)
	addAll(t, w, b.Posts)
	addAll(t, w, b.Polls)
	addAll(t, w, b.PollVotes)
	addAll(t, w, b.Attachments)
	addAll(t, w, b.PMs)
	addAll(t, w, b.TopicWatches)
	addAll(t, w, b.Bookmarks)
	addAll(t, w, b.TopicMarks)
	addAll(t, w, b.ForumMarks)
	addAll(t, w, b.Bans)
	addAll(t, w, b.Smilies)
	addAll(t, w, b.LegacyRoutes)
	addAll(t, w, b.LegacyFiles)
	addAll(t, w, b.StaticPages)

	scratch := t.TempDir()
	paths := make([]string, 0, len(b.Files))
	for p := range b.Files {
		paths = append(paths, p)
	}
	slices.Sort(paths)
	for i, p := range paths {
		src := filepath.Join(scratch, "f"+strconv.Itoa(i))
		if err := os.WriteFile(src, b.Files[p], 0o644); err != nil {
			t.Fatalf("bundletest: %v", err)
		}
		if _, err := w.AddFile(src, p); err != nil {
			t.Fatalf("bundletest: AddFile %s: %v", p, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("bundletest: Close: %v", err)
	}
	return dir
}

// addAll adds every row to w and fails the test on the first refusal.
func addAll[T bundle.Row](t testing.TB, w *bundle.Writer, rows []T) {
	t.Helper()
	for _, r := range rows {
		if err := bundle.Add(w, r); err != nil {
			t.Fatalf("bundletest: Add: %v", err)
		}
	}
}

// at returns a UTC time.
func at(year int, month time.Month, day, hour, min int) time.Time {
	return time.Date(year, month, day, hour, min, 0, 0, time.UTC)
}

// atp returns a pointer to a UTC time.
func atp(year int, month time.Month, day, hour, min int) *time.Time {
	v := at(year, month, day, hour, min)
	return &v
}

func ptr[T any](v T) *T { return &v }

// body returns a body with the three Markdown features on.
func body(source string) bundle.Body {
	return bundle.Body{Source: source, Markdown: true, Smilies: true, Autolink: true}
}

// The fixed password hashes of the made-up users. Each one is a hash of the
// made-up password "example-password". A fixed hash keeps the example bundle
// stable: a new hash from password.Hash has a new random salt every time.
const (
	hashArgonAlice = "$argon2id$v=19$m=19456,t=2,p=1$0czZGIdCG4fQnGDiTXK/HA$1CPqIWDJYPubMeBys3+v96mGtf+2DQYCRVJL45bNaEE"
	hashArgonCarol = "$argon2id$v=19$m=19456,t=2,p=1$1Z/LHMVLlkhdxiCwgTxwbQ$zcl7md1cN3yD1dSuhv/oRkTxwjhmw4yDB8+bcDyWsL4"
	hashBcryptBob  = "$2a$04$1d31JeOG67rsUSTX6ZyHgufpiXuCK7yO/8sJCyPZfhkQfefoUWWaK"
)

// Small returns a valid board that Check accepts. Later tests change it to make
// one fault. Each call returns a new board, so a test may change it freely.
//
// The IDs below are part of the contract: tests in other packages use them.
//
//	Settings    header image header/banner.png; time zone America/New_York
//	ranks       1 "Newcomer" (image ranks/newcomer.png)
//	users       1 alice  owner, active, argon2id ("gobb" scheme), rank 1, avatar
//	            2 bob    active, bcrypt cost 4 ("legacy" scheme), rank 1
//	            3 carol  active, argon2id, email not verified
//	            4 dave   inactive, empty legacy hash, empty email
//	gone_users  8
//	groups      1 guests, 2 members, 3 moderators, 4 admins (the built-in groups)
//	            5 "Beta testers" (kind request)
//	members     admins: 1 (leader). moderators: 2. members: 1, 2, 3, 4.
//	            Beta testers: 2 (leader), 3 (pending)
//	roles       1 "Standard posting" (forum role; holds topic.global never)
//	            2 "Member account" (board role)
//	grants      guests: forum.list on forums 1 and 2, forum.read on forum 2 only.
//	            members: forum.list on forums 1 and 2, forum.read yes on forum 2,
//	            role 1 on forum 2, role 2 board-wide.
//	            moderators: mod.edit and mod.approve board-wide. admins: admin
//	            board-wide, forum.list and forum.read on forum 3.
//	            Beta testers (group 5, with pending member carol): forum.list,
//	            forum.read and topic.reply on forum 3.
//	            user 4 (dave): forum.read never on forum 2, while dave is a
//	            confirmed member of members, which grants forum.read yes there.
//	forums      1 category "General"
//	            2 forum "Main Discussion", child of 1; guests can read it
//	            3 forum "Members Lounge", child of 2; guests cannot read it
//	topics      1 sticky, forum 2, by alice, 4 posts
//	            2 normal, forum 3, by bob, 2 posts, has the poll
//	            3 moved-topic marker in forum 3, moved_to 1, no posts
//	posts       1 topic 1, alice, approved first post, edited once by user 1
//	            2 topic 1, bob, approved reply; shows attachment 1 at /a/1
//	            3 topic 1, carol, unapproved reply
//	            4 topic 1, guest "Visitor" (no author ID), approved reply
//	            5 topic 2, bob, approved first post with the smiley :)
//	            6 topic 2, alice, approved reply
//	polls       topic 2: options 1 Green (3 votes), 2 Blue (2), 3 Red (0);
//	            max_options 2
//	poll_votes  alice voted option 1; bob voted option 2
//	attachments 1 on post 2, by bob (attachments/1.png, image/png)
//	            2 orphan, by alice (attachments/2.gif, image/gif)
//	pms         1 alice to bob, bcc carol. 2 bob to alice (author deleted)
//	topic_watches    bob watches topic 1
//	bookmarks        carol marks topic 2
//	topic_marks      bob, topic 1. forum_marks: bob, forum 2
//	bans        user 4 (ended), email spammer@example.com, ip 10.99.0.0/16
//	smilies     ":)" (smilies/smile.gif), ":D" (smilies/grin.png)
//	legacy_routes   1 old/thread.cgi?view=full -> topics, id_param "id",
//	                offset_param "page"
//	                2 old/profile.cgi -> user 8, a gone user (answers 410)
//	                3 old/about.cgi -> page "about"
//	legacy_files    old/images/smilies/ -> smilies
//	static_pages    "about" and "rules"
//
// Every user's password is "example-password", except dave, who has none.
func Small() *Board {
	files := map[string][]byte{
		"header/banner.png":  headerPNG,
		"ranks/newcomer.png": rankPNG,
		"avatars/1.png":      avatarPNG,
		"attachments/1.png":  attachmentPNG,
		"attachments/2.gif":  attachmentGIF,
		"smilies/smile.gif":  smileGIF,
		"smilies/grin.png":   grinPNG,
	}
	ref := func(path string) bundle.FileRef { return Ref(path, files[path]) }
	banner := ref("header/banner.png")

	b := &Board{
		Source: "example forum export",
		Settings: bundle.Settings{
			BoardName:          "Example Board",
			BoardDescription:   "A board for examples",
			Timezone:           "America/New_York",
			DefaultColorScheme: "slate",
			Registration:       "approval",
			EmailNotifications: true,
			HeaderImage:        &banner,
		},
		Files: files,
	}

	rankImg := ref("ranks/newcomer.png")
	b.Ranks = []bundle.Rank{
		{ID: 1, Title: "Newcomer", MinPosts: 0, Special: false, Image: &rankImg},
	}

	avatar := ref("avatars/1.png")
	b.Users = []bundle.User{
		{
			ID: 1, Username: "alice", Email: "alice@example.com",
			Password: bundle.Password{Scheme: "gobb", Hash: hashArgonAlice},
			State:    "active", Owner: true, EmailVerified: true,
			RegisteredAt: at(2019, 5, 1, 10, 0), LastVisitAt: atp(2026, 10, 1, 8, 30),
			PostCount: 120, RankID: ptr(int64(1)), Avatar: &avatar,
			Signature: body("Example signature"), Timezone: "America/New_York",
			ColorScheme: "slate", NotifyOnPost: true,
		},
		{
			ID: 2, Username: "bob", Email: "bob@example.com",
			Password: bundle.Password{Scheme: "legacy", Hash: hashBcryptBob},
			State:    "active", EmailVerified: true,
			RegisteredAt: at(2019, 6, 10, 14, 0), LastVisitAt: atp(2026, 9, 30, 19, 15),
			LastMarkAt: atp(2026, 9, 30, 19, 0),
			PostCount:  45, RankID: ptr(int64(1)),
			Signature: body(""), Timezone: "Europe/London",
			ColorScheme: "slate-deep", NotifyOnPost: true,
		},
		{
			ID: 3, Username: "carol", Email: "carol@example.com",
			Password:     bundle.Password{Scheme: "gobb", Hash: hashArgonCarol},
			State:        "active",
			RegisteredAt: at(2020, 1, 15, 9, 0),
			PostCount:    3,
			Signature:    body(""), Timezone: "UTC",
			ColorScheme: "slate-light",
		},
		{
			ID: 4, Username: "dave", Email: "",
			Password:     bundle.Password{Scheme: "legacy", Hash: ""},
			State:        "inactive",
			RegisteredAt: at(2020, 3, 3, 12, 0),
			Signature:    body(""), Timezone: "UTC",
			ColorScheme: "slate",
		},
	}
	b.GoneUsers = []bundle.GoneUser{{ID: 8}}

	b.Groups = []bundle.Group{
		{ID: 1, Name: "guests", Builtin: ptr("guests"), Kind: "builtin", Description: "Visitors who are not signed in"},
		{ID: 2, Name: "members", Builtin: ptr("members"), Kind: "builtin", Description: "Every registered user"},
		{ID: 3, Name: "moderators", Builtin: ptr("moderators"), Kind: "builtin", Description: "Moderators of the board"},
		{ID: 4, Name: "admins", Builtin: ptr("admins"), Kind: "builtin", Description: "Administrators of the board"},
		{ID: 5, Name: "Beta testers", Kind: "request", Description: "People who try new features"},
	}
	b.GroupMembers = []bundle.GroupMember{
		{GroupID: 4, UserID: 1, Leader: true},
		{GroupID: 3, UserID: 2},
		{GroupID: 2, UserID: 1},
		{GroupID: 2, UserID: 2},
		{GroupID: 2, UserID: 3},
		{GroupID: 2, UserID: 4},
		{GroupID: 5, UserID: 2, Leader: true},
		{GroupID: 5, UserID: 3, Pending: true},
	}

	b.Roles = []bundle.Role{
		{
			ID: 1, Name: "Standard posting", Kind: "forum", Description: "Read, post and vote", Position: 1,
			Settings: []bundle.RoleSetting{
				{Option: "topic.start", Setting: bundle.Yes},
				{Option: "topic.reply", Setting: bundle.Yes},
				{Option: "post.edit_own", Setting: bundle.Yes},
				{Option: "post.markdown", Setting: bundle.Yes},
				{Option: "post.smilies", Setting: bundle.Yes},
				{Option: "attach.download", Setting: bundle.Yes},
				{Option: "poll.vote", Setting: bundle.Yes},
				{Option: "topic.global", Setting: bundle.Never},
			},
		},
		{
			ID: 2, Name: "Member account", Kind: "board", Description: "Account and message rights", Position: 2,
			Settings: []bundle.RoleSetting{
				{Option: "pm.send", Setting: bundle.Yes},
				{Option: "pm.read", Setting: bundle.Yes},
				{Option: "pm.markdown", Setting: bundle.Yes},
				{Option: "pm.smilies", Setting: bundle.Yes},
				{Option: "account.signature", Setting: bundle.Yes},
				{Option: "account.avatar", Setting: bundle.Yes},
				{Option: "account.profile", Setting: bundle.Yes},
				{Option: "account.view_profiles", Setting: bundle.Yes},
				{Option: "account.search", Setting: bundle.Yes},
				{Option: "account.password", Setting: bundle.Yes},
				{Option: "account.email", Setting: bundle.Yes},
			},
		},
	}

	opt := func(group int64, forum int64, option string, s bundle.Setting) bundle.Grant {
		return bundle.Grant{GroupID: &group, ForumID: forum, Option: &option, Setting: &s}
	}
	role := func(group, forum, role int64) bundle.Grant {
		return bundle.Grant{GroupID: &group, ForumID: forum, RoleID: &role}
	}
	never := bundle.Never
	b.Grants = []bundle.Grant{
		opt(1, 1, "forum.list", bundle.Yes),
		opt(1, 2, "forum.list", bundle.Yes),
		opt(1, 2, "forum.read", bundle.Yes),
		opt(2, 1, "forum.list", bundle.Yes),
		opt(2, 2, "forum.list", bundle.Yes),
		opt(2, 2, "forum.read", bundle.Yes),
		role(2, 2, 1),
		role(2, 0, 2),
		opt(3, 0, "mod.edit", bundle.Yes),
		opt(3, 0, "mod.approve", bundle.Yes),
		opt(4, 0, "admin", bundle.Yes),
		opt(4, 3, "forum.list", bundle.Yes),
		opt(4, 3, "forum.read", bundle.Yes),
		opt(5, 3, "forum.list", bundle.Yes),
		opt(5, 3, "forum.read", bundle.Yes),
		opt(5, 3, "topic.reply", bundle.Yes),
		{UserID: ptr(int64(4)), ForumID: 2, Option: ptr("forum.read"), Setting: &never},
	}

	b.Forums = []bundle.Forum{
		{ID: 1, Kind: "category", Name: "General", Description: "Talk about anything", Position: 1},
		{ID: 2, ParentID: ptr(int64(1)), Kind: "forum", Name: "Main Discussion", Description: "The main forum", Rules: "Be kind.", Position: 1},
		{ID: 3, ParentID: ptr(int64(2)), Kind: "forum", Name: "Members Lounge", Description: "A quiet room", Position: 1},
	}

	b.Topics = []bundle.Topic{
		{ID: 1, ForumID: 2, Title: "Welcome to the example board", AuthorID: ptr(int64(1)), AuthorName: "alice", CreatedAt: at(2019, 5, 2, 9, 0), ViewCount: 42, Kind: "sticky", Visibility: "approved"},
		{ID: 2, ForumID: 3, Title: "Favorite color?", AuthorID: ptr(int64(2)), AuthorName: "bob", CreatedAt: at(2019, 7, 1, 18, 0), ViewCount: 9, Kind: "normal", Visibility: "approved"},
		{ID: 3, ForumID: 3, Title: "Welcome to the example board", AuthorID: ptr(int64(1)), AuthorName: "alice", CreatedAt: at(2019, 5, 2, 9, 0), Kind: "normal", Visibility: "approved", MovedTo: ptr(int64(1))},
	}

	b.Posts = []bundle.Post{
		{
			ID: 1, TopicID: 1, AuthorID: ptr(int64(1)), AuthorName: "alice", AuthorIP: ptr("10.0.0.5"),
			CreatedAt: at(2019, 5, 2, 9, 0), Subject: "Welcome",
			Body:          body("Welcome to the **example** board."),
			ShowSignature: true, Visibility: "approved",
			EditCount: 1, EditedAt: atp(2019, 5, 2, 9, 5), EditedBy: ptr(int64(1)), EditReason: "typo",
		},
		{
			ID: 2, TopicID: 1, AuthorID: ptr(int64(2)), AuthorName: "bob", AuthorIP: ptr("10.0.0.6"),
			CreatedAt: at(2019, 5, 2, 9, 30), Subject: "Re: Welcome",
			Body:       body("Thanks! Here is a diagram:\n\n![diagram](/a/1)"),
			Visibility: "approved",
		},
		{
			ID: 3, TopicID: 1, AuthorID: ptr(int64(3)), AuthorName: "carol", AuthorIP: ptr("10.0.0.7"),
			CreatedAt: at(2020, 1, 16, 10, 0), Subject: "Re: Welcome",
			Body:       body("This reply waits for a moderator."),
			Visibility: "unapproved",
		},
		{
			ID: 4, TopicID: 1, AuthorName: "Visitor", AuthorIP: ptr("2001:db8::7"),
			CreatedAt: at(2020, 2, 1, 11, 0), Subject: "Re: Welcome",
			Body:       body("Hello from a guest."),
			Visibility: "approved",
		},
		{
			ID: 5, TopicID: 2, AuthorID: ptr(int64(2)), AuthorName: "bob", AuthorIP: ptr("10.0.0.6"),
			CreatedAt: at(2019, 7, 1, 18, 0), Subject: "Favorite color?",
			Body:       body("Which color do you like best? :)"),
			Visibility: "approved",
		},
		{
			ID: 6, TopicID: 2, AuthorID: ptr(int64(1)), AuthorName: "alice", AuthorIP: ptr("10.0.0.5"),
			CreatedAt: at(2019, 7, 1, 19, 0), Subject: "Re: Favorite color?",
			Body:       body("Green, for me."),
			Visibility: "approved",
		},
	}

	b.Polls = []bundle.Poll{{
		TopicID: 2, Question: "Favorite color?", MaxOptions: 2, CanChangeVote: true,
		Options: []bundle.PollOption{
			{ID: 1, Text: "Green", Votes: 3},
			{ID: 2, Text: "Blue", Votes: 2},
			{ID: 3, Text: "Red", Votes: 0},
		},
	}}
	b.PollVotes = []bundle.PollVote{
		{TopicID: 2, OptionID: 1, UserID: 1, VoterIP: ptr("10.0.0.5")},
		{TopicID: 2, OptionID: 2, UserID: 2, VoterIP: ptr("2001:db8::6")},
	}

	b.Attachments = []bundle.Attachment{
		{
			ID: 1, PostID: ptr(int64(2)), Position: 0, UploaderID: ptr(int64(2)), Name: "diagram.png",
			File: ref("attachments/1.png"), Mime: "image/png", Width: ptr(1), Height: ptr(1),
			DownloadCount: 7, Comment: "A diagram", CreatedAt: at(2019, 5, 2, 9, 30),
		},
		{
			ID: 2, Position: 0, UploaderID: ptr(int64(1)), Name: "unused.gif",
			File: ref("attachments/2.gif"), Mime: "image/gif", Width: ptr(1), Height: ptr(1),
			CreatedAt: at(2019, 6, 1, 8, 0),
		},
	}

	b.PMs = []bundle.PM{
		{
			ID: 1, AuthorID: ptr(int64(1)), AuthorName: "alice", AuthorIP: ptr("10.0.0.5"), CreatedAt: at(2019, 6, 10, 14, 30),
			Subject: "Welcome aboard", Body: body("Glad you joined, bob."), ShowSignature: true,
			Recipients: []bundle.Recipient{
				{UserID: 2, Kind: "to", Read: true},
				{UserID: 3, Kind: "bcc"},
			},
		},
		{
			ID: 2, AuthorID: ptr(int64(2)), AuthorName: "bob", CreatedAt: at(2019, 6, 11, 7, 0),
			Subject: "Re: Welcome aboard", Body: body("Thanks, alice."), AuthorDeleted: true,
			Recipients: []bundle.Recipient{{UserID: 1, Kind: "to", Read: true, Deleted: true}},
		},
	}

	b.TopicWatches = []bundle.TopicWatch{{UserID: 2, TopicID: 1}}
	b.Bookmarks = []bundle.Bookmark{{UserID: 3, TopicID: 2}}
	b.TopicMarks = []bundle.TopicMark{{UserID: 2, TopicID: 1, MarkedAt: at(2026, 10, 1, 12, 0)}}
	b.ForumMarks = []bundle.ForumMark{{UserID: 2, ForumID: 2, MarkedAt: at(2026, 9, 30, 8, 0)}}

	b.Bans = []bundle.Ban{
		{Kind: "user", UserID: ptr(int64(4)), Reason: "Spam", ReasonShown: "Rule 1", CreatedAt: at(2020, 1, 1, 0, 0), EndsAt: atp(2020, 2, 1, 0, 0)},
		{Kind: "email", Email: ptr("spammer@example.com"), Reason: "Known spam address", CreatedAt: at(2020, 4, 1, 0, 0)},
		{Kind: "ip", IP: ptr("10.99.0.0/16"), Reason: "Open proxy", ReasonShown: "Proxies are not allowed", CreatedAt: at(2020, 5, 1, 0, 0)},
	}

	b.Smilies = []bundle.Smiley{
		{Code: ":)", Image: ref("smilies/smile.gif"), Description: "smile", Position: 1},
		{Code: ":D", Image: ref("smilies/grin.png"), Description: "grin", Position: 2},
	}

	b.LegacyRoutes = []bundle.LegacyRoute{
		{Path: "old/thread.cgi", Match: map[string]string{"view": "full"}, IDParam: ptr("id"), TargetKind: "topic", OffsetParam: ptr("page"), Position: 1},
		{Path: "old/profile.cgi", Match: map[string]string{}, TargetKind: "user", TargetID: ptr(int64(8)), Position: 2},
		{Path: "old/about.cgi", Match: map[string]string{}, TargetKind: "page", TargetSlug: ptr("about"), Position: 3},
	}
	b.LegacyFiles = []bundle.LegacyFile{{Prefix: "old/images/smilies/", Dir: "smilies"}}

	b.StaticPages = []bundle.StaticPage{
		{Slug: "about", Title: "About", Body: body("This is an example board."), UpdatedAt: at(2025, 6, 1, 12, 0)},
		{Slug: "rules", Title: "Rules", Body: body("1. Be kind.\n2. Stay on topic."), UpdatedAt: at(2025, 6, 2, 12, 0)},
	}
	return b
}

// Large returns a valid board with the given number of topics and the given
// number of posts in each topic, for scale tests. It keeps the users, forums,
// groups, grants and settings of [Small], and it replaces everything that hangs
// on topics and posts: the topics, posts, polls, votes, attachments, private
// messages, watches, bookmarks and marks. It drops the attachment files too.
//
// Topic t (counted from 1) lives in forum 2 when t is odd, and in forum 3 when
// t is even. Its posts are numbered in order across the board, so the post IDs
// run from 1 to topics*postsPerTopic. Each topic has a first post and
// postsPerTopic-1 replies, all approved. Authors rotate through users 1 to 3.
// Each body holds about 600 bytes of Markdown with emphasis, a link and a list.
// Both counts must be at least 1.
func Large(topics, postsPerTopic int) *Board {
	b := Small()
	b.Topics, b.Posts, b.Polls, b.PollVotes = nil, nil, nil, nil
	b.Attachments, b.PMs = nil, nil
	b.TopicWatches, b.Bookmarks, b.TopicMarks, b.ForumMarks = nil, nil, nil, nil
	delete(b.Files, "attachments/1.png")
	delete(b.Files, "attachments/2.gif")

	b.Topics = make([]bundle.Topic, 0, topics)
	b.Posts = make([]bundle.Post, 0, topics*postsPerTopic)
	names := []string{"alice", "bob", "carol"}
	start := at(2021, 1, 1, 0, 0)
	postID := int64(0)
	for t := 1; t <= topics; t++ {
		forum := int64(2)
		if t%2 == 0 {
			forum = 3
		}
		author := int64(t%3) + 1
		created := start.Add(time.Duration(t) * time.Hour)
		b.Topics = append(b.Topics, bundle.Topic{
			ID: int64(t), ForumID: forum, Title: "Topic " + strconv.Itoa(t),
			AuthorID: ptr(author), AuthorName: names[author-1], CreatedAt: created,
			ViewCount: t % 100, Kind: "normal", Visibility: "approved",
		})
		for p := 0; p < postsPerTopic; p++ {
			postID++
			who := int64((t+p)%3) + 1
			b.Posts = append(b.Posts, bundle.Post{
				ID: postID, TopicID: int64(t), AuthorID: ptr(who), AuthorName: names[who-1],
				AuthorIP:   ptr("10.0.0." + strconv.Itoa(int(who))),
				CreatedAt:  created.Add(time.Duration(p) * time.Minute),
				Subject:    "Topic " + strconv.Itoa(t),
				Body:       body(largeBody(t, p)),
				Visibility: "approved",
			})
		}
	}
	return b
}

// largeBody returns about 600 bytes of Markdown for post p of topic t.
func largeBody(t, p int) string {
	var sb strings.Builder
	sb.WriteString("Post " + strconv.Itoa(p+1) + " in topic " + strconv.Itoa(t) + ": this is *emphasized* text ")
	sb.WriteString("and this is **strong** text, with a [link](https://example.com/page/" + strconv.Itoa(t) + ").\n\n")
	sb.WriteString("- the first point has a few words in it\n- the second point has a few more words\n- the third point ends the list\n\n")
	for sb.Len() < 600 {
		sb.WriteString("The quick brown fox jumps over the lazy dog near the river bank. ")
	}
	return sb.String()
}
