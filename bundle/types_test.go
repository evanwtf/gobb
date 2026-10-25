package bundle

import (
	"encoding/json"
	"math"
	"reflect"
	"strings"
	"testing"
	"time"
)

var testTime = time.Date(2020, 3, 4, 5, 6, 7, 0, time.UTC)

const testHash = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func ptr[T any](v T) *T { return &v }

func ref(dir string) FileRef {
	return FileRef{Path: dir + "/1.png", Size: 12, SHA256: testHash}
}

func body() Body { return Body{Source: "Hello", Markdown: true, Smilies: true, Autolink: true} }

// fullRows returns one row per data file with every pointer set.
func fullRows() []Row {
	return []Row{
		Rank{ID: 1, Title: "Regular", MinPosts: 10, Special: true, Image: ptr(ref("ranks"))},
		User{
			ID: 1, Username: "alice", Email: "alice@example.com",
			Password: Password{Scheme: "legacy", Hash: ""}, State: "active", Owner: true,
			EmailVerified: true, RegisteredAt: testTime, LastVisitAt: &testTime,
			LastMarkAt: &testTime, PostCount: 3, RankID: ptr(int64(1)),
			Avatar: ptr(ref("avatars")), Signature: body(), Timezone: "America/New_York",
			ColorScheme: "slate", DateFormat: "iso", Location: "Lake Placid, NY",
			Occupation: "Pilot", Interests: "Gliders", Website: "https://example.com/alice",
			BirthDay: ptr(14), BirthMonth: ptr(3), BirthYear: ptr(1984),
		},
		GoneUser{ID: 9},
		Group{ID: 1, Name: "Guests", Builtin: ptr("guests"), Kind: "builtin", Description: "d"},
		GroupMember{GroupID: 1, UserID: 1, Leader: true, Pending: true},
		Role{
			ID: 1, Name: "Reader", Kind: "forum", Description: "d", Position: 1,
			Settings: []RoleSetting{{Option: "forum.read", Setting: Yes}},
		},
		Grant{
			GroupID: ptr(int64(1)), UserID: ptr(int64(2)), ForumID: 3, RoleID: ptr(int64(4)),
			Option: ptr("forum.read"), Setting: ptr(Never),
		},
		Forum{
			ID: 2, ParentID: ptr(int64(1)), Kind: "link", Name: "Out", Description: "d",
			Rules: "r", LinkURL: ptr("https://example.com/"), Locked: true, Position: 2,
		},
		Topic{
			ID: 1, ForumID: 2, Title: "t", AuthorID: ptr(int64(1)), AuthorName: "alice",
			CreatedAt: testTime, ViewCount: 5, Kind: "sticky", Locked: true,
			Visibility: "approved", MovedTo: ptr(int64(2)),
		},
		Post{
			ID: 1, TopicID: 1, AuthorID: ptr(int64(1)), AuthorName: "alice",
			AuthorIP: ptr("10.0.0.1"), CreatedAt: testTime, Subject: "s", Body: body(),
			ShowSignature: true, Visibility: "approved", EditCount: 1, EditedAt: &testTime,
			EditedBy: ptr(int64(1)), EditReason: "typo",
		},
		Poll{
			TopicID: 1, Question: "q", MaxOptions: 1, EndsAt: &testTime, CanChangeVote: true,
			Options: []PollOption{{ID: 1, Text: "a", Votes: 2}},
		},
		PollVote{TopicID: 1, OptionID: 1, UserID: 1, VoterIP: ptr("10.0.0.2")},
		Attachment{
			ID: 1, PostID: ptr(int64(1)), Position: 0, UploaderID: ptr(int64(1)), Name: "a.png",
			File: ref("attachments"), Mime: "image/png", Width: ptr(10), Height: ptr(20),
			DownloadCount: 1, Comment: "c", CreatedAt: testTime,
		},
		PM{
			ID: 1, AuthorID: ptr(int64(1)), AuthorName: "alice", AuthorIP: ptr("10.0.0.3"),
			CreatedAt: testTime, Subject: "s",
			Body: body(), ShowSignature: true, AuthorDeleted: true,
			Recipients: []Recipient{{UserID: 2, Kind: "to", Read: true, Deleted: true}},
		},
		TopicMark{UserID: 1, TopicID: 1, MarkedAt: testTime},
		ForumMark{UserID: 1, ForumID: 2, MarkedAt: testTime},
		Ban{
			Kind: "user", UserID: ptr(int64(1)), Email: ptr("a@example.com"),
			IP: ptr("10.0.0.0/8"), Reason: "r", ReasonShown: "s", CreatedAt: testTime,
			EndsAt: &testTime,
		},
		Smiley{Code: ":)", Image: ref("smilies"), Description: "smile", Position: 1},
		LegacyRoute{
			Path: "old/main.cgi", Match: map[string]string{"view": "full"}, IDParam: ptr("id"),
			TargetKind: "forum", TargetID: ptr(int64(1)), TargetSlug: ptr("about"),
			OffsetParam: ptr("page"), Position: 1,
		},
		LegacyFile{Prefix: "img/emoticons/", Dir: "smilies"},
		StaticPage{Slug: "about", Title: "About", Body: body(), UpdatedAt: testTime},
	}
}

// validRows returns one row per data file that passes check.
func validRows() []Row {
	return []Row{
		Rank{ID: 1, Title: "Regular", MinPosts: 10, Image: ptr(ref("ranks"))},
		validUser(),
		GoneUser{ID: 9},
		validGroup(),
		GroupMember{GroupID: 1, UserID: 1},
		validRole(),
		validGrant(),
		validForum(),
		validTopic(),
		validPost(),
		validPoll(),
		PollVote{TopicID: 1, OptionID: 1, UserID: 1, VoterIP: ptr("2001:db8::1")},
		validAttachment(),
		validPM(),
		TopicMark{UserID: 1, TopicID: 1, MarkedAt: testTime},
		ForumMark{UserID: 1, ForumID: 2, MarkedAt: testTime},
		validBan(),
		validSmiley(),
		validRoute(),
		LegacyFile{Prefix: "img/emoticons/", Dir: "smilies"},
		validPage(),
	}
}

