package bundle_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/evanwtf/gobb/bundle"
	"github.com/evanwtf/gobb/internal/bundletest"
)

// checkBoard writes b, opens it and runs Check.
func checkBoard(t *testing.T, b *bundletest.Board) error {
	t.Helper()
	r, err := bundle.Open(bundletest.Write(t, b))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return bundle.Check(r)
}

func p[T any](v T) *T { return &v }

func TestCheckSmallBoard(t *testing.T) {
	if err := checkBoard(t, bundletest.Small()); err != nil {
		t.Fatalf("Check(Small) = %v", err)
	}
}

func TestCheckRefuses(t *testing.T) {
	zeros := strings.Repeat("0", 64)
	cases := []struct {
		name   string
		mutate func(b *bundletest.Board)
		want   string
	}{
		{"duplicate user ID", func(b *bundletest.Board) {
			u := b.Users[0]
			u.Username = "eve"
			u.Avatar = nil
			u.Owner = false
			u.ID = 2
			b.Users = append(b.Users, u)
		}, "users.jsonl:5: id: id 2 appears already, on line 2"},
		{"user ID that is also a gone user", func(b *bundletest.Board) {
			b.GoneUsers = append(b.GoneUsers, bundle.GoneUser{ID: 2})
		}, "gone_users.jsonl:2: id: id 2 is also a user, on users.jsonl line 2"},
		{"duplicate gone user", func(b *bundletest.Board) {
			b.GoneUsers = append(b.GoneUsers, bundle.GoneUser{ID: 8})
		}, "gone_users.jsonl:2: id: id 8 appears already, on line 1"},
		{"rank that does not exist", func(b *bundletest.Board) {
			b.Users[2].RankID = p(int64(9))
		}, "users.jsonl:3: rank_id: no row in ranks.jsonl has id 9"},
		{"post with a missing topic", func(b *bundletest.Board) {
			po := b.Posts[0]
			po.ID, po.TopicID = 7, 99
			b.Posts = append(b.Posts, po)
		}, "posts.jsonl:7: topic_id: no row in topics.jsonl has id 99"},
		{"edited_by that does not exist", func(b *bundletest.Board) {
			b.Posts[0].EditedBy = p(int64(99))
		}, "posts.jsonl:1: edited_by: no row in users.jsonl has id 99"},
		{"topic author that does not exist", func(b *bundletest.Board) {
			b.Topics[1].AuthorID = p(int64(99))
		}, "topics.jsonl:2: author_id: no row in users.jsonl has id 99"},
		{"attachment on a missing post", func(b *bundletest.Board) {
			b.Attachments[0].PostID = p(int64(99))
		}, "attachments.jsonl:1: post_id: no row in posts.jsonl has id 99"},
		{"missing admins group", func(b *bundletest.Board) {
			b.Groups = append(b.Groups[:3:3], b.Groups[4])
			var gm []bundle.GroupMember
			for _, m := range b.GroupMembers {
				if m.GroupID != 4 {
					gm = append(gm, m)
				}
			}
			b.GroupMembers = gm
			var gr []bundle.Grant
			for _, g := range b.Grants {
				if g.GroupID == nil || *g.GroupID != 4 {
					gr = append(gr, g)
				}
			}
			b.Grants = gr
		}, "groups.jsonl: builtin: no group is the built-in admins group"},
		{"built-in group twice", func(b *bundletest.Board) {
			b.Groups = append(b.Groups, bundle.Group{ID: 6, Name: "guests again", Builtin: p("guests"), Kind: "builtin"})
		}, "groups.jsonl:6: builtin: the built-in guests group appears already, on line 1"},
		{"duplicate group member", func(b *bundletest.Board) {
			b.GroupMembers = append(b.GroupMembers, b.GroupMembers[0])
		}, "group_members.jsonl:9: group_id: the pair (group_id 4, user_id 1) appears already, on line 1"},
		{"no owner", func(b *bundletest.Board) {
			for i := range b.Users {
				b.Users[i].Owner = false
			}
		}, "users.jsonl: owner: no active user is an owner"},
		{"the only owner is inactive", func(b *bundletest.Board) {
			b.Users[0].State = "inactive"
		}, "users.jsonl: owner: no active user is an owner"},
		{"the only active owner cannot log in", func(b *bundletest.Board) {
			b.Users[0].Password = bundle.Password{Scheme: "legacy", Hash: ""}
		}, "users.jsonl: owner: no active owner has a password hash"},
		{"an owner with a hash is inactive, the active owner has none", func(b *bundletest.Board) {
			b.Users[0].Password = bundle.Password{Scheme: "legacy", Hash: ""}
			b.Users[1].Owner = true
			b.Users[1].State = "inactive"
		}, "users.jsonl: owner: no active owner has a password hash"},
		{"pm author that does not exist", func(b *bundletest.Board) {
			b.PMs[0].AuthorID = p(int64(99))
		}, "pms.jsonl:1: author_id: no row in users.jsonl has id 99"},
		{"attachment uploader that does not exist", func(b *bundletest.Board) {
			b.Attachments[0].UploaderID = p(int64(99))
		}, "attachments.jsonl:1: uploader_id: no row in users.jsonl has id 99"},
		{"pm from a gone author", func(b *bundletest.Board) {
			b.PMs[0].AuthorID, b.PMs[0].AuthorName = nil, "ghost"
		}, ""},
		{"pm from a live author with no recipients", func(b *bundletest.Board) {
			b.PMs[0].Recipients = nil
		}, ""},
		{"attachment from a gone uploader", func(b *bundletest.Board) {
			b.Attachments[0].UploaderID = nil
		}, ""},
		{"forum cycle", func(b *bundletest.Board) {
			b.Forums[0].ParentID = p(int64(3))
		}, "forums.jsonl:1: parent_id: the parent chain loops: 1, 3, 2, 1"},
		{"forum parent that does not exist", func(b *bundletest.Board) {
			b.Forums[2].ParentID = p(int64(99))
		}, "forums.jsonl:3: parent_id: no row in forums.jsonl has id 99"},
		{"topic in a category", func(b *bundletest.Board) {
			b.Topics = append(b.Topics, bundle.Topic{ID: 4, ForumID: 1, Title: "t", AuthorName: "x", CreatedAt: b.Topics[0].CreatedAt, Kind: "normal", Visibility: "approved"})
			po := b.Posts[0]
			po.ID, po.TopicID = 7, 4
			b.Posts = append(b.Posts, po)
		}, "topics.jsonl:4: forum_id: forum 1 has kind category; a topic needs a forum of kind forum"},
		{"topic in a missing forum", func(b *bundletest.Board) {
			b.Topics[1].ForumID = 99
		}, "topics.jsonl:2: forum_id: no row in forums.jsonl has id 99"},
		{"non-marker topic with no posts", func(b *bundletest.Board) {
			b.Topics = append(b.Topics, bundle.Topic{ID: 4, ForumID: 2, Title: "t", AuthorName: "x", CreatedAt: b.Topics[0].CreatedAt, Kind: "normal", Visibility: "approved"})
		}, "topics.jsonl:4: id: topic 4 has no post; only a moved-topic marker has none"},
		{"marker with a post", func(b *bundletest.Board) {
			po := b.Posts[0]
			po.ID, po.TopicID = 7, 3
			b.Posts = append(b.Posts, po)
		}, "posts.jsonl:7: topic_id: topic 3 is a moved-topic marker; it has no posts"},
		{"marker that points to a marker", func(b *bundletest.Board) {
			b.Topics = append(b.Topics, bundle.Topic{ID: 4, ForumID: 2, Title: "t", AuthorName: "x", CreatedAt: b.Topics[0].CreatedAt, Kind: "normal", Visibility: "approved", MovedTo: p(int64(3))})
		}, "topics.jsonl:4: moved_to: topic 3 is itself a moved-topic marker"},
		{"marker that points to a missing topic", func(b *bundletest.Board) {
			b.Topics[2].MovedTo = p(int64(99))
		}, "topics.jsonl:3: moved_to: no row in topics.jsonl has id 99"},
		{"poll on a missing topic", func(b *bundletest.Board) {
			po := b.Polls[0]
			po.TopicID = 99
			b.Polls = append(b.Polls, po)
		}, "polls.jsonl:2: topic_id: no row in topics.jsonl has id 99"},
		{"two polls on one topic", func(b *bundletest.Board) {
			b.Polls = append(b.Polls, b.Polls[0])
		}, "polls.jsonl:2: topic_id: topic 2 has a poll already, on line 1"},
		{"vote for an option the poll lacks", func(b *bundletest.Board) {
			b.PollVotes = append(b.PollVotes, bundle.PollVote{TopicID: 2, OptionID: 9, UserID: 1})
		}, "poll_votes.jsonl:3: option_id: the poll on topic 2 has no option 9"},
		{"vote on a topic with no poll", func(b *bundletest.Board) {
			b.PollVotes = append(b.PollVotes, bundle.PollVote{TopicID: 1, OptionID: 1, UserID: 1})
		}, "poll_votes.jsonl:3: topic_id: topic 1 has no poll"},
		{"duplicate vote row", func(b *bundletest.Board) {
			b.PollVotes = append(b.PollVotes, b.PollVotes[0])
		}, "poll_votes.jsonl:3: user_id: the triple (topic_id 2, option_id 1, user_id 1) appears already, on line 1"},
		{"3 vote rows on an option with votes 2", func(b *bundletest.Board) {
			b.Polls[0].Options[0].Votes = 2
			b.PollVotes = append(b.PollVotes,
				bundle.PollVote{TopicID: 2, OptionID: 1, UserID: 3},
				bundle.PollVote{TopicID: 2, OptionID: 1, UserID: 4})
		}, "poll_votes.jsonl:4: option_id: option 1 of the poll on topic 2 has votes 2; this is vote row 3 for it"},
		{"more vote rows by a user than max_options", func(b *bundletest.Board) {
			b.Polls[0].Options[2].Votes = 1
			b.PollVotes = append(b.PollVotes,
				bundle.PollVote{TopicID: 2, OptionID: 2, UserID: 1},
				bundle.PollVote{TopicID: 2, OptionID: 3, UserID: 1})
		}, "poll_votes.jsonl:4: user_id: user 1 has vote row 3 in the poll on topic 2; max_options is 2"},
		{"recipient that is not a user", func(b *bundletest.Board) {
			b.PMs[0].Recipients = append(b.PMs[0].Recipients, bundle.Recipient{UserID: 99, Kind: "bcc"})
		}, "pms.jsonl:1: recipients[2].user_id: no row in users.jsonl has id 99"},
		{"bookmark twice", func(b *bundletest.Board) {
			b.Bookmarks = append(b.Bookmarks, b.Bookmarks[0])
		}, "bookmarks.jsonl:2: user_id: the pair (user_id 3, topic_id 2) appears already, on line 1"},
		{"forum mark for a missing forum", func(b *bundletest.Board) {
			b.ForumMarks[0].ForumID = 99
		}, "forum_marks.jsonl:1: forum_id: no row in forums.jsonl has id 99"},
		{"ban of a missing user", func(b *bundletest.Board) {
			b.Bans[0].UserID = p(int64(99))
		}, "bans.jsonl:1: user_id: no row in users.jsonl has id 99"},
		{"smiley code twice", func(b *bundletest.Board) {
			s := b.Smilies[0]
			s.Position = 9
			b.Smilies = append(b.Smilies, s)
		}, "smilies.jsonl:3: code: code \":)\" appears already, on line 1"},
		{"route to a missing static page", func(b *bundletest.Board) {
			b.LegacyRoutes[2].TargetSlug = p("nope")
		}, "legacy_routes.jsonl:3: target_slug: no row in static_pages.jsonl has slug nope"},
		{"fixed route target that does not exist", func(b *bundletest.Board) {
			b.LegacyRoutes[1].TargetID = p(int64(99))
		}, "legacy_routes.jsonl:2: target_id: no row in users.jsonl or gone_users.jsonl has id 99"},
		{"avatar route to a gone user", func(b *bundletest.Board) {
			b.LegacyRoutes = append(b.LegacyRoutes, bundle.LegacyRoute{
				Path: "old/avatar.cgi", Match: map[string]string{}, TargetKind: "avatar",
				TargetID: p(int64(8)), Position: 4,
			})
		}, "legacy_routes.jsonl:4: target_id: no row in users.jsonl has id 8"},
		{"topic route to a missing topic", func(b *bundletest.Board) {
			b.LegacyRoutes = append(b.LegacyRoutes, bundle.LegacyRoute{
				Path: "old/t.cgi", Match: map[string]string{}, TargetKind: "topic",
				TargetID: p(int64(99)), Position: 4,
			})
		}, "legacy_routes.jsonl:4: target_id: no row in topics.jsonl has id 99"},
		{"route twice", func(b *bundletest.Board) {
			r := b.LegacyRoutes[0]
			r.Position = 9
			b.LegacyRoutes = append(b.LegacyRoutes, r)
		}, "legacy_routes.jsonl:4: path: the triple (path, match, id_param) appears already, on line 1"},
		{"legacy file prefix twice", func(b *bundletest.Board) {
			b.LegacyFiles = append(b.LegacyFiles, bundle.LegacyFile{Prefix: "old/images/smilies/", Dir: "ranks"})
		}, "legacy_files.jsonl:2: prefix: prefix \"old/images/smilies/\" appears already, on line 1"},
		{"static page twice", func(b *bundletest.Board) {
			b.StaticPages = append(b.StaticPages, b.StaticPages[0])
		}, "static_pages.jsonl:3: slug: slug \"about\" appears already, on line 1"},
		{"forum role on the board", func(b *bundletest.Board) {
			b.Grants = append(b.Grants, bundle.Grant{GroupID: p(int64(2)), ForumID: 0, RoleID: p(int64(1))})
		}, "grants.jsonl:19: forum_id: role 1 is a forum role; set forum_id to a forum"},
		{"board role on a forum", func(b *bundletest.Board) {
			b.Grants = append(b.Grants, bundle.Grant{GroupID: p(int64(2)), ForumID: 2, RoleID: p(int64(2))})
		}, "grants.jsonl:19: forum_id: role 2 is a board role; set forum_id to 0"},
		{"mod role fits a forum and the board", func(b *bundletest.Board) {
			b.Roles = append(b.Roles, bundle.Role{ID: 3, Name: "Tools", Kind: "mod", Settings: []bundle.RoleSetting{{Option: "mod.edit", Setting: bundle.Yes}}})
			b.Grants = append(b.Grants,
				bundle.Grant{GroupID: p(int64(3)), ForumID: 0, RoleID: p(int64(3))},
				bundle.Grant{GroupID: p(int64(3)), ForumID: 2, RoleID: p(int64(3))})
		}, ""},
		{"grant to a missing role", func(b *bundletest.Board) {
			b.Grants = append(b.Grants, bundle.Grant{GroupID: p(int64(2)), ForumID: 2, RoleID: p(int64(9))})
		}, "grants.jsonl:19: role_id: no row in roles.jsonl has id 9"},
		{"grant to a missing group", func(b *bundletest.Board) {
			b.Grants[0].GroupID = p(int64(99))
		}, "grants.jsonl:1: group_id: no row in groups.jsonl has id 99"},
		{"grant to a missing user", func(b *bundletest.Board) {
			b.Grants[16].UserID = p(int64(99))
		}, "grants.jsonl:17: user_id: no row in users.jsonl has id 99"},
		{"grant on a missing forum", func(b *bundletest.Board) {
			b.Grants[0].ForumID = 99
		}, "grants.jsonl:1: forum_id: no row in forums.jsonl has id 99"},
		{"file whose SHA-256 differs", func(b *bundletest.Board) {
			b.Users[0].Avatar.SHA256 = zeros
		}, "users.jsonl:1: avatar.sha256: is " + zeros + "; the SHA-256 of files/avatars/1.png is "},
		{"file whose size differs", func(b *bundletest.Board) {
			b.Users[0].Avatar.Size++
		}, "users.jsonl:1: avatar.size: is 71; files/avatars/1.png has 70 bytes"},
		{"file that is missing", func(b *bundletest.Board) {
			delete(b.Files, "avatars/1.png")
		}, "users.jsonl:1: avatar.path: files/avatars/1.png does not exist"},
		{"file that no row names", func(b *bundletest.Board) {
			b.Files["attachments/stray.png"] = []byte("x")
		}, "files/attachments/stray.png: no row names this file"},
		{"two rows name one path with different sizes", func(b *bundletest.Board) {
			u := b.Users[1]
			u.ID, u.Username = 5, "eve"
			a := *b.Users[0].Avatar
			a.Size++
			u.Avatar = &a
			b.Users = append(b.Users, u)
		}, "users.jsonl:5: avatar.size: is 71; users.jsonl:1 names files/avatars/1.png with size 70"},
		{"two rows name one path with different hashes", func(b *bundletest.Board) {
			u := b.Users[1]
			u.ID, u.Username = 5, "eve"
			a := *b.Users[0].Avatar
			a.SHA256 = zeros
			u.Avatar = &a
			b.Users = append(b.Users, u)
		}, "users.jsonl:5: avatar.sha256: is " + zeros + "; users.jsonl:1 names files/avatars/1.png with SHA-256 "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := bundletest.Small()
			tc.mutate(b)
			err := checkBoard(t, b)
			if tc.want == "" {
				if err != nil {
					t.Fatalf("Check = %v, want nil", err)
				}
				return
			}
			var errs bundle.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("Check = %v (%T), want bundle.Errors", err, err)
			}
			if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), tc.want) {
				t.Fatalf("Check = %v\nwant one error that starts with %q", err, tc.want)
			}
		})
	}
}

