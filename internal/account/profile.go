package account

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The faults that the profile checks return. A caller tests them with errors.Is
// and maps each to its own message.
var (
	// ErrNotUTF8 means the text is not valid UTF-8.
	ErrNotUTF8 = errors.New("account: not valid UTF-8")
	// ErrTooLong means the text is longer than the limit.
	ErrTooLong = errors.New("account: too long")
	// ErrControl means the text holds a control character. A line break is one.
	// A format character (Unicode category Cf, such as the bidirectional controls,
	// the zero-width space and the byte order mark) and the line and paragraph
	// separators count too, because they hide or reorder text.
	ErrControl = errors.New("account: holds a control character")
	// ErrWebsite means the website is not an http or https URL with a host, or it
	// holds user information, white space or a control character.
	ErrWebsite = errors.New("account: not a website URL")
	// ErrBirthdayPair means only one of the day and the month is set.
	ErrBirthdayPair = errors.New("account: birthday needs a day and a month together")
	// ErrYearAlone means the year is set but the day and month are not.
	ErrYearAlone = errors.New("account: birth year needs a day and a month")
	// ErrNoSuchDate means the date does not exist, for example 31 April or 29
	// February in a year that is not a leap year.
	ErrNoSuchDate = errors.New("account: no such date")
	// ErrYearTooEarly means the birth year is before [MinBirthYear].
	ErrYearTooEarly = errors.New("account: birth year is too early")
	// ErrYearTooLate means the birth year is after the latest year that the caller
	// allows.
	ErrYearTooLate = errors.New("account: birth year is too late")
)

// The limits of the "About you" fields.
const (
	// MaxLocation is the longest location, in characters.
	MaxLocation = 100
	// MaxOccupation is the longest occupation, in characters.
	MaxOccupation = 100
	// MaxInterests is the longest interests text, in characters.
	MaxInterests = 200
	// MaxWebsite is the longest website, in bytes.
	MaxWebsite = 200
	// MinBirthYear is the earliest birth year.
	MinBirthYear = 1900
	// MinAge is the youngest age. The latest birth year is the current year minus
	// MinAge.
	MinAge = 19
)

// CheckProfileText checks a plain-text profile field: valid UTF-8, at most limit
// characters (runes, not bytes), and no control character, format character or
// line or paragraph separator. A line break, a tab and U+0000 are control
// characters. It returns nil, [ErrNotUTF8], [ErrTooLong]
// or [ErrControl]. It does not trim s: a caller that trims does so first.
func CheckProfileText(s string, limit int) error {
	switch {
	case !utf8.ValidString(s):
		return ErrNotUTF8
	case utf8.RuneCountInString(s) > limit:
		return ErrTooLong
	case strings.IndexFunc(s, hidden) >= 0:
		return ErrControl
	}
	return nil
}

// hidden reports whether r is a control character, a format character (Unicode
// category Cf) or a line or paragraph separator (Zl, Zp).
func hidden(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Cf, unicode.Zl, unicode.Zp)
}

// CheckWebsite checks a website field. An empty string passes. Any other value
// must meet all of these:
//
//   - it is valid UTF-8, and at most [MaxWebsite] bytes;
//   - it holds no white space, no control character, no format character and no
//     line or paragraph separator, in the text or after the percent-decoding of the
//     host and the path;
//   - its scheme is http or https, in any case;
//   - it has a host;
//   - it has no user information.
//
// It returns nil, [ErrNotUTF8], [ErrTooLong] or [ErrWebsite].
func CheckWebsite(s string) error {
	if s == "" {
		return nil
	}
	switch {
	case !utf8.ValidString(s):
		return ErrNotUTF8
	case len(s) > MaxWebsite:
		return ErrTooLong
	case strings.IndexFunc(s, func(r rune) bool { return unicode.IsSpace(r) || hidden(r) }) >= 0:
		return ErrWebsite
	}
	u, err := url.Parse(s)
	if err != nil {
		return ErrWebsite
	}
	if scheme := strings.ToLower(u.Scheme); scheme != "http" && scheme != "https" {
		return ErrWebsite
	}
	if u.Hostname() == "" || u.User != nil {
		return ErrWebsite
	}
	// Percent-encoding would hide a character that the raw string check refuses.
	// url.Parse decodes the path; the host is decoded here.
	host, err := url.PathUnescape(u.Host)
	if err != nil || strings.IndexFunc(host+u.Path, hidden) >= 0 {
		return ErrWebsite
	}
	return nil
}

// CheckBirthday checks a birthday. A value of 0 means unset. The day and the month
// are both set or both unset, and the year may be set only when both are. The date
// must exist: 29 February needs an unset year or a leap year. A set year must be
// at least [MinBirthYear], and at most latestYear. A latestYear of 0 skips the
// upper bound, for a caller that cannot know the current year.
//
// It returns nil, [ErrBirthdayPair], [ErrYearAlone], [ErrNoSuchDate],
// [ErrYearTooEarly] or [ErrYearTooLate].
func CheckBirthday(day, month, year, latestYear int) error {
	switch {
	case (day == 0) != (month == 0):
		return ErrBirthdayPair
	case day == 0 && year != 0:
		return ErrYearAlone
	case day == 0:
		return nil
	}
	if year != 0 {
		switch {
		case year < MinBirthYear:
			return ErrYearTooEarly
		case latestYear != 0 && year > latestYear:
			return ErrYearTooLate
		}
	}
	// With no year, test the date in a leap year, so that 29 February passes.
	y := year
	if y == 0 {
		y = 2000
	}
	if month < 1 || month > 12 || day < 1 {
		return ErrNoSuchDate
	}
	if d := time.Date(y, time.Month(month), day, 0, 0, 0, 0, time.UTC); d.Day() != day {
		return ErrNoSuchDate
	}
	return nil
}