func validUser() User {
	return User{
		ID: 1, Username: "alice", Email: "", State: "active", RegisteredAt: testTime,
		Password: Password{Scheme: "legacy", Hash: ""}, Signature: body(),
		Timezone: "America/New_York", ColorScheme: "slate-deep", DateFormat: "standard",
	}
}

func validGroup() Group { return Group{ID: 1, Name: "Staff", Kind: "open"} }

func validRole() Role {
	return Role{
		ID: 1, Name: "Reader", Kind: "forum", Position: 1,
		Settings: []RoleSetting{{Option: "forum.read", Setting: Yes}},
	}
}

func validGrant() Grant {
	return Grant{GroupID: ptr(int64(1)), ForumID: 2, Option: ptr("forum.read"), Setting: ptr(Yes)}
}

func validForum() Forum { return Forum{ID: 2, Kind: "forum", Name: "General"} }

func validTopic() Topic {
	return Topic{
		ID: 1, ForumID: 2, Title: "t", AuthorName: "alice", CreatedAt: testTime,
		Kind: "normal", Visibility: "approved",
	}
}

func validPost() Post {
	return Post{
		ID: 1, TopicID: 1, AuthorName: "alice", CreatedAt: testTime, Body: body(),
		Visibility: "approved",
	}
}

func validPoll() Poll {
	return Poll{
		TopicID: 1, Question: "q", MaxOptions: 1,
		Options: []PollOption{{ID: 1, Text: "a"}, {ID: 2, Text: "b"}},
	}
}

func validAttachment() Attachment {
	return Attachment{
		ID: 1, UploaderID: ptr(int64(1)), Name: "a.png", File: ref("attachments"), Mime: "image/png",
		CreatedAt: testTime,
	}
}

func validPM() PM {
	return PM{
		ID: 1, AuthorID: ptr(int64(1)), AuthorName: "alice", CreatedAt: testTime,
		Subject: "s", Body: body(),
		Recipients: []Recipient{{UserID: 2, Kind: "to"}, {UserID: 3, Kind: "bcc"}},
	}
}

func validBan() Ban {
	return Ban{Kind: "ip", IP: ptr("10.0.0.0/8"), CreatedAt: testTime}
}

func validSmiley() Smiley {
	return Smiley{Code: ":)", Image: ref("smilies"), Description: "smile"}
}

func validRoute() LegacyRoute {
	return LegacyRoute{
		Path: "old/thread.cgi", Match: map[string]string{}, IDParam: ptr("id"),
		TargetKind: "topic", OffsetParam: ptr("page"),
	}
}

func validPage() StaticPage {
	return StaticPage{Slug: "about", Title: "About", Body: body(), UpdatedAt: testTime}
}

func TestRowFiles(t *testing.T) {
	if len(Files) != 21 {
		t.Fatalf("len(Files) = %d, want 21", len(Files))
	}
	seen := map[string]bool{}
	for _, f := range Files {
		if seen[f] {
			t.Errorf("duplicate file name %q", f)
		}
		seen[f] = true
	}
	rows := fullRows()
	if len(rows) != len(Files) {
		t.Fatalf("%d row types, want %d", len(rows), len(Files))
	}
	for i, r := range rows {
		if got := r.DataFile(); got != Files[i] {
			t.Errorf("%T.DataFile() = %q, want %q (load order)", r, got, Files[i])
		}
	}
}

func TestRowJSONRoundTrip(t *testing.T) {
	for _, r := range fullRows() {
		t.Run(r.DataFile(), func(t *testing.T) {
			data, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			got := reflect.New(reflect.TypeOf(r))
			if err := json.Unmarshal(data, got.Interface()); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Elem().Interface(), r) {
				t.Errorf("round trip changed the row\n got  %+v\n want %+v", got.Elem().Interface(), r)
			}
		})
	}
}

func TestNilPointersMarshalAsNull(t *testing.T) {
	for _, r := range fullRows() {
		t.Run(r.DataFile(), func(t *testing.T) {
			zero := reflect.Zero(reflect.TypeOf(r)).Interface()
			data, err := json.Marshal(zero)
			if err != nil {
				t.Fatal(err)
			}
			var m map[string]json.RawMessage
			if err := json.Unmarshal(data, &m); err != nil {
				t.Fatal(err)
			}
			rt := reflect.TypeOf(r)
			for i := range rt.NumField() {
				f := rt.Field(i)
				name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
				if strings.Contains(f.Tag.Get("json"), "omitempty") {
					t.Errorf("%s has omitempty", f.Name)
				}
				raw, ok := m[name]
				if !ok {
					t.Errorf("field %s (%q) is missing from the JSON", f.Name, name)
					continue
				}
				if f.Type.Kind() == reflect.Pointer {
					if string(raw) != "null" {
						t.Errorf("%s marshals as %s, want null", name, raw)
					}
				}
			}
		})
	}
}

func TestErrorString(t *testing.T) {
	tests := []struct {
		e    Error
		want string
	}{
		{Error{File: "posts.jsonl", Line: 12, Field: "body.source", Msg: "too long"}, "posts.jsonl:12: body.source: too long"},
		{Error{File: "posts.jsonl", Field: "body.source", Msg: "too long"}, "posts.jsonl: body.source: too long"},
		{Error{File: "posts.jsonl", Line: 3, Msg: "bad line"}, "posts.jsonl:3: bad line"},
		{Error{File: "posts.jsonl", Msg: "3 rows, manifest says 4"}, "posts.jsonl: 3 rows, manifest says 4"},
		{Error{Field: "title", Msg: "empty"}, "title: empty"},
		{Error{Line: 7, Field: "title", Msg: "empty"}, "line 7: title: empty"},
		{Error{Line: 7, Msg: "bad line"}, "line 7: bad line"},
	}
	for _, tt := range tests {
		if got := tt.e.Error(); got != tt.want {
			t.Errorf("got %q, want %q", got, tt.want)
		}
	}
}