// One old path can carry a topic ID in one parameter and a post ID in another.
// The two routes differ only by id_param, so both are allowed.
func TestCheckRoutesDifferByIDParam(t *testing.T) {
	b := bundletest.Small()
	r := b.LegacyRoutes[0]
	r.IDParam, r.TargetKind, r.OffsetParam, r.Position = p("post"), "post", nil, 9
	b.LegacyRoutes = append(b.LegacyRoutes, r)
	if err := checkBoard(t, b); err != nil {
		t.Fatalf("Check = %v; want nil", err)
	}
}

func TestCheckUsernameCollision(t *testing.T) {
	b := bundletest.Small()
	b.Users[1].Username = "robert"
	u := b.Users[2]
	first, second := u, u
	first.ID, first.Username = 7, "Bob"
	second.ID, second.Username = 9, "ｂｏｂ"
	b.Users = append(b.Users, first, second)
	err := checkBoard(t, b)
	var errs bundle.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Check = %v, want bundle.Errors", err)
	}
	want := `users.jsonl:6: username: user 9 and user 7 have the same cleaned username "bob"`
	if len(errs) != 1 || errs[0].Error() != want {
		t.Fatalf("Check = %v\nwant one error: %s", err, want)
	}
}

func TestCheckWithoutFilesDirectory(t *testing.T) {
	// No row names a file: a bundle with no files/ is valid.
	b := bundletest.Small()
	b.Settings.HeaderImage = nil
	b.Ranks[0].Image = nil
	b.Users[0].Avatar = nil
	b.Attachments = nil
	b.Smilies = nil
	b.Files = map[string][]byte{}
	dir := bundletest.Write(t, b)
	if err := os.RemoveAll(filepath.Join(dir, "files")); err != nil {
		t.Fatal(err)
	}
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if err := bundle.Check(r); err != nil {
		t.Fatalf("Check = %v, want nil", err)
	}

	// Rows name files: each gets the normal "does not exist" error.
	dir = bundletest.Write(t, bundletest.Small())
	if err := os.RemoveAll(filepath.Join(dir, "files")); err != nil {
		t.Fatal(err)
	}
	r, err = bundle.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	var errs bundle.Errors
	if err := bundle.Check(r); !errors.As(err, &errs) {
		t.Fatalf("Check = %v, want bundle.Errors", err)
	}
	want := []string{
		"settings.json: header_image.path: files/header/banner.png does not exist",
		"ranks.jsonl:1: image.path: files/ranks/newcomer.png does not exist",
		"users.jsonl:1: avatar.path: files/avatars/1.png does not exist",
		"attachments.jsonl:1: file.path: files/attachments/1.png does not exist",
		"attachments.jsonl:2: file.path: files/attachments/2.gif does not exist",
		"smilies.jsonl:1: image.path: files/smilies/smile.gif does not exist",
		"smilies.jsonl:2: image.path: files/smilies/grin.png does not exist",
	}
	if len(errs) != len(want) {
		t.Fatalf("Check = %v\nwant %d errors", errs, len(want))
	}
	for i, e := range errs {
		if e.Error() != want[i] {
			t.Errorf("error %d = %q, want %q", i, e, want[i])
		}
	}
}

