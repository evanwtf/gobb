package board

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/evanwtf/gobb/internal/account"
	"github.com/evanwtf/gobb/internal/tz"
)

// The messages of the settings forms. Each one names the problem, and none holds
// text from the request.
const (
	msgZone       = "Pick a time zone from the list."
	msgDateFormat = "Pick one of the three formats."
	msgLong100    = "Use at most 100 characters."
	msgLong200    = "Use at most 200 characters."
	msgOneLine    = "Use one line of plain text."
	msgWebsite    = "Enter an address that starts with https:// or http://."
	msgWebLong    = "Use an address of at most 200 characters."
	msgBdayPair   = "Pick both a day and a month, or neither."
	msgBdayYear   = "A year needs a day and a month."
	msgBdayEarly  = "The earliest year you can pick is 1900."
)

// SetTimezone stores zone as the time zone of member v. A guest gets
// [ErrForbidden]. The zone must be in [tz.Zones]; any other value, a fixed offset
// and "UTC" included, is an [*InputError] on the field "timezone".
func SetTimezone(ctx context.Context, pool *pgxpool.Pool, v Viewer, zone string) error {
	if v.UserID == 0 {
		return ErrForbidden
	}
	if !tz.Known(zone) {
		return inputError("timezone", msgZone)
	}
	return updateOwnRow(ctx, pool, v, `update users set timezone = $2 where id = $1 and state = 'active'`, zone)
}

// dateFormats are the values that the column date_format accepts.
var dateFormats = map[string]bool{"standard": true, "relative": true, "iso": true}

// SetDateFormat stores format as the date format of member v. A guest gets
// [ErrForbidden]. The value is "standard", "relative" or "iso"; any other value is
// an [*InputError] on the field "dateformat".
func SetDateFormat(ctx context.Context, pool *pgxpool.Pool, v Viewer, format string) error {
	if v.UserID == 0 {
		return ErrForbidden
	}
	if !dateFormats[format] {
		return inputError("dateformat", msgDateFormat)
	}
	return updateOwnRow(ctx, pool, v, `update users set date_format = $2 where id = $1 and state = 'active'`, format)
}

