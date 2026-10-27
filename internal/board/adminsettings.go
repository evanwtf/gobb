package board

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/tz"
)

// The writes of the settings page of the admin area. Each one goes through
// [AdminWrite], so it writes its mod_log row in its own transaction.

// The longest board name and board description, in characters.
const (
	MaxBoardName        = 100
	MaxBoardDescription = 500
)

// The messages of the board settings form. Each one names the problem, and none
// holds text from the request.
const (
	msgNameEmpty   = "Enter a name of 1 to 100 characters."
	msgNameLong    = "Use at most 100 characters."
	msgDescLong    = "Use at most 500 characters."
	msgSchemeFault = "Pick one of the color schemes."
)

// The keys of the table settings, as the details of a mod_log row names them.
const (
	keyName        = "board_name"
	keyDescription = "board_description"
	keyTimezone    = "timezone"
	keyScheme      = "default_color_scheme"
	keyHeader      = "header_image"
)

// SettingsChange is the board settings form: the new value of each setting that
// the form holds.
type SettingsChange struct {
	Name, Description, Timezone, Scheme string
}

// UpdateSettings stores the values of c as the board name, the board
// description, the board's time zone and the default color scheme. It runs for
// an admin v through [AdminWrite] and returns the names of the keys that
// changed, in this order: board_name, board_description, timezone,
// default_color_scheme. The mod_log row is {action "settings.update", target
// type "settings", target ID 0} with those names as details.keys.
//
//   - Name and description are trimmed. The name has 1 to [MaxBoardName]
//     characters and the description up to [MaxBoardDescription]. Both are one
//     line of valid UTF-8 with no control character and no hidden formatting
//     character ([account.CheckProfileText]).
//   - The time zone is in [tz.Zones]. The zone that the board already has is also
//     accepted, so that a board with an odd imported zone can save its name.
//   - The scheme is one of [Schemes].
//
// Every field that fails gives an [*InputError] on its name ("name",
// "description", "timezone" or "scheme"). One field comes back as it is, and
// several as [errors.Join] of them in that order. A save that changes nothing
// gives [ErrNoChange], writes no row and changes nothing. A viewer who is not an
// admin gets [ErrForbidden].
func UpdateSettings(ctx context.Context, pool *pgxpool.Pool, v Viewer, c SettingsChange, now time.Time) ([]string, error) {
	var changed []string
	err := AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		cur, err := lockSettings(ctx, tx)
		if err != nil {
			return Change{}, err
		}
		c.Name, c.Description = strings.TrimSpace(c.Name), strings.TrimSpace(c.Description)
		if err := checkSettings(c, cur); err != nil {
			return Change{}, err
		}
		for _, f := range []struct {
			key      string
			old, new string
		}{
			{keyName, cur.BoardName, c.Name},
			{keyDescription, cur.BoardDescription, c.Description},
			{keyTimezone, cur.Timezone, c.Timezone},
			{keyScheme, cur.DefaultScheme, c.Scheme},
		} {
			if f.old == f.new {
				continue
			}
			if err := storeSetting(ctx, tx, f.key, f.new); err != nil {
				return Change{}, err
			}
			changed = append(changed, f.key)
		}
		if len(changed) == 0 {
			return Change{}, ErrNoChange
		}
		return settingsChange(changed), nil
	})
	if err != nil {
		return nil, err
	}
	return changed, nil
}

// checkSettings checks c, whose name and description are trimmed, against the
// current settings cur. It returns the [*InputError]s as UpdateSettings says.
func checkSettings(c SettingsChange, cur Settings) error {
	var errs []error
	switch err := account.CheckProfileText(c.Name, MaxBoardName); {
	case c.Name == "":
		errs = append(errs, inputError("name", msgNameEmpty))
	case err == nil:
	case errors.Is(err, account.ErrTooLong):
		errs = append(errs, inputError("name", msgNameLong))
	case errors.Is(err, account.ErrFormat):
		errs = append(errs, inputError("name", msgHidden))
	default: // not UTF-8, control character
		errs = append(errs, inputError("name", msgOneLine))
	}
	switch err := account.CheckProfileText(c.Description, MaxBoardDescription); {
	case err == nil:
	case errors.Is(err, account.ErrTooLong):
		errs = append(errs, inputError("description", msgDescLong))
	case errors.Is(err, account.ErrFormat):
		errs = append(errs, inputError("description", msgHidden))
	default:
		errs = append(errs, inputError("description", msgOneLine))
	}
	if c.Timezone != cur.Timezone && !tz.Known(c.Timezone) {
		errs = append(errs, inputError("timezone", msgZone))
	}
	if !ValidScheme(c.Scheme) {
		errs = append(errs, inputError("scheme", msgSchemeFault))
	}
	if len(errs) == 1 {
		return errs[0]
	}
	return errors.Join(errs...)
}