func TestCheckNamedDirectory(t *testing.T) {
	dir := bundletest.Write(t, bundletest.Small())
	avatar := filepath.Join(dir, "files", "avatars", "1.png")
	if err := os.Remove(avatar); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(avatar, 0o755); err != nil {
		t.Fatal(err)
	}
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := "users.jsonl:1: avatar.path: files/avatars/1.png is not a regular file"
	var errs bundle.Errors
	if err := bundle.Check(r); !errors.As(err, &errs) || len(errs) != 1 || errs[0].Error() != want {
		t.Fatalf("Check = %v, want one error: %s", err, want)
	}
}

func TestCheckDeepUnnamedFile(t *testing.T) {
	b := bundletest.Small()
	b.Files["attachments/a/b/stray.png"] = []byte("x")
	want := "files/attachments/a/b/stray.png: no row names this file"
	var errs bundle.Errors
	if err := checkBoard(t, b); !errors.As(err, &errs) || len(errs) != 1 || errs[0].Error() != want {
		t.Fatalf("Check = %v, want one error: %s", err, want)
	}
}

func TestCheckLongCycleMessage(t *testing.T) {
	b := bundletest.Small()
	const first, n = 4, 30
	for i := range n {
		id := int64(first + i)
		next := int64(first + (i+1)%n)
		b.Forums = append(b.Forums, bundle.Forum{ID: id, ParentID: &next, Kind: "category", Name: "loop"})
	}
	var ids []string
	for i := range 20 {
		ids = append(ids, strconv.Itoa(first+i))
	}
	want := "forums.jsonl:4: parent_id: the parent chain loops: " + strings.Join(ids, ", ") + ", …"
	var errs bundle.Errors
	if err := checkBoard(t, b); !errors.As(err, &errs) || len(errs) != 1 || errs[0].Error() != want {
		t.Fatalf("Check = %v, want one error: %s", err, want)
	}
}

