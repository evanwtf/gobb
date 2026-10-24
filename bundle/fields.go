package bundle

import (
	"errors"
	"fmt"
	"io/fs"
	"net/netip"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata" // zone data for a host that has none of its own
	"unicode"
	"unicode/utf8"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/password"
	"github.com/evanwtf/gobb/markup"
)

// maxName is the longest name or label, in bytes.
const maxName = 255

// maxInt32 is the largest value of an integer that lands in a 32-bit database
// column.
const maxInt32 = 1<<31 - 1

// minInt32 is the smallest value of a 32-bit database column.
const minInt32 = -1 << 31

var (
	slugRE   = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	paramRE  = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)
	sha256RE = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// The enum values of the format.
var (
	colorSchemes   = []string{"slate", "slate-deep", "slate-light"}
	registrations  = []string{"closed", "open", "approval"}
	passwordScheme = []string{"legacy", "gobb"}
	userStates     = []string{"active", "inactive"}
	builtinGroups  = []string{"guests", "members", "moderators", "admins"}
	groupKinds     = []string{"open", "request", "closed", "hidden", "builtin"}
	roleKinds      = []string{"forum", "mod", "board"}
	forumKinds     = []string{"category", "forum", "link"}
	topicKinds     = []string{"normal", "sticky", "announce", "global"}
	visibilities   = []string{"approved", "unapproved", "deleted"}
	mimeTypes      = []string{"image/png", "image/jpeg", "image/gif", "image/webp"}
	recipientKinds = []string{"to", "bcc"}
	banKinds       = []string{"user", "email", "ip"}
	routeKinds     = []string{
		"home", "forum", "topic", "post", "user", "members", "team",
		"attachment", "avatar", "search", "account", "page",
	}
	routeKindsWithID = []string{"forum", "topic", "post", "user", "attachment", "avatar"}
	routeKindsOffset = []string{"forum", "topic"}
	legacyFileDirs   = []string{"smilies", "ranks"}
	settingValues    = []string{string(Yes), string(No), string(Never)}
)

// vet collects the faults of one row.
type vet struct{ errs []*Error }

func (v *vet) add(field, format string, args ...any) {
	v.errs = append(v.errs, &Error{Field: field, Msg: fmt.Sprintf(format, args...)})
}

// text applies the rules for every string: valid UTF-8, no U+0000, and at most
// MaxSourceBytes bytes. It reports whether the string passes.
func (v *vet) text(field, s string) bool {
	switch {
	case !utf8.ValidString(s):
		v.add(field, "is not valid UTF-8")
	case strings.IndexByte(s, 0) >= 0:
		v.add(field, "holds U+0000")
	case len(s) > MaxSourceBytes:
		v.add(field, "is %d bytes; the limit is %d", len(s), MaxSourceBytes)
	default:
		return true
	}
	return false
}

func hasControl(s string) bool { return strings.IndexFunc(s, unicode.IsControl) >= 0 }

// name checks a "name": 1 to 255 bytes after trimming Unicode white space (as
// strings.TrimSpace does). The value holds no control character anywhere, ends
// included, so a trailing newline is refused.
func (v *vet) name(field, s string) {
	if !v.text(field, s) {
		return
	}
	t := strings.TrimSpace(s)
	switch {
	case hasControl(s):
		v.add(field, "holds a control character")
	case t == "":
		v.add(field, "is empty after trimming white space")
	case len(t) > maxName:
		v.add(field, "is %d bytes; the limit is %d", len(t), maxName)
	}
}

// label checks a "label": at most 255 bytes, no control characters.
func (v *vet) label(field, s string) {
	if !v.text(field, s) {
		return
	}
	switch {
	case len(s) > maxName:
		v.add(field, "is %d bytes; the limit is %d", len(s), maxName)
	case hasControl(s):
		v.add(field, "holds a control character")
	}
}

func (v *vet) optLabel(field string, s *string) {
	if s != nil {
		v.label(field, *s)
	}
}

func (v *vet) slug(field, s string) {
	if v.text(field, s) && !slugRE.MatchString(s) {
		v.add(field, "must match %s", slugRE)
	}
}

func (v *vet) param(field, s string) {
	if v.text(field, s) && !paramRE.MatchString(s) {
		v.add(field, "must match %s", paramRE)
	}
}

// enum checks that s is one of vals.
func (v *vet) enum(field, s string, vals []string) bool {
	if slices.Contains(vals, s) {
		return true
	}
	v.add(field, "is %q; it must be one of %s", clip(s), strings.Join(vals, ", "))
	return false
}