// lockSettings locks the rows of the board settings until the end of tx and
// returns them, so that two saves run one after the other and each compares
// against the values the other left.
func lockSettings(ctx context.Context, tx pgx.Tx) (Settings, error) {
	if _, err := tx.Exec(ctx, `select 1 from settings where key = any($1) order by key for update`, settingKeys); err != nil {
		return Settings{}, fmt.Errorf("board: lock the settings: %w", err)
	}
	return LoadSettings(ctx, tx)
}

// storeSetting stores value, a string or nil (JSON null), as the value of key.
func storeSetting(ctx context.Context, tx pgx.Tx, key string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return fmt.Errorf("board: encode setting %s: %w", key, err)
	}
	tag, err := tx.Exec(ctx, `update settings set value = $2::jsonb where key = $1`, key, string(b))
	if err != nil {
		return fmt.Errorf("board: store setting %s: %w", key, err)
	}
	if tag.RowsAffected() != 1 {
		return fmt.Errorf("board: setting %q is missing", key)
	}
	return nil
}

// settingsChange is the [Change] of a write that changed the setting keys.
func settingsChange(keys []string) Change {
	return Change{Action: "settings.update", TargetType: "settings", Details: map[string]any{"keys": keys}}
}

// headerPathRE matches a header image path that gobb makes:
// "header/h-{16 lowercase hex}.{png|jpg|webp}".
var headerPathRE = regexp.MustCompile(`^header/h-[0-9a-f]{16}\.(png|jpg|webp)$`)

// ValidHeaderPath reports whether p is a stored header image path that gobb may
// delete: it is under "header/", every element is non-empty and made of
// letters, digits, hyphens, dots and underscores, and no element starts with a
// dot. An imported header image keeps the name of the converter, so this is the
// rule for a path read from the database; [SetHeaderImage] asks for more of a
// new path. A caller deletes a file only when this returns true.
func ValidHeaderPath(p string) bool { return validDataPath("header/", p) }

// SetHeaderImage stores path as the header image of the board and returns the
// path it replaced ("" when there was none). It runs for an admin v through
// [AdminWrite]: the mod_log row is "settings.update" with details.keys
// ["header_image"]. path is a site path that gobb made, "header/h-{16 hex}.{png,
// jpg or webp}"; any other value is an error of the caller. The caller deletes
// the returned file after the commit, so that a failed write never loses the
// current image. A viewer who is not an admin gets [ErrForbidden].
func SetHeaderImage(ctx context.Context, pool *pgxpool.Pool, v Viewer, path string, now time.Time) (old string, err error) {
	if !headerPathRE.MatchString(path) {
		return "", fmt.Errorf("board: %q is not a header image path", path)
	}
	return swapHeader(ctx, pool, v, path, now)
}

// RemoveHeaderImage clears the header image of the board and returns the path it
// removed. It runs for an admin v through [AdminWrite], like [SetHeaderImage]. A
// board with no header image gives [ErrNoChange].
func RemoveHeaderImage(ctx context.Context, pool *pgxpool.Pool, v Viewer, now time.Time) (old string, err error) {
	return swapHeader(ctx, pool, v, "", now)
}

// swapHeader stores path (JSON null when empty) as the header image under the
// lock of the settings and returns the previous path.
func swapHeader(ctx context.Context, pool *pgxpool.Pool, v Viewer, path string, now time.Time) (string, error) {
	var old string
	err := AdminWrite(ctx, pool, v, now, func(tx pgx.Tx) (Change, error) {
		cur, err := lockSettings(ctx, tx)
		if err != nil {
			return Change{}, err
		}
		if cur.HeaderImage == "" && path == "" {
			return Change{}, ErrNoChange
		}
		var value any
		if path != "" {
			value = path
		}
		if err := storeSetting(ctx, tx, keyHeader, value); err != nil {
			return Change{}, err
		}
		old = cur.HeaderImage
		return settingsChange([]string{keyHeader}), nil
	})
	if err != nil {
		return "", err
	}
	return old, nil
}
