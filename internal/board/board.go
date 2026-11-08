// Package board reads the board for the pages of the server: the settings, the
// viewer and the forum tree. It holds queries and rules, and no HTML.
package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"time"
	// The timezone database is compiled in, so that a zone name loads in the
	// static image, which has no zoneinfo files.
	_ "time/tzdata"

	"github.com/jackc/pgx/v5"

	"github.com/evanwtf/gobb/internal/acl"
	"github.com/evanwtf/gobb/internal/acldb"
	"github.com/evanwtf/gobb/internal/db"
)

var (
	// ErrNotFound means the row does not exist, or the viewer may not know that
	// it does. Both look the same to the viewer.
	ErrNotFound = errors.New("board: not found")
	// ErrForbidden means the viewer knows the row exists but may not use it.
	ErrForbidden = errors.New("board: forbidden")
	// ErrTopicLocked and ErrForumLocked are an [ErrForbidden] that says why: a
	// lock on the topic, or on its forum, stops a reply.
	ErrTopicLocked = fmt.Errorf("%w: the topic is locked", ErrForbidden)
	ErrForumLocked = fmt.Errorf("%w: the forum is locked", ErrForbidden)
	// ErrBanned is an [ErrForbidden] that says why: a user ban holds the member.
	// A change of the password or the address that was in flight when the ban
	// landed gets it, and changes nothing.
	ErrBanned = fmt.Errorf("%w: the member is banned", ErrForbidden)
	// ErrGone means the row existed and was removed for good.
	ErrGone = errors.New("board: gone")
)

// ErrNoSettings means the settings table holds none of the board keys. That is
// a database that no board was imported into.
var ErrNoSettings = errors.New("board: no board settings; run gobb import")

// Schemes lists the color schemes that gobb has built in. The first is the
// fallback.
var Schemes = []string{"slate", "slate-deep", "slate-light"}

// ValidScheme reports whether name is one of [Schemes].
func ValidScheme(name string) bool {
	for _, s := range Schemes {
		if s == name {
			return true
		}
	}
	return false
}

// Settings holds the board-wide settings that the pages use.
type Settings struct {
	BoardName          string
	BoardDescription   string
	Timezone           string
	DefaultScheme      string
	Registration       string
	EmailNotifications bool
	// HeaderImage is a path under the data directory, such as
	// "header/banner.png". It is empty when the board has no header image.
	HeaderImage string
	// AICrawlers is the policy of robots.txt for the AI crawlers: one of
	// [AICrawlerPolicies]. A board with no stored value has [AICrawlersAll].
	AICrawlers string
}

// The values of the setting ai_crawlers. Each names which AI crawlers robots.txt
// lets read the pages that a guest can see.
const (
	// AICrawlersAll lets every crawler read, AI training included.
	AICrawlersAll = "all"
	// AICrawlersSearch blocks the crawlers that collect text to train AI models,
	// and lets the crawlers of AI search and answers read.
	AICrawlersSearch = "search-and-answers"
	// AICrawlersNone blocks every known AI crawler.
	AICrawlersNone = "none"
)

// AICrawlerPolicies lists the values of the setting ai_crawlers, the default
// first.
var AICrawlerPolicies = []string{AICrawlersAll, AICrawlersSearch, AICrawlersNone}

// ValidAICrawlers reports whether v is one of [AICrawlerPolicies].
func ValidAICrawlers(v string) bool {
	for _, p := range AICrawlerPolicies {
		if p == v {
			return true
		}
	}
	return false
}

// LoadSettings reads the seven setting keys. A missing key is an error that
// names it.
func LoadSettings(ctx context.Context, q db.Querier) (Settings, error) {
	rows, err := q.Query(ctx, `select key, value from settings where key = any($1)`, append(slices.Clone(settingKeys), optionalKeys...))
	if err != nil {
		return Settings{}, fmt.Errorf("board: load settings: %w", err)
	}
	defer rows.Close()
	raw := map[string]json.RawMessage{}
	for rows.Next() {
		var (
			k string
			v json.RawMessage
		)
		if err := rows.Scan(&k, &v); err != nil {
			return Settings{}, fmt.Errorf("board: load settings: %w", err)
		}
		raw[k] = v
	}
	if err := rows.Err(); err != nil {
		return Settings{}, fmt.Errorf("board: load settings: %w", err)
	}

	if !slices.ContainsFunc(settingKeys, func(k string) bool { _, ok := raw[k]; return ok }) {
		return Settings{}, ErrNoSettings
	}
	var s Settings
	var header *string
	for _, f := range []struct {
		key string
		dst any
	}{
		{"board_name", &s.BoardName},
		{"board_description", &s.BoardDescription},
		{"timezone", &s.Timezone},
		{"default_color_scheme", &s.DefaultScheme},
		{"registration", &s.Registration},
		{"email_notifications", &s.EmailNotifications},
		{"header_image", &header},
	} {
		v, ok := raw[f.key]
		if !ok {
			return Settings{}, fmt.Errorf("board: setting %q is missing", f.key)
		}
		if err := json.Unmarshal(v, f.dst); err != nil {
			return Settings{}, fmt.Errorf("board: setting %q: %w", f.key, err)
		}
	}
	if header != nil {
		s.HeaderImage = *header
	}
	// ai_crawlers is optional: an import writes only the keys of the bundle, and
	// a board that never saved the setting has the default.
	s.AICrawlers = AICrawlersAll
	if v, ok := raw[keyAICrawlers]; ok {
		var policy string
		if err := json.Unmarshal(v, &policy); err != nil {
			return Settings{}, fmt.Errorf("board: setting %q: %w", keyAICrawlers, err)
		}
		if ValidAICrawlers(policy) {
			s.AICrawlers = policy
		}
	}
	return s, nil
}

