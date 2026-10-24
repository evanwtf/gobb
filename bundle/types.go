package bundle

import (
	"strconv"
	"time"
)

// Format is the value of the "format" field of manifest.json.
const Format = "gobb-bundle"

// Version is the version of the format that this package reads and writes.
const Version = 1

// MaxLineBytes is the longest line that a data file may hold, in bytes.
const MaxLineBytes = 1 << 20

// MaxSourceBytes is the longest string that a field may hold, in bytes, unless
// the format sets a smaller limit. It is also the limit for the Markdown source
// of a [Body].
const MaxSourceBytes = 100_000

// Files lists the names of the 23 data files in load order. A name has no
// ".jsonl" suffix. A row that points at another row comes after the file of the
// row it points at.
var Files = []string{
	"ranks",
	"users",
	"gone_users",
	"groups",
	"group_members",
	"roles",
	"grants",
	"forums",
	"topics",
	"posts",
	"polls",
	"poll_votes",
	"attachments",
	"pms",
	"topic_watches",
	"bookmarks",
	"topic_marks",
	"forum_marks",
	"bans",
	"smilies",
	"legacy_routes",
	"legacy_files",
	"static_pages",
}

// Row is one line of a data file. Every data-file type implements it.
type Row interface {
	// DataFile returns the name of the file that holds the row, without the
	// ".jsonl" suffix. The name is in [Files].
	DataFile() string
}

// checker is the interface of a type that checks the rules a single row can
// prove. Every Row implements it. The checks leave File and Line empty.
type checker interface {
	check() []*Error
}

// Error describes one fault in a bundle. It names the file, the line and the
// field, so a converter can fix its output from the message alone.
type Error struct {
	File  string // data file name with suffix, for example "posts.jsonl"
	Line  int    // line number, counted from 1; 0 when the fault has no line
	Field string // field path, for example "body.source"; empty when none
	Msg   string // what is wrong
}

// Error returns the message in the form "file:line: field: msg". It drops ":line"
// when Line is 0, and "field: " when Field is empty. When File is empty and Line
// is set, it starts with "line N" instead.
func (e *Error) Error() string {
	s := e.File
	switch {
	case e.Line > 0 && s == "":
		s = "line " + strconv.Itoa(e.Line)
	case e.Line > 0:
		s += ":" + strconv.Itoa(e.Line)
	}
	if s != "" {
		s += ": "
	}
	if e.Field != "" {
		s += e.Field + ": "
	}
	return s + e.Msg
}

// Setting is the state of one permission option: "yes", "no" or "never".
type Setting string

// The three values of a [Setting]. Never always wins over Yes.
const (
	Yes   Setting = "yes"
	No    Setting = "no"
	Never Setting = "never"
)

// FileRef names a file under the bundle's files directory. Path is a
// slash-separated path. Its first element is the directory that the field
// names, for example "avatars".
type FileRef struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"` // 64 lowercase hex characters
}

// Body is the content of a post, a private message, a signature or a static
// page. Source is Markdown. The three flags say which Markdown features apply.
type Body struct {
	Source   string `json:"source"`
	Markdown bool   `json:"markdown"`
	Smilies  bool   `json:"smilies"`
	Autolink bool   `json:"autolink"`
}

// Password is the stored password hash of a user. Scheme is "legacy" or "gobb".
type Password struct {
	Scheme string `json:"scheme"`
	Hash   string `json:"hash"`
}

// RoleSetting is one option of a [Role] and its setting.
type RoleSetting struct {
	Option  string  `json:"option"`
	Setting Setting `json:"setting"`
}

// PollOption is one choice of a [Poll]. ID is unique within the poll.
type PollOption struct {
	ID    int    `json:"id"`
	Text  string `json:"text"`
	Votes int    `json:"votes"`
}