func TestCheckCollectsManyErrors(t *testing.T) {
	b := bundletest.Small()
	for i := range 150 {
		po := b.Posts[0]
		po.ID, po.TopicID = int64(100+i), 99
		b.Posts = append(b.Posts, po)
	}
	err := checkBoard(t, b)
	var errs bundle.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Check = %v, want bundle.Errors", err)
	}
	if len(errs) != 100 {
		t.Fatalf("got %d errors, want 100", len(errs))
	}
	for i, e := range errs {
		if e.File != "posts.jsonl" || e.Line != 7+i {
			t.Fatalf("error %d is %v; want posts.jsonl:%d", i, e, 7+i)
		}
	}
	if n := strings.Count(err.Error(), "\n"); n != 99 {
		t.Fatalf("Error() has %d line breaks, want 99", n)
	}
}

func TestCheckErrorsAreInFileOrder(t *testing.T) {
	b := bundletest.Small()
	b.LegacyRoutes[2].TargetSlug = p("nope")
	b.Topics = append(b.Topics, bundle.Topic{ID: 4, ForumID: 2, Title: "t", AuthorName: "x", CreatedAt: b.Topics[0].CreatedAt, Kind: "normal", Visibility: "approved"})
	b.Users[0].Avatar.SHA256 = strings.Repeat("0", 64)
	b.Posts[0].EditedBy = p(int64(99))
	err := checkBoard(t, b)
	var errs bundle.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Check = %v, want bundle.Errors", err)
	}
	var files []string
	for _, e := range errs {
		files = append(files, e.File)
	}
	want := []string{"users.jsonl", "topics.jsonl", "posts.jsonl", "legacy_routes.jsonl"}
	if strings.Join(files, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v\n%v", files, want, err)
	}
}