func checkRow(t *testing.T, r Row) []*Error {
	t.Helper()
	c, ok := r.(checker)
	if !ok {
		t.Fatalf("%T has no check method", r)
	}
	return c.check()
}

func TestCheckFieldsAcceptsValid(t *testing.T) {
	for _, r := range validRows() {
		t.Run(r.DataFile(), func(t *testing.T) {
			for _, e := range checkRow(t, r) {
				t.Errorf("unexpected error: %v", e)
			}
		})
	}
	// Both empty and non-empty forms of the nullable file shapes are fine.
	for _, m := range []Manifest{validManifest()} {
		for _, e := range m.check() {
			t.Errorf("manifest: %v", e)
		}
	}
	for _, e := range validSettings().check() {
		t.Errorf("settings: %v", e)
	}
}

func validManifest() Manifest {
	m := Manifest{Format: Format, Version: Version, CreatedAt: testTime, Source: "example", Counts: map[string]int{}}
	for _, f := range Files {
		m.Counts[f] = 0
	}
	return m
}

func validSettings() Settings {
	return Settings{
		BoardName: "Example", BoardDescription: "A board", Timezone: "Europe/Paris",
		DefaultColorScheme: "slate", Registration: "open",
	}
}

func TestCheckFields(t *testing.T) {
	long := strings.Repeat("x", MaxSourceBytes+1)
	mod := func(f func(*Post)) Post { p := validPost(); f(&p); return p }

	tests := []struct {
		name  string
		row   Row
		field string
	}{
		{"username only spaces", func() Row { u := validUser(); u.Username = "   "; return u }(), "username"},
		{"username NBSP only", func() Row { u := validUser(); u.Username = "\u00a0"; return u }(), "username"},
		{"username U+3000 only", func() Row { u := validUser(); u.Username = "\u3000\u3000"; return u }(), "username"},
		{"username trailing newline", func() Row { u := validUser(); u.Username = "alice\n"; return u }(), "username"},
		{"group name trailing newline", func() Row { g := validGroup(); g.Name = "alice\n"; return g }(), "name"},
		{"username leading tab", func() Row { u := validUser(); u.Username = "\talice"; return u }(), "username"},
		{"group name leading tab", func() Row { g := validGroup(); g.Name = "\talice"; return g }(), "name"},
		{"username trailing CRLF", func() Row { u := validUser(); u.Username = "alice\r\n"; return u }(), "username"},
		{"group name trailing CRLF", func() Row { g := validGroup(); g.Name = "alice\r\n"; return g }(), "name"},
		{"username trailing U+0085", func() Row { u := validUser(); u.Username = "alice\u0085"; return u }(), "username"},
		{"group name trailing U+0085", func() Row { g := validGroup(); g.Name = "alice\u0085"; return g }(), "name"},
		{"username newline only is a control character", func() Row { u := validUser(); u.Username = "\n"; return u }(), "username"},
		{"username tab only", func() Row { u := validUser(); u.Username = "\t "; return u }(), "username"},
		{"group name NBSP only", func() Row { g := validGroup(); g.Name = "\u00a0"; return g }(), "name"},
		{"role name U+3000 only", func() Row { r := validRole(); r.Name = "\u3000"; return r }(), "name"},
		{"forum name newline only", func() Row { f := validForum(); f.Name = "\n"; return f }(), "name"},
		{"page title NBSP only", func() Row { p := validPage(); p.Title = "\u00a0"; return p }(), "title"},
		{"username control", func() Row { u := validUser(); u.Username = "a\x01b"; return u }(), "username"},
		{"username too long", func() Row { u := validUser(); u.Username = strings.Repeat("a", 256); return u }(), "username"},
		{"password scheme", func() Row { u := validUser(); u.Password.Scheme = "md5"; return u }(), "password.scheme"},
		{"gobb hash is bcrypt", func() Row {
			u := validUser()
			u.Password = Password{Scheme: "gobb", Hash: "$2y$10$abcdefghijklmnopqrstuuabcdefghijklmnopqrstuvwxyz01234"}
			return u
		}(), "password.hash"},
		{"legacy hash unknown form", func() Row { u := validUser(); u.Password.Hash = "md5:abc"; return u }(), "password.hash"},
		{"user state", func() Row { u := validUser(); u.State = "banned"; return u }(), "state"},
		{"user timezone", func() Row { u := validUser(); u.Timezone = "Mars/Base"; return u }(), "timezone"},
		{"user timezone long", func() Row { u := validUser(); u.Timezone = strings.Repeat("a", 65); return u }(), "timezone"},
		{"user timezone empty", func() Row { u := validUser(); u.Timezone = ""; return u }(), "timezone"},
		{"user timezone Local", func() Row { u := validUser(); u.Timezone = "Local"; return u }(), "timezone"},
		{"user timezone UTC", func() Row { u := validUser(); u.Timezone = "UTC"; return u }(), "timezone"},
		{"user timezone offset", func() Row { u := validUser(); u.Timezone = "Etc/GMT+4"; return u }(), "timezone"},
		{"user date format", func() Row { u := validUser(); u.DateFormat = "custom"; return u }(), "date_format"},
		{"user date format empty", func() Row { u := validUser(); u.DateFormat = ""; return u }(), "date_format"},
		{"user location", func() Row { u := validUser(); u.Location = strings.Repeat("l", 101); return u }(), "location"},
		{"user occupation", func() Row { u := validUser(); u.Occupation = "a\nb"; return u }(), "occupation"},
		{"user interests", func() Row { u := validUser(); u.Interests = strings.Repeat("i", 201); return u }(), "interests"},
		{"user website", func() Row { u := validUser(); u.Website = "ftp://x.org"; return u }(), "website"},
		{"user website long", func() Row { u := validUser(); u.Website = "https://example.com/" + strings.Repeat("a", 200); return u }(), "website"},
		{"user birth day alone", func() Row { u := validUser(); u.BirthDay = ptr(5); return u }(), "birth_day"},
		{"user birth month alone", func() Row { u := validUser(); u.BirthMonth = ptr(5); return u }(), "birth_day"},
		{"user birth year alone", func() Row { u := validUser(); u.BirthYear = ptr(1990); return u }(), "birth_year"},
		{"user birth day 0", func() Row { u := validUser(); u.BirthDay, u.BirthMonth = ptr(0), ptr(5); return u }(), "birth_day"},
		{"user birth month 13", func() Row { u := validUser(); u.BirthDay, u.BirthMonth = ptr(5), ptr(13); return u }(), "birth_month"},
		{"user birth day 31 April", func() Row { u := validUser(); u.BirthDay, u.BirthMonth = ptr(31), ptr(4); return u }(), "birth_day"},
		{"user birth year 0", func() Row { u := validUser(); u.BirthDay, u.BirthMonth, u.BirthYear = ptr(5), ptr(5), ptr(0); return u }(), "birth_year"},
		{"user birth year 1899", func() Row {
			u := validUser()
			u.BirthDay, u.BirthMonth, u.BirthYear = ptr(5), ptr(5), ptr(1899)
			return u
		}(), "birth_year"},
		{"user color", func() Row { u := validUser(); u.ColorScheme = "neon"; return u }(), "color_scheme"},
		{"user avatar dir", func() Row { u := validUser(); u.Avatar = ptr(ref("ranks")); return u }(), "avatar.path"},
		{"user id zero", func() Row { u := validUser(); u.ID = 0; return u }(), "id"},
		{"user negative posts", func() Row { u := validUser(); u.PostCount = -1; return u }(), "post_count"},
		{"user rank id zero", func() Row { u := validUser(); u.RankID = ptr(int64(0)); return u }(), "rank_id"},
		{"user signature long", func() Row { u := validUser(); u.Signature.Source = long; return u }(), "signature.source"},
		{"user email control", func() Row { u := validUser(); u.Email = "a\n@example.com"; return u }(), "email"},
		{"group builtin kind no builtin", func() Row { g := validGroup(); g.Kind = "builtin"; return g }(), "kind"},
		{"group builtin set, kind open", func() Row { g := validGroup(); g.Builtin = ptr("guests"); return g }(), "kind"},
		{"group builtin unknown", func() Row { g := validGroup(); g.Kind = "builtin"; g.Builtin = ptr("staff"); return g }(), "builtin"},
		{"group kind unknown", func() Row { g := validGroup(); g.Kind = "secret"; return g }(), "kind"},
		{"group name empty", func() Row { g := validGroup(); g.Name = ""; return g }(), "name"},
		{"group member id", GroupMember{GroupID: 0, UserID: 1}, "group_id"},
		{"role forum holds admin", func() Row {
			r := validRole()
			r.Settings = []RoleSetting{{Option: "admin", Setting: Yes}}
			return r
		}(), "settings[0].option"},
		{"role forum holds mod", func() Row {
			r := validRole()
			r.Settings = []RoleSetting{{Option: "forum.read", Setting: Yes}, {Option: "mod.edit", Setting: Yes}}
			return r
		}(), "settings[1].option"},
		{"role mod holds forum option", func() Row {
			r := validRole()
			r.Kind = "mod"
			return r
		}(), "settings[0].option"},
		{"role board holds forum option", func() Row {
			r := validRole()
			r.Kind = "board"
			return r
		}(), "settings[0].option"},
		{"role unknown option", func() Row {
			r := validRole()
			r.Settings = []RoleSetting{{Option: "nope", Setting: Yes}}
			return r
		}(), "settings[0].option"},
		{"role repeated option", func() Row {
			r := validRole()
			r.Settings = append(r.Settings, r.Settings[0])
			return r
		}(), "settings[1].option"},
		{"role bad setting", func() Row {
			r := validRole()
			r.Settings[0].Setting = "maybe"
			return r
		}(), "settings[0].setting"},
		{"role kind", func() Row { r := validRole(); r.Kind = "global"; return r }(), "kind"},
		{"grant board-wide forum option", func() Row {
			g := validGrant()
			g.ForumID = 0
			return g
		}(), "option"},
		{"grant forum board option", func() Row {
			g := validGrant()
			g.Option = ptr("admin")
			return g
		}(), "option"},
		{"grant unknown option", func() Row { g := validGrant(); g.Option = ptr("nope"); return g }(), "option"},
		{"grant group and user", func() Row { g := validGrant(); g.UserID = ptr(int64(2)); return g }(), "group_id"},
		{"grant no group or user", func() Row { g := validGrant(); g.GroupID = nil; return g }(), "group_id"},
		{"grant role and option", func() Row { g := validGrant(); g.RoleID = ptr(int64(3)); return g }(), "role_id"},
		{"grant no role or option", func() Row { g := validGrant(); g.Option, g.Setting = nil, nil; return g }(), "role_id"},
		{"grant option no setting", func() Row { g := validGrant(); g.Setting = nil; return g }(), "setting"},
		{"grant setting no option", func() Row { g := validGrant(); g.Option = nil; return g }(), "option"},
		{"grant role with setting", func() Row {
			g := validGrant()
			g.Option, g.RoleID = nil, ptr(int64(3))
			return g
		}(), "setting"},
		{"grant bad setting", func() Row { g := validGrant(); g.Setting = ptr(Setting("maybe")); return g }(), "setting"},
		{"grant negative forum", func() Row { g := validGrant(); g.ForumID = -1; return g }(), "forum_id"},
		{"forum link javascript", func() Row {
			f := validForum()
			f.Kind, f.LinkURL = "link", ptr("javascript:x")
			return f
		}(), "link_url"},
		{"forum link missing url", func() Row { f := validForum(); f.Kind = "link"; return f }(), "link_url"},
		{"forum url on non-link", func() Row { f := validForum(); f.LinkURL = ptr("https://example.com/"); return f }(), "link_url"},
		{"forum kind", func() Row { f := validForum(); f.Kind = "board"; return f }(), "kind"},
		{"forum link url padded", func() Row {
			f := validForum()
			f.Kind, f.LinkURL = "link", ptr(" https://example.com/")
			return f
		}(), "link_url"},
		{"forum own parent", func() Row { f := validForum(); f.ParentID = ptr(f.ID); return f }(), "parent_id"},
		{"topic kind", func() Row { t := validTopic(); t.Kind = "pinned"; return t }(), "kind"},
		{"topic visibility", func() Row { t := validTopic(); t.Visibility = "hidden"; return t }(), "visibility"},
		{"topic title long", func() Row { t := validTopic(); t.Title = strings.Repeat("t", 256); return t }(), "title"},
		{"topic moved to itself", func() Row { t := validTopic(); t.MovedTo = ptr(t.ID); return t }(), "moved_to"},
		{"topic negative views", func() Row { t := validTopic(); t.ViewCount = -1; return t }(), "view_count"},
		{"post body long", mod(func(p *Post) { p.Body.Source = long }), "body.source"},
		{"post body NUL", mod(func(p *Post) { p.Body.Source = "a\x00b" }), "body.source"},
		{"post body bad UTF-8", mod(func(p *Post) { p.Body.Source = "a\xffb" }), "body.source"},
		{"post ip bad", mod(func(p *Post) { p.AuthorIP = ptr("10.0.0") }), "author_ip"},
		{"post ip zone", mod(func(p *Post) { p.AuthorIP = ptr("fe80::1%eth0") }), "author_ip"},
		{"post subject control", mod(func(p *Post) { p.Subject = "a\tb" }), "subject"},
		{"post edit reason long", mod(func(p *Post) { p.EditReason = strings.Repeat("r", 256) }), "edit_reason"},
		{"post negative edits", mod(func(p *Post) { p.EditCount = -1 }), "edit_count"},
		{"post visibility", mod(func(p *Post) { p.Visibility = "gone" }), "visibility"},
		{"post author name empty is fine but id zero", mod(func(p *Post) { p.AuthorID = ptr(int64(0)) }), "author_id"},
		{"poll no options", func() Row { p := validPoll(); p.Options = nil; return p }(), "options"},
		{"poll 101 options", func() Row {
			p := validPoll()
			p.Options = nil
			for i := 1; i <= 101; i++ {
				p.Options = append(p.Options, PollOption{ID: i, Text: "o"})
			}
			return p
		}(), "options"},
		{"poll max too big", func() Row { p := validPoll(); p.MaxOptions = 3; return p }(), "max_options"},
		{"poll max zero", func() Row { p := validPoll(); p.MaxOptions = 0; return p }(), "max_options"},
		{"poll option id zero", func() Row { p := validPoll(); p.Options[0].ID = 0; return p }(), "options[0].id"},
		{"poll option id repeated", func() Row { p := validPoll(); p.Options[1].ID = 1; return p }(), "options[1].id"},
		{"poll option votes", func() Row { p := validPoll(); p.Options[0].Votes = -1; return p }(), "options[0].votes"},
		{"poll option text", func() Row { p := validPoll(); p.Options[1].Text = "a\nb"; return p }(), "options[1].text"},
		{"poll question long", func() Row { p := validPoll(); p.Question = strings.Repeat("q", 256); return p }(), "question"},
		{"poll vote option zero", PollVote{TopicID: 1, OptionID: 0, UserID: 1}, "option_id"},
		{"poll vote ip", PollVote{TopicID: 1, OptionID: 1, UserID: 1, VoterIP: ptr("nope")}, "voter_ip"},
		{"attachment mime", func() Row { a := validAttachment(); a.Mime = "text/html"; return a }(), "mime"},
		{"attachment path dotdot", func() Row { a := validAttachment(); a.File.Path = "../x.png"; return a }(), "file.path"},
		{"attachment wrong dir", func() Row { a := validAttachment(); a.File.Path = "avatars/1.png"; return a }(), "file.path"},
		{"attachment backslash", func() Row { a := validAttachment(); a.File.Path = "attachments/a\\b.png"; return a }(), "file.path"},
		{"attachment path is dir only", func() Row { a := validAttachment(); a.File.Path = "attachments"; return a }(), "file.path"},
		{"attachment leading slash", func() Row { a := validAttachment(); a.File.Path = "/attachments/1.png"; return a }(), "file.path"},
		{"attachment dot segment", func() Row { a := validAttachment(); a.File.Path = "attachments/./1.png"; return a }(), "file.path"},
		{"attachment long path", func() Row { a := validAttachment(); a.File.Path = "attachments/" + strings.Repeat("a", 244); return a }(), "file.path"},
		{"attachment size", func() Row { a := validAttachment(); a.File.Size = -1; return a }(), "file.size"},
		{"attachment sha upper", func() Row { a := validAttachment(); a.File.SHA256 = strings.ToUpper(testHash); return a }(), "file.sha256"},
		{"attachment sha short", func() Row { a := validAttachment(); a.File.SHA256 = "abc"; return a }(), "file.sha256"},
		{"attachment width", func() Row { a := validAttachment(); a.Width = ptr(0); return a }(), "width"},
		{"attachment height", func() Row { a := validAttachment(); a.Height = ptr(-2); return a }(), "height"},
		{"attachment position", func() Row { a := validAttachment(); a.Position = -1; return a }(), "position"},
		{"attachment downloads", func() Row { a := validAttachment(); a.DownloadCount = -1; return a }(), "download_count"},
		{"pm gone author and no recipients", func() Row {
			p := validPM()
			p.AuthorID, p.AuthorName, p.Recipients = nil, "ghost", nil
			return p
		}(), "recipients"},
		{"pm author id zero", func() Row { p := validPM(); p.AuthorID = ptr(int64(0)); return p }(), "author_id"},
		{"pm author name long", func() Row { p := validPM(); p.AuthorName = strings.Repeat("a", 256); return p }(), "author_name"},
		{"pm author name control", func() Row { p := validPM(); p.AuthorName = "a\nb"; return p }(), "author_name"},
		{"attachment uploader id zero", func() Row { a := validAttachment(); a.UploaderID = ptr(int64(0)); return a }(), "uploader_id"},
		{"username 3 bytes and 300 spaces", func() Row { u := validUser(); u.Username = "bob" + strings.Repeat(" ", 300); return u }(), "username"},
		{"group name 3 bytes and 300 spaces", func() Row { g := validGroup(); g.Name = "bob" + strings.Repeat(" ", 300); return g }(), "name"},
		{"page title 3 bytes and 300 spaces", func() Row { p := validPage(); p.Title = "bob" + strings.Repeat(" ", 300); return p }(), "title"},
		{"pm recipient kind", func() Row { p := validPM(); p.Recipients[0].Kind = "cc"; return p }(), "recipients[0].kind"},
		{"pm recipient twice", func() Row { p := validPM(); p.Recipients[1].UserID = 2; return p }(), "recipients[1].user_id"},
		{"pm recipient id", func() Row { p := validPM(); p.Recipients[0].UserID = 0; return p }(), "recipients[0].user_id"},
		{"pm body long", func() Row { p := validPM(); p.Body.Source = long; return p }(), "body.source"},
		{"pm ip", func() Row { p := validPM(); p.AuthorIP = ptr("x"); return p }(), "author_ip"},
		{"ban ip host bits", func() Row { b := validBan(); b.IP = ptr("10.0.0.1/8"); return b }(), "ip"},
		{"ban ip no prefix", func() Row { b := validBan(); b.IP = ptr("10.0.0.1"); return b }(), "ip"},
		{"ban ip6 host bits", func() Row { b := validBan(); b.IP = ptr("2001:db8::1/32"); return b }(), "ip"},
		{"ban kind", func() Row { b := validBan(); b.Kind = "name"; return b }(), "kind"},
		{"ban ip kind, ip missing", func() Row { b := validBan(); b.IP = nil; return b }(), "ip"},
		{"ban ip kind with email", func() Row { b := validBan(); b.Email = ptr("a@example.com"); return b }(), "email"},
		{"ban ip kind with user", func() Row { b := validBan(); b.UserID = ptr(int64(1)); return b }(), "user_id"},
		{"ban user kind, user missing", func() Row { b := validBan(); b.Kind = "user"; b.IP = nil; return b }(), "user_id"},
		{"ban email kind, email missing", func() Row { b := validBan(); b.Kind = "email"; b.IP = nil; return b }(), "email"},
		{"ban email long", func() Row {
			b := validBan()
			b.Kind, b.IP, b.Email = "email", nil, ptr(strings.Repeat("e", 256))
			return b
		}(), "email"},
		{"ban reason control", func() Row { b := validBan(); b.Reason = "a\x07"; return b }(), "reason"},
		{"smiley code tab", func() Row { s := validSmiley(); s.Code = ":\t)"; return s }(), "code"},
		{"smiley code empty", func() Row { s := validSmiley(); s.Code = ""; return s }(), "code"},
		{"smiley code long", func() Row { s := validSmiley(); s.Code = strings.Repeat(":", 33); return s }(), "code"},
		{"smiley image dir", func() Row { s := validSmiley(); s.Image = ref("ranks"); return s }(), "image.path"},
		{"smiley description", func() Row { s := validSmiley(); s.Description = strings.Repeat("d", 256); return s }(), "description"},
		{"route post both ids", func() Row {
			r := validRoute()
			r.TargetKind, r.TargetID = "post", ptr(int64(4))
			return r
		}(), "id_param"},
		{"route post no id", func() Row { r := validRoute(); r.TargetKind, r.IDParam = "post", nil; return r }(), "id_param"},
		{"route message no id", func() Row {
			r := validRoute()
			r.TargetKind, r.IDParam, r.OffsetParam = "message", nil, nil
			return r
		}(), "id_param"},
		{"route message both ids", func() Row {
			r := validRoute()
			r.TargetKind, r.OffsetParam, r.TargetID = "message", nil, ptr(int64(4))
			return r
		}(), "id_param"},
		{"route messages with id_param", func() Row {
			r := validRoute()
			r.TargetKind, r.OffsetParam = "messages", nil
			return r
		}(), "id_param"},
		{"route messages with target_id", func() Row {
			r := validRoute()
			r.TargetKind, r.IDParam, r.OffsetParam, r.TargetID = "messages", nil, nil, ptr(int64(1))
			return r
		}(), "target_id"},
		{"route offset on message", func() Row {
			r := validRoute()
			r.TargetKind = "message"
			return r
		}(), "offset_param"},
		{"route home with id_param", func() Row {
			r := validRoute()
			r.TargetKind, r.OffsetParam = "home", nil
			return r
		}(), "id_param"},
		{"route home with target_id", func() Row {
			r := validRoute()
			r.TargetKind, r.IDParam, r.OffsetParam, r.TargetID = "home", nil, nil, ptr(int64(1))
			return r
		}(), "target_id"},
		{"route page no slug", func() Row {
			r := validRoute()
			r.TargetKind, r.IDParam, r.OffsetParam = "page", nil, nil
			return r
		}(), "target_slug"},
		{"route page with id", func() Row {
			r := validRoute()
			r.TargetKind, r.OffsetParam, r.TargetSlug = "page", nil, ptr("about")
			return r
		}(), "id_param"},
		{"route slug on topic", func() Row { r := validRoute(); r.TargetSlug = ptr("about"); return r }(), "target_slug"},
		{"route page bad slug", func() Row {
			r := validRoute()
			r.TargetKind, r.IDParam, r.OffsetParam, r.TargetSlug = "page", nil, nil, ptr("Bad Slug")
			return r
		}(), "target_slug"},
		{"route offset on user", func() Row {
			r := validRoute()
			r.TargetKind = "user"
			return r
		}(), "offset_param"},
		{"route offset bad param", func() Row { r := validRoute(); r.OffsetParam = ptr("a-b"); return r }(), "offset_param"},
		{"route id_param bad", func() Row { r := validRoute(); r.IDParam = ptr("a b"); return r }(), "id_param"},
		{"route target id zero", func() Row {
			r := validRoute()
			r.IDParam, r.TargetID = nil, ptr(int64(0))
			return r
		}(), "target_id"},
		{"route kind", func() Row { r := validRoute(); r.TargetKind = "image"; return r }(), "target_kind"},
		{"route path leading slash", func() Row { r := validRoute(); r.Path = "/old/x.cgi"; return r }(), "path"},
		{"route path query", func() Row { r := validRoute(); r.Path = "old/x.cgi?a=1"; return r }(), "path"},
		{"route path fragment", func() Row { r := validRoute(); r.Path = "old/x.cgi#a"; return r }(), "path"},
		{"route path empty", func() Row { r := validRoute(); r.Path = ""; return r }(), "path"},
		{"route path long", func() Row { r := validRoute(); r.Path = strings.Repeat("a", 256); return r }(), "path"},
		{"route match key", func() Row { r := validRoute(); r.Match = map[string]string{"a-b": "x"}; return r }(), "match"},
		{"route match value", func() Row { r := validRoute(); r.Match = map[string]string{"a": "x\ny"}; return r }(), "match.a"},
		{"route match eleven", func() Row {
			r := validRoute()
			r.Match = map[string]string{}
			for _, k := range "abcdefghijk" {
				r.Match[string(k)] = "x"
			}
			return r
		}(), "match"},
		{"legacy file no slash", LegacyFile{Prefix: "img/emoticons", Dir: "smilies"}, "prefix"},
		{"legacy file leading slash", LegacyFile{Prefix: "/img/", Dir: "smilies"}, "prefix"},
		{"legacy file empty", LegacyFile{Prefix: "", Dir: "smilies"}, "prefix"},
		{"legacy file dir", LegacyFile{Prefix: "img/", Dir: "avatars"}, "dir"},
		{"page bad slug", func() Row { p := validPage(); p.Slug = "Bad Slug"; return p }(), "slug"},
		{"page slug starts with dash", func() Row { p := validPage(); p.Slug = "-a"; return p }(), "slug"},
		{"page slug long", func() Row { p := validPage(); p.Slug = strings.Repeat("a", 65); return p }(), "slug"},
		{"page title blank", func() Row { p := validPage(); p.Title = " "; return p }(), "title"},
		{"page body long", func() Row { p := validPage(); p.Body.Source = long; return p }(), "body.source"},
		{"rank title long", Rank{ID: 1, Title: strings.Repeat("t", 256)}, "title"},
		{"rank min posts", Rank{ID: 1, Title: "t", MinPosts: -1}, "min_posts"},
		{"rank image dir", Rank{ID: 1, Title: "t", Image: ptr(ref("avatars"))}, "image.path"},
		{"gone user id", GoneUser{ID: 0}, "id"},
		{"topic mark ids", TopicMark{UserID: 1, TopicID: 0, MarkedAt: testTime}, "topic_id"},
		{"forum mark ids", ForumMark{UserID: 0, ForumID: 1, MarkedAt: testTime}, "user_id"},
		{"ban email kind, empty email", Ban{Kind: "email", Email: ptr(""), CreatedAt: testTime}, "email"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			errs := checkRow(t, tt.row)
			for _, e := range errs {
				if e.File != "" || e.Line != 0 {
					t.Errorf("error sets File or Line: %+v", e)
				}
				if e.Msg == "" {
					t.Errorf("error has no message: %+v", e)
				}
			}
			for _, e := range errs {
				if e.Field == tt.field {
					return
				}
			}
			t.Errorf("no error for field %q; got %v", tt.field, errs)
		})
	}
}