// Recipient is one recipient of a [PM]. Kind is "to" or "bcc".
type Recipient struct {
	UserID  int64  `json:"user_id"`
	Kind    string `json:"kind"`
	Read    bool   `json:"read"`
	Deleted bool   `json:"deleted"`
}

// Manifest is the content of manifest.json. Counts has one key for each name in
// [Files], with the line count of that file.
type Manifest struct {
	Format    string         `json:"format"`
	Version   int            `json:"version"`
	CreatedAt time.Time      `json:"created_at"`
	Source    string         `json:"source"`
	Counts    map[string]int `json:"counts"`
}

// Settings is the content of settings.json: the board-wide settings.
type Settings struct {
	BoardName          string   `json:"board_name"`
	BoardDescription   string   `json:"board_description"`
	Timezone           string   `json:"timezone"`
	DefaultColorScheme string   `json:"default_color_scheme"`
	Registration       string   `json:"registration"`
	EmailNotifications bool     `json:"email_notifications"`
	HeaderImage        *FileRef `json:"header_image"`
}

// Rank is a row of ranks.jsonl.
type Rank struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	MinPosts int      `json:"min_posts"`
	Special  bool     `json:"special"`
	Image    *FileRef `json:"image"`
}

// User is a row of users.jsonl.
type User struct {
	ID            int64      `json:"id"`
	Username      string     `json:"username"`
	Email         string     `json:"email"`
	Password      Password   `json:"password"`
	State         string     `json:"state"`
	Owner         bool       `json:"owner"`
	EmailVerified bool       `json:"email_verified"`
	RegisteredAt  time.Time  `json:"registered_at"`
	LastVisitAt   *time.Time `json:"last_visit_at"`
	LastMarkAt    *time.Time `json:"last_mark_at"`
	PostCount     int        `json:"post_count"`
	RankID        *int64     `json:"rank_id"`
	Avatar        *FileRef   `json:"avatar"`
	Signature     Body       `json:"signature"`
	Timezone      string     `json:"timezone"`
	ColorScheme   string     `json:"color_scheme"`
	NotifyOnPost  bool       `json:"notify_on_post"`
}

// GoneUser is a row of gone_users.jsonl: the ID of a user that the source system
// deleted. Posts may still point at it.
type GoneUser struct {
	ID int64 `json:"id"`
}

// Group is a row of groups.jsonl.
type Group struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Builtin     *string `json:"builtin"`
	Kind        string  `json:"kind"`
	Description string  `json:"description"`
}

// GroupMember is a row of group_members.jsonl.
type GroupMember struct {
	GroupID int64 `json:"group_id"`
	UserID  int64 `json:"user_id"`
	Leader  bool  `json:"leader"`
	Pending bool  `json:"pending"`
}

// Role is a row of roles.jsonl: a named set of option settings.
type Role struct {
	ID          int64         `json:"id"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	Description string        `json:"description"`
	Position    int           `json:"position"`
	Settings    []RoleSetting `json:"settings"`
}

// Grant is a row of grants.jsonl. Exactly one of GroupID and UserID is set.
// Exactly one of RoleID and the pair Option and Setting is set.
type Grant struct {
	GroupID *int64   `json:"group_id"`
	UserID  *int64   `json:"user_id"`
	ForumID int64    `json:"forum_id"` // 0 means board-wide
	RoleID  *int64   `json:"role_id"`
	Option  *string  `json:"option"`
	Setting *Setting `json:"setting"`
}

// Forum is a row of forums.jsonl.
type Forum struct {
	ID          int64   `json:"id"`
	ParentID    *int64  `json:"parent_id"`
	Kind        string  `json:"kind"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	Rules       string  `json:"rules"`
	LinkURL     *string `json:"link_url"`
	Locked      bool    `json:"locked"`
	Position    int     `json:"position"`
}