// settingKeys are the keys that [LoadSettings] needs. It also reads the optional
// keys of optionalKeys.
var settingKeys = []string{
	"board_name", "board_description", "timezone", "default_color_scheme",
	"registration", "email_notifications", "header_image",
}

// optionalKeys are the keys that [LoadSettings] reads when they exist.
var optionalKeys = []string{keyAICrawlers}

// Viewer is the person who looks at a page: a guest or a member.
type Viewer struct {
	// UserID is 0 for a guest.
	UserID int64
	Name   string
	// Perms holds the resolved permissions of the viewer.
	Perms acl.Set
	// Loc is the zone in which the viewer sees times.
	Loc *time.Location
	// DateFormat is how the viewer sees times: "standard", "relative" or "iso".
	DateFormat string
	// Scheme is the color scheme of the viewer.
	Scheme string
	// LastVisit is the member's last_visit_at, or registered_at when that is
	// null. The "new since your last visit" list starts here. It is zero for a
	// guest.
	LastVisit time.Time
	// MarkFloor is the member's last_mark_at, or registered_at when that is
	// null. A topic whose last post is not after it counts as read. It is zero
	// for a guest.
	MarkFloor time.Time
	// LastActive is the member's last_active_at: the time of the last activity
	// that the database holds. It is zero for a guest, and for a member with no
	// activity yet.
	LastActive time.Time
}

// Guest returns the viewer who is not signed in. The guest sees times in the
// board time zone and the default color scheme, and holds the grants of the
// guests group.
func Guest(ctx context.Context, q db.Querier, s Settings) (Viewer, error) {
	subject, err := acldb.Load(ctx, q, 0)
	if err != nil {
		return Viewer{}, err
	}
	loc, err := time.LoadLocation(s.Timezone)
	if err != nil {
		return Viewer{}, fmt.Errorf("board: setting \"timezone\": %w", err)
	}
	scheme := s.DefaultScheme
	if !ValidScheme(scheme) {
		scheme = Schemes[0]
	}
	return Viewer{Perms: acl.Resolve(subject), Loc: loc, Scheme: scheme, DateFormat: "standard"}, nil
}

// Member returns the viewer for the active member userID: the grants of the
// member's own rows and groups (a member does not hold the grants of the guests
// group), the member's time zone and the member's color scheme. A zone or a
// scheme that the row holds and gobb does not know falls back to the board's.
// A user who does not exist, or is not active, gives [ErrNotFound].
func Member(ctx context.Context, q db.Querier, userID int64, s Settings) (Viewer, error) {
	var (
		name, zone, scheme, state, dateFormat string
		lastVisit, markFloor                  time.Time
		lastActive                            *time.Time
	)
	err := q.QueryRow(ctx, `
select username, timezone, color_scheme, state, date_format,
       coalesce(last_visit_at, registered_at), coalesce(last_mark_at, registered_at), last_active_at
  from users where id = $1`, userID).
		Scan(&name, &zone, &scheme, &state, &dateFormat, &lastVisit, &markFloor, &lastActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return Viewer{}, ErrNotFound
	}
	if err != nil {
		return Viewer{}, fmt.Errorf("board: load member %d: %w", userID, err)
	}
	if state != "active" {
		return Viewer{}, ErrNotFound
	}
	subject, err := acldb.Load(ctx, q, userID)
	if err != nil {
		if errors.Is(err, acldb.ErrUnknownUser) {
			return Viewer{}, ErrNotFound
		}
		return Viewer{}, err
	}
	loc, err := time.LoadLocation(zone)
	if err != nil {
		slog.Warn("board: a member has a time zone that gobb does not know; using the board zone", "user", userID, "zone", zone)
		if loc, err = time.LoadLocation(s.Timezone); err != nil {
			return Viewer{}, fmt.Errorf("board: setting \"timezone\": %w", err)
		}
	}
	if !ValidScheme(scheme) {
		scheme = s.DefaultScheme
		if !ValidScheme(scheme) {
			scheme = Schemes[0]
		}
	}
	v := Viewer{
		UserID: userID, Name: name, Perms: acl.Resolve(subject),
		Loc: loc, Scheme: scheme, DateFormat: dateFormat, LastVisit: lastVisit, MarkFloor: markFloor,
	}
	if lastActive != nil {
		v.LastActive = *lastActive
	}
	return v, nil
}

// SetScheme stores scheme as the color scheme of member userID. It reports
// false, and changes nothing, when no active member has that ID. A scheme that
// is not one of [Schemes] is an error.
func SetScheme(ctx context.Context, q db.Querier, userID int64, scheme string) (bool, error) {
	if !ValidScheme(scheme) {
		return false, fmt.Errorf("board: no color scheme %q", scheme)
	}
	tag, err := q.Exec(ctx, `update users set color_scheme = $2 where id = $1 and state = 'active'`, userID, scheme)
	if err != nil {
		return false, fmt.Errorf("board: set the scheme of %d: %w", userID, err)
	}
	return tag.RowsAffected() == 1, nil
}