func TestCheckManifestAndSettings(t *testing.T) {
	m := func(f func(*Manifest)) Manifest { x := validManifest(); f(&x); return x }
	s := func(f func(*Settings)) Settings { x := validSettings(); f(&x); return x }
	tests := []struct {
		name  string
		errs  []*Error
		field string
	}{
		{"format", m(func(x *Manifest) { x.Format = "other" }).check(), "format"},
		{"version", m(func(x *Manifest) { x.Version = 3 }).check(), "version"},
		{"version 1", m(func(x *Manifest) { x.Version = 1 }).check(), "version"},
		{"source", m(func(x *Manifest) { x.Source = strings.Repeat("s", 256) }).check(), "source"},
		{"missing count", m(func(x *Manifest) { delete(x.Counts, "posts") }).check(), "counts"},
		{"extra count", m(func(x *Manifest) { x.Counts["extra"] = 1 }).check(), "counts"},
		{"negative count", m(func(x *Manifest) { x.Counts["posts"] = -1 }).check(), "counts.posts"},
		{"board name", s(func(x *Settings) { x.BoardName = "" }).check(), "board_name"},
		{"board description", s(func(x *Settings) { x.BoardDescription = strings.Repeat("d", 256) }).check(), "board_description"},
		{"timezone", s(func(x *Settings) { x.Timezone = "Nowhere/Land" }).check(), "timezone"},
		{"board name NBSP only", s(func(x *Settings) { x.BoardName = "\u00a0" }).check(), "board_name"},
		{"scheme", s(func(x *Settings) { x.DefaultColorScheme = "red" }).check(), "default_color_scheme"},
		{"registration", s(func(x *Settings) { x.Registration = "maybe" }).check(), "registration"},
		{"header dir", s(func(x *Settings) { x.HeaderImage = ptr(ref("avatars")) }).check(), "header_image.path"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, e := range tt.errs {
				if e.Field == tt.field {
					return
				}
			}
			t.Errorf("no error for field %q; got %v", tt.field, tt.errs)
		})
	}
	ok := validSettings()
	ok.HeaderImage = ptr(ref("header"))
	if errs := ok.check(); len(errs) != 0 {
		t.Errorf("valid header image: %v", errs[0])
	}
}