// Topic is a row of topics.jsonl.
type Topic struct {
	ID         int64     `json:"id"`
	ForumID    int64     `json:"forum_id"`
	Title      string    `json:"title"`
	AuthorID   *int64    `json:"author_id"`
	AuthorName string    `json:"author_name"`
	CreatedAt  time.Time `json:"created_at"`
	ViewCount  int       `json:"view_count"`
	Kind       string    `json:"kind"`
	Locked     bool      `json:"locked"`
	Visibility string    `json:"visibility"`
	MovedTo    *int64    `json:"moved_to"`
}

// Post is a row of posts.jsonl.
type Post struct {
	ID            int64      `json:"id"`
	TopicID       int64      `json:"topic_id"`
	AuthorID      *int64     `json:"author_id"`
	AuthorName    string     `json:"author_name"`
	AuthorIP      *string    `json:"author_ip"`
	CreatedAt     time.Time  `json:"created_at"`
	Subject       string     `json:"subject"`
	Body          Body       `json:"body"`
	ShowSignature bool       `json:"show_signature"`
	Visibility    string     `json:"visibility"`
	EditCount     int        `json:"edit_count"`
	EditedAt      *time.Time `json:"edited_at"`
	EditedBy      *int64     `json:"edited_by"`
	EditReason    string     `json:"edit_reason"`
}

// Poll is a row of polls.jsonl. A topic has at most one poll.
type Poll struct {
	TopicID       int64        `json:"topic_id"`
	Question      string       `json:"question"`
	MaxOptions    int          `json:"max_options"`
	EndsAt        *time.Time   `json:"ends_at"`
	CanChangeVote bool         `json:"can_change_vote"`
	Options       []PollOption `json:"options"`
}

// PollVote is a row of poll_votes.jsonl.
type PollVote struct {
	TopicID  int64   `json:"topic_id"`
	OptionID int     `json:"option_id"`
	UserID   int64   `json:"user_id"`
	VoterIP  *string `json:"voter_ip"`
}

// Attachment is a row of attachments.jsonl. An upload whose uploader is gone has
// a nil UploaderID.
type Attachment struct {
	ID            int64     `json:"id"`
	PostID        *int64    `json:"post_id"`
	Position      int       `json:"position"`
	UploaderID    *int64    `json:"uploader_id"`
	Name          string    `json:"name"`
	File          FileRef   `json:"file"`
	Mime          string    `json:"mime"`
	Width         *int      `json:"width"`
	Height        *int      `json:"height"`
	DownloadCount int       `json:"download_count"`
	Comment       string    `json:"comment"`
	CreatedAt     time.Time `json:"created_at"`
}

// PM is a row of pms.jsonl: one private message with all its recipients. A
// message whose author is gone has a nil AuthorID and keeps AuthorName.
type PM struct {
	ID            int64       `json:"id"`
	AuthorID      *int64      `json:"author_id"`
	AuthorName    string      `json:"author_name"`
	AuthorIP      *string     `json:"author_ip"`
	CreatedAt     time.Time   `json:"created_at"`
	Subject       string      `json:"subject"`
	Body          Body        `json:"body"`
	ShowSignature bool        `json:"show_signature"`
	AuthorDeleted bool        `json:"author_deleted"`
	Recipients    []Recipient `json:"recipients"`
}

// TopicWatch is a row of topic_watches.jsonl.
type TopicWatch struct {
	UserID        int64 `json:"user_id"`
	TopicID       int64 `json:"topic_id"`
	NotifyPending bool  `json:"notify_pending"`
}

// Bookmark is a row of bookmarks.jsonl.
type Bookmark struct {
	UserID  int64 `json:"user_id"`
	TopicID int64 `json:"topic_id"`
}

// TopicMark is a row of topic_marks.jsonl: the time a user last read a topic.
type TopicMark struct {
	UserID   int64     `json:"user_id"`
	TopicID  int64     `json:"topic_id"`
	MarkedAt time.Time `json:"marked_at"`
}