// clip shortens s for a message.
func clip(s string) string {
	if len(s) > 40 {
		return strings.ToValidUTF8(s[:40], "?") + "..."
	}
	return s
}

func (v *vet) id(field string, n int64) {
	if n < 1 {
		v.add(field, "is %d; an ID is 1 or more", n)
	}
}

func (v *vet) optID(field string, n *int64) {
	if n != nil {
		v.id(field, *n)
	}
}

// count checks a count: 0 to 2,147,483,647.
func (v *vet) count(field string, n int) {
	switch {
	case n < 0:
		v.add(field, "is %d; it must be 0 or more", n)
	case n > maxInt32:
		v.add(field, "is %d; the limit is %d", n, maxInt32)
	}
}

// position checks a position: any value that fits a 32-bit column.
func (v *vet) position(field string, n int) {
	if n < minInt32 || n > maxInt32 {
		v.add(field, "is %d; it must be from %d to %d", n, minInt32, maxInt32)
	}
}

// optSize checks an optional pixel size: 1 to 2,147,483,647.
func (v *vet) optSize(field string, n *int) {
	switch {
	case n == nil:
	case *n < 1:
		v.add(field, "is %d; it must be 1 or more", *n)
	case *n > maxInt32:
		v.add(field, "is %d; the limit is %d", *n, maxInt32)
	}
}

// optionID checks the ID of a poll option: 1 to 2,147,483,647.
func (v *vet) optionID(field string, n int) {
	switch {
	case n < 1:
		v.add(field, "is %d; an option ID is 1 or more", n)
	case n > maxInt32:
		v.add(field, "is %d; the limit is %d", n, maxInt32)
	}
}

// ip checks an optional IP address.
func (v *vet) ip(field string, s *string) {
	if s == nil || !v.text(field, *s) {
		return
	}
	a, err := netip.ParseAddr(*s)
	switch {
	case err != nil:
		v.add(field, "is not an IP address")
	case a.Zone() != "":
		v.add(field, "must not have a zone")
	}
}

// prefix checks a CIDR prefix with no host bits set.
func (v *vet) prefix(field, s string) {
	if !v.text(field, s) {
		return
	}
	p, err := netip.ParsePrefix(s)
	switch {
	case err != nil:
		v.add(field, "is not a CIDR prefix")
	case p != p.Masked():
		v.add(field, "has host bits set; write %s", p.Masked())
	}
}