func TestNameFaultIsOneError(t *testing.T) {
	for _, name := range []string{"alice\n", "\talice", "alice\r\n", "alice\u0085", "\n", "\u00a0", " "} {
		u := validUser()
		u.Username = name
		if got := u.check(); len(got) != 1 {
			t.Errorf("username %q: %d errors, want 1: %v", name, len(got), got)
		}
	}
}

func TestLinkURLFaultIsOneError(t *testing.T) {
	for _, u := range []string{"a\x00b", "a\xffb", " https://example.com/", "javascript:x"} {
		f := validForum()
		f.Kind, f.LinkURL = "link", ptr(u)
		if got := f.check(); len(got) != 1 {
			t.Errorf("link_url %q: %d errors, want 1: %v", u, len(got), got)
		}
	}
}

func TestCheckReportsEveryFault(t *testing.T) {
	u := validUser()
	u.Username = ""
	u.State = "nope"
	u.Timezone = "nope"
	if got := len(u.check()); got != 3 {
		t.Errorf("got %d errors, want 3", got)
	}
}

func TestSettingConstants(t *testing.T) {
	if Yes != "yes" || No != "no" || Never != "never" {
		t.Error("Setting constants have the wrong values")
	}
}

func TestCheckEmailBan(t *testing.T) {
	ok := Ban{Kind: "email", Email: ptr("spam@example.com"), CreatedAt: testTime}
	if errs := ok.check(); len(errs) != 0 {
		t.Errorf("valid email ban: %v", errs[0])
	}
}