// ForumMark is a row of forum_marks.jsonl: the time a user marked a forum read.
type ForumMark struct {
	UserID   int64     `json:"user_id"`
	ForumID  int64     `json:"forum_id"`
	MarkedAt time.Time `json:"marked_at"`
}

// Ban is a row of bans.jsonl. Kind is "user", "email" or "ip". Exactly the field
// that matches Kind is set.
type Ban struct {
	Kind        string     `json:"kind"`
	UserID      *int64     `json:"user_id"`
	Email       *string    `json:"email"`
	IP          *string    `json:"ip"` // a CIDR prefix with no host bits set
	Reason      string     `json:"reason"`
	ReasonShown string     `json:"reason_shown"`
	CreatedAt   time.Time  `json:"created_at"`
	EndsAt      *time.Time `json:"ends_at"`
}

// Smiley is a row of smilies.jsonl.
type Smiley struct {
	Code        string  `json:"code"`
	Image       FileRef `json:"image"`
	Description string  `json:"description"`
	Position    int     `json:"position"`
}

// LegacyRoute is a row of legacy_routes.jsonl: an old URL that gobb redirects.
type LegacyRoute struct {
	Path        string            `json:"path"`
	Match       map[string]string `json:"match"`
	IDParam     *string           `json:"id_param"`
	TargetKind  string            `json:"target_kind"`
	TargetID    *int64            `json:"target_id"`
	TargetSlug  *string           `json:"target_slug"`
	OffsetParam *string           `json:"offset_param"`
	Position    int               `json:"position"`
}

// LegacyFile is a row of legacy_files.jsonl: an old URL prefix for a smiley or
// rank image directory.
type LegacyFile struct {
	Prefix string `json:"prefix"`
	Dir    string `json:"dir"`
}

// StaticPage is a row of static_pages.jsonl.
type StaticPage struct {
	Slug      string    `json:"slug"`
	Title     string    `json:"title"`
	Body      Body      `json:"body"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DataFile returns "ranks".
func (Rank) DataFile() string { return "ranks" }

// DataFile returns "users".
func (User) DataFile() string { return "users" }

// DataFile returns "gone_users".
func (GoneUser) DataFile() string { return "gone_users" }

// DataFile returns "groups".
func (Group) DataFile() string { return "groups" }

// DataFile returns "group_members".
func (GroupMember) DataFile() string { return "group_members" }

// DataFile returns "roles".
func (Role) DataFile() string { return "roles" }

// DataFile returns "grants".
func (Grant) DataFile() string { return "grants" }

// DataFile returns "forums".
func (Forum) DataFile() string { return "forums" }

// DataFile returns "topics".
func (Topic) DataFile() string { return "topics" }

// DataFile returns "posts".
func (Post) DataFile() string { return "posts" }

// DataFile returns "polls".
func (Poll) DataFile() string { return "polls" }

// DataFile returns "poll_votes".
func (PollVote) DataFile() string { return "poll_votes" }

// DataFile returns "attachments".
func (Attachment) DataFile() string { return "attachments" }

// DataFile returns "pms".
func (PM) DataFile() string { return "pms" }

// DataFile returns "topic_watches".
func (TopicWatch) DataFile() string { return "topic_watches" }

// DataFile returns "bookmarks".
func (Bookmark) DataFile() string { return "bookmarks" }

// DataFile returns "topic_marks".
func (TopicMark) DataFile() string { return "topic_marks" }

// DataFile returns "forum_marks".
func (ForumMark) DataFile() string { return "forum_marks" }

// DataFile returns "bans".
func (Ban) DataFile() string { return "bans" }

// DataFile returns "smilies".
func (Smiley) DataFile() string { return "smilies" }

// DataFile returns "legacy_routes".
func (LegacyRoute) DataFile() string { return "legacy_routes" }

// DataFile returns "legacy_files".
func (LegacyFile) DataFile() string { return "legacy_files" }

// DataFile returns "static_pages".
func (StaticPage) DataFile() string { return "static_pages" }