// updateOwnRow runs a one-row update of the own row of v and gives [ErrNotFound]
// when no active row matched.
func updateOwnRow(ctx context.Context, pool *pgxpool.Pool, v Viewer, query string, args ...any) error {
	tag, err := pool.Exec(ctx, query, append([]any{v.UserID}, args...)...)
	if err != nil {
		return fmt.Errorf("board: update settings of %d: %w", v.UserID, err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// AboutChange is a change to the "About you" fields. A nil field stays as it is.
// A field that is set is trimmed, and an empty result clears it.
type AboutChange struct {
	Location, Occupation, Interests, Website *string
}

// SetAbout stores the fields of c that are set, for member v. It needs
// account.profile. Each set field is trimmed and checked by [account.CheckProfileText]
// (or [account.CheckWebsite]). The first field that fails gives an [*InputError]
// on its name ("location", "occupation", "interests" or "website"), and nothing
// is stored.
func SetAbout(ctx context.Context, pool *pgxpool.Pool, v Viewer, c AboutChange) error {
	if err := mayAccount(v, "account.profile"); err != nil {
		return err
	}
	type field struct {
		name, column string
		in           *string
		limit        int
		msgLong      string
	}
	fields := []field{
		{"location", "location", c.Location, account.MaxLocation, msgLong100},
		{"occupation", "occupation", c.Occupation, account.MaxOccupation, msgLong100},
		{"interests", "interests", c.Interests, account.MaxInterests, msgLong200},
		{"website", "website", c.Website, 0, ""},
	}
	var sets []string
	args := []any{v.UserID}
	for _, f := range fields {
		if f.in == nil {
			continue
		}
		val := strings.TrimSpace(*f.in)
		var err error
		if f.name == "website" {
			err = account.CheckWebsite(val)
		} else {
			err = account.CheckProfileText(val, f.limit)
		}
		switch {
		case err == nil:
		case errors.Is(err, account.ErrTooLong):
			if f.name == "website" {
				return inputError(f.name, msgWebLong)
			}
			return inputError(f.name, f.msgLong)
		case errors.Is(err, account.ErrWebsite) || f.name == "website":
			return inputError(f.name, msgWebsite)
		default: // not UTF-8, control character
			return inputError(f.name, msgOneLine)
		}
		args = append(args, val)
		sets = append(sets, fmt.Sprintf("%s = $%d", f.column, len(args)))
	}
	if len(sets) == 0 {
		return nil
	}
	return updateOwnRow(ctx, pool, v, `update users set `+strings.Join(sets, ", ")+` where id = $1 and state = 'active'`, args[1:]...)
}

// Birthday is a birthday. 0 means unset in each field.
type Birthday struct {
	Day, Month, Year int
}

// SetBirthday stores b as the birthday of member v. It needs account.profile. The
// zero Birthday clears it. The latest year is now.Year() - [account.MinAge]. A
// refusal is an [*InputError] on the field "birthday".
func SetBirthday(ctx context.Context, pool *pgxpool.Pool, v Viewer, b Birthday, now time.Time) error {
	if err := mayAccount(v, "account.profile"); err != nil {
		return err
	}
	latest := now.Year() - account.MinAge
	switch err := account.CheckBirthday(b.Day, b.Month, b.Year, latest); {
	case err == nil:
	case errors.Is(err, account.ErrBirthdayPair):
		return inputError("birthday", msgBdayPair)
	case errors.Is(err, account.ErrYearAlone):
		return inputError("birthday", msgBdayYear)
	case errors.Is(err, account.ErrYearTooEarly):
		return inputError("birthday", msgBdayEarly)
	case errors.Is(err, account.ErrYearTooLate):
		return inputError("birthday", fmt.Sprintf("The latest year you can pick is %d.", latest))
	case errors.Is(err, account.ErrNoSuchDate):
		return inputError("birthday", noSuchDate(b))
	default:
		return fmt.Errorf("board: check a birthday: %w", err)
	}
	return updateOwnRow(ctx, pool, v,
		`update users set birth_day = $2, birth_month = $3, birth_year = $4 where id = $1 and state = 'active'`,
		nullZero(b.Day), nullZero(b.Month), nullZero(b.Year))
}

// noSuchDate words the refusal of a date that does not exist. The month is in
// English. For 29 February of a year that is not a leap year, the message names
// the year. b.Month is in 1..12 when the day is within the month names; a month
// outside that range cannot come from the form, and the message then names no
// month.
func noSuchDate(b Birthday) string {
	if b.Month == 2 && b.Day == 29 && b.Year != 0 {
		return fmt.Sprintf("February %d has no day 29.", b.Year)
	}
	if b.Month < 1 || b.Month > 12 {
		return "Pick a month from the list."
	}
	return fmt.Sprintf("%s has no day %d.", time.Month(b.Month), b.Day)
}

// nullZero returns nil for 0, so that the column holds NULL for an unset value.
func nullZero(n int) any {
	if n == 0 {
		return nil
	}
	return n
}

// validAvatarPath reports whether p is a path that gobb makes: "avatars/" and a
// file name of letters, digits, hyphens, dots and underscores, with no ".." and
// no separator after the folder.
func validAvatarPath(p string) bool {
	name, ok := strings.CutPrefix(p, "avatars/")
	if !ok || name == "" || strings.Contains(name, "..") {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_':
		default:
			return false
		}
	}
	return true
}

// SetAvatar stores path as the avatar of member v and returns the path it
// replaced ("" when there was none). It needs account.avatar. path is a site path
// that gobb made ("avatars/..."); any other value is an error of the caller. The
// caller deletes the old file after the commit, so that a failed write never
// loses the current avatar.
func SetAvatar(ctx context.Context, pool *pgxpool.Pool, v Viewer, path string) (old string, err error) {
	if err := mayAccount(v, "account.avatar"); err != nil {
		return "", err
	}
	if !validAvatarPath(path) {
		return "", fmt.Errorf("board: %q is not an avatar path", path)
	}
	return swapAvatar(ctx, pool, v, path)
}

// RemoveAvatar clears the avatar of member v and returns the path it removed ("" when
// there was none). It needs account.avatar. The caller deletes the file after the
// commit.
func RemoveAvatar(ctx context.Context, pool *pgxpool.Pool, v Viewer) (old string, err error) {
	if err := mayAccount(v, "account.avatar"); err != nil {
		return "", err
	}
	return swapAvatar(ctx, pool, v, "")
}

// swapAvatar stores path (NULL when empty) and returns the previous value. It
// reads the old value under a row lock, so that two writes in parallel each
// return a different old path and no file is left behind.
func swapAvatar(ctx context.Context, pool *pgxpool.Pool, v Viewer, path string) (string, error) {
	var old string
	err := inTx(ctx, pool, func(tx pgx.Tx) error {
		var prev *string
		err := tx.QueryRow(ctx, `select avatar_path from users where id = $1 and state = 'active' for no key update`, v.UserID).Scan(&prev)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return fmt.Errorf("board: read the avatar of %d: %w", v.UserID, err)
		}
		var next any
		if path != "" {
			next = path
		}
		if _, err := tx.Exec(ctx, `update users set avatar_path = $2 where id = $1`, v.UserID, next); err != nil {
			return fmt.Errorf("board: store the avatar of %d: %w", v.UserID, err)
		}
		if prev != nil {
			old = *prev
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	return old, nil
}