// TestCheckInt32Bounds pins the limit of every integer that lands in a 32-bit
// database column: the value 2,147,483,647 passes and the next value fails.
func TestCheckInt32Bounds(t *testing.T) {
	max := int(math.MaxInt32)
	over := max + 1
	tests := []struct {
		name  string
		field string
		row   func(n int) Row
		// atMax is false when another rule makes the maximum itself invalid.
		skipMax bool
	}{
		{"post_count", "post_count", func(n int) Row { u := validUser(); u.PostCount = n; return u }, false},
		{"view_count", "view_count", func(n int) Row { x := validTopic(); x.ViewCount = n; return x }, false},
		{"edit_count", "edit_count", func(n int) Row { x := validPost(); x.EditCount = n; return x }, false},
		{"download_count", "download_count", func(n int) Row { a := validAttachment(); a.DownloadCount = n; return a }, false},
		{"votes", "options[1].votes", func(n int) Row { p := validPoll(); p.Options[1].Votes = n; return p }, false},
		{"min_posts", "min_posts", func(n int) Row { return Rank{ID: 1, Title: "t", MinPosts: n} }, false},
		{"max_options", "max_options", func(n int) Row { p := validPoll(); p.MaxOptions = n; return p }, true},
		{"option id", "options[1].id", func(n int) Row { p := validPoll(); p.Options[1].ID = n; return p }, false},
		{"vote option_id", "option_id", func(n int) Row { return PollVote{TopicID: 1, OptionID: n, UserID: 1} }, false},
		{"attachment position", "position", func(n int) Row { a := validAttachment(); a.Position = n; return a }, false},
		{"role position", "position", func(n int) Row { r := validRole(); r.Position = n; return r }, false},
		{"forum position", "position", func(n int) Row { f := validForum(); f.Position = n; return f }, false},
		{"smiley position", "position", func(n int) Row { s := validSmiley(); s.Position = n; return s }, false},
		{"route position", "position", func(n int) Row { r := validRoute(); r.Position = n; return r }, false},
		{"width", "width", func(n int) Row { a := validAttachment(); a.Width = &n; return a }, false},
		{"height", "height", func(n int) Row { a := validAttachment(); a.Height = &n; return a }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !tt.skipMax {
				for _, e := range checkRow(t, tt.row(max)) {
					t.Errorf("value %d: %v", max, e)
				}
			}
			for _, e := range checkRow(t, tt.row(over)) {
				if e.Field == tt.field {
					return
				}
			}
			t.Errorf("value %d: no error for field %q", over, tt.field)
		})
	}
	// A position may be negative, down to the 32-bit minimum.
	r := validRole()
	r.Position = math.MinInt32
	if errs := r.check(); len(errs) != 0 {
		t.Errorf("minimum position: %v", errs[0])
	}
	r.Position = math.MinInt32 - 1
	if errs := r.check(); len(errs) == 0 {
		t.Error("position below the 32-bit minimum passes")
	}
}

// TestCheckGonePeople covers a private message and an attachment whose person
// is gone: the ID is null and the label stays.
func TestCheckGonePeople(t *testing.T) {
	gone := validPM()
	gone.AuthorID, gone.AuthorName = nil, "ghost"
	live := validPM()
	live.Recipients = nil
	upload := validAttachment()
	upload.UploaderID = nil
	for name, row := range map[string]Row{
		"gone author with a recipient":  gone,
		"live author and no recipients": live,
		"gone uploader":                 upload,
	} {
		if errs := firstError(row); errs != nil {
			t.Errorf("%s: %v", name, errs)
		}
	}
}

// TestCheckNameLimitIsRaw proves that a name is measured as given: 255 bytes pass,
// and 255 bytes plus trailing spaces do not.
func TestCheckNameLimitIsRaw(t *testing.T) {
	u := validUser()
	u.Username = strings.Repeat("a", 255)
	if err := firstError(u); err != nil {
		t.Errorf("255 bytes: %v", err)
	}
	u.Username += " "
	if err := firstError(u); err == nil || !strings.Contains(err.Error(), "256 bytes") {
		t.Errorf("255 bytes and a space = %v, want a length error", err)
	}
}