func TestCheckStopsAtAFailingRow(t *testing.T) {
	dir := bundletest.Write(t, bundletest.Small())
	f, err := os.OpenFile(filepath.Join(dir, "posts.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("{\"id\":\n"); err != nil {
		t.Fatal(err)
	}
	f.Close()
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	var errs bundle.Errors
	if err := bundle.Check(r); !errors.As(err, &errs) || len(errs) != 1 || errs[0].File != "posts.jsonl" || errs[0].Line != 7 {
		t.Fatalf("Check = %v, want one error at posts.jsonl:7", err)
	}
}

// TestCheckPMWithNobodyToSeeIt writes a message that has no author ID and no
// recipient. The writer refuses such a row, so the test edits the file.
func TestCheckPMWithNobodyToSeeIt(t *testing.T) {
	b := bundletest.Small()
	b.PMs[0].AuthorID, b.PMs[0].AuthorName = nil, "ghost"
	dir := bundletest.Write(t, b)
	path := filepath.Join(dir, "pms.jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	edited := regexp.MustCompile(`"recipients":\[[^\]]*\]`).ReplaceAll(data, []byte(`"recipients":[]`))
	if bytes.Equal(edited, data) {
		t.Fatal("the edit changed nothing")
	}
	if err := os.WriteFile(path, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	r, err := bundle.Open(dir)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	err = bundle.Check(r)
	var errs bundle.Errors
	if !errors.As(err, &errs) {
		t.Fatalf("Check = %v (%T), want bundle.Errors", err, err)
	}
	want := "pms.jsonl:1: recipients: "
	if len(errs) != 1 || !strings.HasPrefix(errs[0].Error(), want) {
		t.Fatalf("Check = %v\nwant one error that starts with %q", err, want)
	}
}