// filePath checks the path of a file reference. dir is the directory it must
// start with. The path is at most 255 bytes, clean, slash-separated, and holds
// no backslash.
func (v *vet) filePath(field, p, dir string) {
	if !v.text(field, p) {
		return
	}
	switch {
	case len(p) > maxName:
		v.add(field, "is %d bytes; the limit is %d", len(p), maxName)
	case strings.Contains(p, `\`):
		v.add(field, "holds a backslash")
	case !fs.ValidPath(p) || p == ".":
		v.add(field, "is not a clean slash-separated path")
	case !strings.HasPrefix(p, dir+"/") || len(p) == len(dir)+1:
		v.add(field, "must name a file under %s/", dir)
	}
}

// file checks a file reference. dir is the directory its path must start with.
func (v *vet) file(field string, f FileRef, dir string) {
	v.filePath(field+".path", f.Path, dir)
	if f.Size < 0 {
		v.add(field+".size", "is %d; it must be 0 or more", f.Size)
	}
	if !sha256RE.MatchString(f.SHA256) {
		v.add(field+".sha256", "must be 64 lowercase hex characters")
	}
}

func (v *vet) optFile(field string, f *FileRef, dir string) {
	if f != nil {
		v.file(field, *f, dir)
	}
}

// body checks a body.
func (v *vet) body(field string, b Body) { v.text(field+".source", b.Source) }

// maxZoneName is the longest zone name that the check passes to LoadLocation.
const maxZoneName = 64

// zoneOK holds the zone names that LoadLocation has accepted. A failed name is
// not cached, so a hostile bundle cannot grow the map.
var zoneOK sync.Map // name -> struct{}

// zone checks that s is an IANA zone name that time.LoadLocation accepts.
// LoadLocation also takes "" (UTC) and "Local"; the format does not. The answer
// can depend on the host: LoadLocation reads the host's zoneinfo first, and uses
// the copy embedded by time/tzdata only when the host has none.
func (v *vet) zone(field, s string) {
	if !v.text(field, s) {
		return
	}
	if _, ok := zoneOK.Load(s); ok {
		return
	}
	if s != "" && s != "Local" && len(s) <= maxZoneName {
		if _, err := time.LoadLocation(s); err == nil {
			zoneOK.Store(s, struct{}{})
			return
		}
	}
	v.add(field, "is %q; it must be an IANA time zone name of at most %d bytes", clip(s), maxZoneName)
}

// smileyCode checks that code is valid as the code of a markup smiley element.
func (v *vet) smileyCode(field, code string) {
	if !v.text(field, code) {
		return
	}
	tree := &markup.Node{Kind: markup.Root, Children: []*markup.Node{{
		Kind: markup.Element, Name: "p", Children: []*markup.Node{{
			Kind: markup.Element, Name: "smiley", Attrs: map[string]string{"code": code},
		}},
	}}}
	if err := markup.Validate(tree); err != nil {
		msg := err.Error()
		var me *markup.Error
		if errors.As(err, &me) {
			msg = me.Msg
		}
		v.add(field, "is not a valid smiley code: %s", msg)
	}
}

// formatVersion checks the two fields that tell a reader whether it can read
// the rest of manifest.json.
func (v *vet) formatVersion(format string, version int) {
	if format != Format {
		v.add("format", "is %q; it must be %q", clip(format), Format)
	}
	if version != Version {
		v.add("version", "is %d; this reader knows version %d", version, Version)
	}
}

func (m Manifest) check() []*Error {
	var v vet
	v.formatVersion(m.Format, m.Version)
	v.label("source", m.Source)
	for _, f := range Files {
		n, ok := m.Counts[f]
		switch {
		case !ok:
			v.add("counts", "has no key %q", f)
		case n < 0:
			v.add("counts."+f, "is %d; it must be 0 or more", n)
		}
	}
	for _, k := range sortedKeys(m.Counts) {
		if !slices.Contains(Files, k) {
			v.add("counts", "has the unknown key %q", clip(k))
		}
	}
	return v.errs
}

func (s Settings) check() []*Error {
	var v vet
	v.name("board_name", s.BoardName)
	v.label("board_description", s.BoardDescription)
	v.zone("timezone", s.Timezone)
	v.enum("default_color_scheme", s.DefaultColorScheme, colorSchemes)
	v.enum("registration", s.Registration, registrations)
	v.optFile("header_image", s.HeaderImage, "header")
	return v.errs
}

func (r Rank) check() []*Error {
	var v vet
	v.id("id", r.ID)
	v.label("title", r.Title)
	v.count("min_posts", r.MinPosts)
	v.optFile("image", r.Image, "ranks")
	return v.errs
}

func (u User) check() []*Error {
	var v vet
	v.id("id", u.ID)
	v.name("username", u.Username)
	if strings.TrimSpace(u.Username) != "" && account.CleanUsername(u.Username) == "" {
		v.add("username", "is empty after gobb cleans it")
	}
	v.label("email", u.Email)
	if v.enum("password.scheme", u.Password.Scheme, passwordScheme) &&
		v.text("password.hash", u.Password.Hash) &&
		!password.Recognized(u.Password.Scheme, u.Password.Hash) {
		v.add("password.hash", "is not a hash that scheme %s can verify", u.Password.Scheme)
	}
	v.enum("state", u.State, userStates)
	v.count("post_count", u.PostCount)
	v.optID("rank_id", u.RankID)
	v.optFile("avatar", u.Avatar, "avatars")
	v.body("signature", u.Signature)
	v.zone("timezone", u.Timezone)
	v.enum("color_scheme", u.ColorScheme, colorSchemes)
	return v.errs
}

func (g GoneUser) check() []*Error {
	var v vet
	v.id("id", g.ID)
	return v.errs
}

func (g Group) check() []*Error {
	var v vet
	v.id("id", g.ID)
	v.name("name", g.Name)
	if g.Builtin != nil {
		v.enum("builtin", *g.Builtin, builtinGroups)
	}
	if v.enum("kind", g.Kind, groupKinds) && (g.Kind == "builtin") != (g.Builtin != nil) {
		v.add("kind", "must be builtin exactly when builtin is set")
	}
	v.text("description", g.Description)
	return v.errs
}

func (g GroupMember) check() []*Error {
	var v vet
	v.id("group_id", g.GroupID)
	v.id("user_id", g.UserID)
	return v.errs
}

// scopeName names an option scope for a message.
func scopeName(s acl.Scope) string {
	switch s {
	case acl.Forum:
		return "forum"
	case acl.Board:
		return "board"
	default:
		return "moderator"
	}
}

func (r Role) check() []*Error {
	var v vet
	v.id("id", r.ID)
	v.name("name", r.Name)
	kindOK := v.enum("kind", r.Kind, roleKinds)
	v.text("description", r.Description)
	v.position("position", r.Position)
	var want acl.Scope
	switch r.Kind {
	case "forum":
		want = acl.Forum
	case "mod":
		want = acl.Moderator
	case "board":
		want = acl.Board
	}
	seen := map[string]bool{}
	for i, s := range r.Settings {
		opt := "settings[" + strconv.Itoa(i) + "].option"
		set := "settings[" + strconv.Itoa(i) + "].setting"
		if v.text(opt, s.Option) {
			o, ok := acl.Lookup(s.Option)
			switch {
			case !ok:
				v.add(opt, "%q is not an option", clip(s.Option))
			case seen[s.Option]:
				v.add(opt, "%q appears twice in the role", s.Option)
			case kindOK && o.Scope != want:
				v.add(opt, "%q is a %s option; a %s role cannot hold it", s.Option, scopeName(o.Scope), r.Kind)
			}
			seen[s.Option] = true
		}
		v.enum(set, string(s.Setting), settingValues)
	}
	return v.errs
}

func (g Grant) check() []*Error {
	var v vet
	switch {
	case g.GroupID != nil && g.UserID != nil:
		v.add("group_id", "set group_id or user_id, not both")
	case g.GroupID == nil && g.UserID == nil:
		v.add("group_id", "set group_id or user_id")
	}
	v.optID("group_id", g.GroupID)
	v.optID("user_id", g.UserID)
	if g.ForumID < 0 {
		v.add("forum_id", "is %d; it is 0 for board-wide, or a forum ID", g.ForumID)
	}
	v.optID("role_id", g.RoleID)
	switch {
	case g.RoleID != nil && g.Option != nil:
		v.add("role_id", "set role_id or option, not both")
	case g.RoleID != nil && g.Setting != nil:
		v.add("setting", "must be null when role_id is set")
	case g.RoleID == nil && g.Option == nil && g.Setting == nil:
		v.add("role_id", "set role_id, or set option and setting")
	case g.RoleID == nil && g.Option == nil:
		v.add("option", "is required when setting is set")
	case g.RoleID == nil && g.Setting == nil:
		v.add("setting", "is required when option is set")
	}
	if g.Setting != nil {
		v.enum("setting", string(*g.Setting), settingValues)
	}
	if g.Option != nil && v.text("option", *g.Option) {
		o, ok := acl.Lookup(*g.Option)
		switch {
		case !ok:
			v.add("option", "%q is not an option", clip(*g.Option))
		case o.Scope == acl.Forum && g.ForumID == 0:
			v.add("option", "%q is a forum option; set forum_id to a forum", *g.Option)
		case o.Scope == acl.Board && g.ForumID != 0:
			v.add("option", "%q is a board option; set forum_id to 0", *g.Option)
		}
	}
	return v.errs
}

func (f Forum) check() []*Error {
	var v vet
	v.id("id", f.ID)
	v.optID("parent_id", f.ParentID)
	if f.ParentID != nil && *f.ParentID == f.ID {
		v.add("parent_id", "is the forum's own ID")
	}
	v.enum("kind", f.Kind, forumKinds)
	v.name("name", f.Name)
	v.text("description", f.Description)
	v.text("rules", f.Rules)
	v.position("position", f.Position)
	switch {
	case f.Kind == "link" && f.LinkURL == nil:
		v.add("link_url", "is required when kind is link")
	case f.Kind != "link" && f.LinkURL != nil:
		v.add("link_url", "must be null unless kind is link")
	case f.LinkURL != nil && v.text("link_url", *f.LinkURL):
		switch u := *f.LinkURL; {
		case u != strings.TrimSpace(u):
			v.add("link_url", "must not start or end with white space")
		case !markup.CheckLinkURL(u):
			v.add("link_url", "is not an allowed link target")
		}
	}
	return v.errs
}

func (t Topic) check() []*Error {
	var v vet
	v.id("id", t.ID)
	v.id("forum_id", t.ForumID)
	v.label("title", t.Title)
	v.optID("author_id", t.AuthorID)
	v.label("author_name", t.AuthorName)
	v.count("view_count", t.ViewCount)
	v.enum("kind", t.Kind, topicKinds)
	v.enum("visibility", t.Visibility, visibilities)
	v.optID("moved_to", t.MovedTo)
	if t.MovedTo != nil && *t.MovedTo == t.ID {
		v.add("moved_to", "is the topic's own ID")
	}
	return v.errs
}

func (p Post) check() []*Error {
	var v vet
	v.id("id", p.ID)
	v.id("topic_id", p.TopicID)
	v.optID("author_id", p.AuthorID)
	v.label("author_name", p.AuthorName)
	v.ip("author_ip", p.AuthorIP)
	v.label("subject", p.Subject)
	v.body("body", p.Body)
	v.enum("visibility", p.Visibility, visibilities)
	v.count("edit_count", p.EditCount)
	v.optID("edited_by", p.EditedBy)
	v.label("edit_reason", p.EditReason)
	return v.errs
}

func (p Poll) check() []*Error {
	var v vet
	v.id("topic_id", p.TopicID)
	v.label("question", p.Question)
	n := len(p.Options)
	if n == 0 || n > 100 {
		v.add("options", "has %d options; it must have 1 to 100", n)
	}
	switch {
	case p.MaxOptions < 1:
		v.add("max_options", "is %d; it must be 1 or more", p.MaxOptions)
	case p.MaxOptions > maxInt32:
		v.add("max_options", "is %d; the limit is %d", p.MaxOptions, maxInt32)
	case n > 0 && p.MaxOptions > n:
		v.add("max_options", "is %d; the poll has %d options", p.MaxOptions, n)
	}
	seen := map[int]bool{}
	for i, o := range p.Options {
		at := "options[" + strconv.Itoa(i) + "]."
		v.optionID(at+"id", o.ID)
		if seen[o.ID] {
			v.add(at+"id", "%d appears twice in the poll", o.ID)
		}
		seen[o.ID] = true
		v.label(at+"text", o.Text)
		v.count(at+"votes", o.Votes)
	}
	return v.errs
}

func (p PollVote) check() []*Error {
	var v vet
	v.id("topic_id", p.TopicID)
	v.optionID("option_id", p.OptionID)
	v.id("user_id", p.UserID)
	v.ip("voter_ip", p.VoterIP)
	return v.errs
}

func (a Attachment) check() []*Error {
	var v vet
	v.id("id", a.ID)
	v.optID("post_id", a.PostID)
	v.count("position", a.Position)
	v.id("uploader_id", a.UploaderID)
	v.label("name", a.Name)
	v.file("file", a.File, "attachments")
	v.enum("mime", a.Mime, mimeTypes)
	v.optSize("width", a.Width)
	v.optSize("height", a.Height)
	v.count("download_count", a.DownloadCount)
	v.text("comment", a.Comment)
	return v.errs
}

func (p PM) check() []*Error {
	var v vet
	v.id("id", p.ID)
	v.id("author_id", p.AuthorID)
	v.ip("author_ip", p.AuthorIP)
	v.label("subject", p.Subject)
	v.body("body", p.Body)
	if len(p.Recipients) == 0 {
		v.add("recipients", "has no recipient; a message needs 1 or more")
	}
	seen := map[int64]bool{}
	for i, r := range p.Recipients {
		at := "recipients[" + strconv.Itoa(i) + "]."
		switch {
		case r.UserID < 1:
			v.id(at+"user_id", r.UserID)
		case seen[r.UserID]:
			v.add(at+"user_id", "user %d is a recipient twice", r.UserID)
		}
		seen[r.UserID] = true
		v.enum(at+"kind", r.Kind, recipientKinds)
	}
	return v.errs
}

func (w TopicWatch) check() []*Error {
	var v vet
	v.id("user_id", w.UserID)
	v.id("topic_id", w.TopicID)
	return v.errs
}

func (b Bookmark) check() []*Error {
	var v vet
	v.id("user_id", b.UserID)
	v.id("topic_id", b.TopicID)
	return v.errs
}

func (m TopicMark) check() []*Error {
	var v vet
	v.id("user_id", m.UserID)
	v.id("topic_id", m.TopicID)
	return v.errs
}

func (m ForumMark) check() []*Error {
	var v vet
	v.id("user_id", m.UserID)
	v.id("forum_id", m.ForumID)
	return v.errs
}

func (b Ban) check() []*Error {
	var v vet
	if v.enum("kind", b.Kind, banKinds) {
		for _, f := range []struct {
			kind, field string
			set         bool
		}{
			{"user", "user_id", b.UserID != nil},
			{"email", "email", b.Email != nil},
			{"ip", "ip", b.IP != nil},
		} {
			switch {
			case f.kind == b.Kind && !f.set:
				v.add(f.field, "is required when kind is %s", b.Kind)
			case f.kind != b.Kind && f.set:
				v.add(f.field, "must be null when kind is %s", b.Kind)
			}
		}
	}
	v.optID("user_id", b.UserID)
	v.optLabel("email", b.Email)
	if b.Kind == "email" && b.Email != nil && *b.Email == "" {
		v.add("email", "is empty; an email ban needs an address")
	}
	if b.IP != nil {
		v.prefix("ip", *b.IP)
	}
	v.label("reason", b.Reason)
	v.label("reason_shown", b.ReasonShown)
	return v.errs
}

func (s Smiley) check() []*Error {
	var v vet
	v.smileyCode("code", s.Code)
	v.file("image", s.Image, "smilies")
	v.label("description", s.Description)
	v.position("position", s.Position)
	return v.errs
}

func (r LegacyRoute) check() []*Error {
	var v vet
	v.position("position", r.Position)
	if v.text("path", r.Path) {
		switch {
		case r.Path == "" || len(r.Path) > maxName:
			v.add("path", "is %d bytes; it must be 1 to %d", len(r.Path), maxName)
		case strings.HasPrefix(r.Path, "/"):
			v.add("path", "must not start with /")
		case strings.ContainsAny(r.Path, "?#"):
			v.add("path", "must not hold ? or #")
		}
	}
	if len(r.Match) > 10 {
		v.add("match", "has %d pairs; the limit is 10", len(r.Match))
	}
	for _, k := range sortedKeys(r.Match) {
		if !paramRE.MatchString(k) {
			v.add("match", "key %q must match %s", clip(k), paramRE)
			continue
		}
		v.label("match."+k, r.Match[k])
	}
	if r.IDParam != nil {
		v.param("id_param", *r.IDParam)
	}
	if r.OffsetParam != nil {
		v.param("offset_param", *r.OffsetParam)
	}
	if r.TargetID != nil {
		v.id("target_id", *r.TargetID)
	}
	if r.TargetSlug != nil {
		v.slug("target_slug", *r.TargetSlug)
	}
	if !v.enum("target_kind", r.TargetKind, routeKinds) {
		return v.errs
	}
	switch {
	case slices.Contains(routeKindsWithID, r.TargetKind):
		switch {
		case r.IDParam != nil && r.TargetID != nil:
			v.add("id_param", "set id_param or target_id, not both")
		case r.IDParam == nil && r.TargetID == nil:
			v.add("id_param", "a %s route needs id_param or target_id", r.TargetKind)
		}
	default:
		if r.IDParam != nil {
			v.add("id_param", "must be null for a %s route", r.TargetKind)
		}
		if r.TargetID != nil {
			v.add("target_id", "must be null for a %s route", r.TargetKind)
		}
	}
	switch {
	case r.TargetKind == "page" && r.TargetSlug == nil:
		v.add("target_slug", "is required for a page route")
	case r.TargetKind != "page" && r.TargetSlug != nil:
		v.add("target_slug", "must be null for a %s route", r.TargetKind)
	}
	if r.OffsetParam != nil && !slices.Contains(routeKindsOffset, r.TargetKind) {
		v.add("offset_param", "must be null for a %s route", r.TargetKind)
	}
	return v.errs
}

func (f LegacyFile) check() []*Error {
	var v vet
	if v.text("prefix", f.Prefix) {
		switch {
		case f.Prefix == "" || len(f.Prefix) > maxName:
			v.add("prefix", "is %d bytes; it must be 1 to %d", len(f.Prefix), maxName)
		case strings.HasPrefix(f.Prefix, "/"):
			v.add("prefix", "must not start with /")
		case !strings.HasSuffix(f.Prefix, "/"):
			v.add("prefix", "must end with /")
		}
	}
	v.enum("dir", f.Dir, legacyFileDirs)
	return v.errs
}

func (p StaticPage) check() []*Error {
	var v vet
	v.slug("slug", p.Slug)
	v.name("title", p.Title)
	v.body("body", p.Body)
	return v.errs
}

// sortedKeys returns the keys of m in order, so the messages come in a fixed order.
func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
