package account

import (
	"errors"
	"fmt"
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
	// The line and paragraph separators (Unicode categories Zl and Zp) count too.
	ErrControl = errors.New("account: holds a control character")
	// ErrFormat means the text holds a hidden formatting character: one of
	// Unicode category Cf, such as the bidirectional controls, the zero-width
	// space and the byte order mark, because it hides or reorders text. The
	// zero width joiner, the zero width non-joiner and the tag characters
	// U+E0020 to U+E007F are not hidden in this sense. They are part of emoji
	// sequences, subdivision flags and Persian words, and they pass.
	ErrFormat = errors.New("account: holds a hidden formatting character")
	// ErrWebsite means the website is not an http or https URL with a host, or it
	// holds user information, white space or a control character. A website with a
	// hidden formatting character is also [ErrFormat].
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
// characters (runes, not bytes), and no control character, hidden formatting
// character or line or paragraph separator. A line break, a tab and U+0000 are
// control characters. The zero width joiner, the zero width non-joiner and the
// tag characters U+E0020 to U+E007F are allowed (ruling R40). It returns nil,
// [ErrNotUTF8], [ErrTooLong], [ErrControl] or [ErrFormat]. It does not trim s: a
// caller that trims does so first.
func CheckProfileText(s string, limit int) error {
	switch {
	case !utf8.ValidString(s):
		return ErrNotUTF8
	case utf8.RuneCountInString(s) > limit:
		return ErrTooLong
	}
	return hiddenFault(s)
}

// HiddenFormat reports whether r is a hidden formatting character: one of Unicode
// category Cf that [ErrFormat] describes. The zero width joiner, the zero width
// non-joiner and the tag characters U+E0020 to U+E007F are not (ruling R40). This
// is the one rule for it: the profile checks, the checks of titles and subjects,
// and the bundle checks all call it.
func HiddenFormat(r rune) bool {
	switch {
	case r == '\u200c' || r == '\u200d', r >= 0xE0020 && r <= 0xE007F:
		return false
	}
	return unicode.Is(unicode.Cf, r)
}

// HasHiddenFormat reports whether s holds a hidden formatting character (see
// [HiddenFormat]).
func HasHiddenFormat(s string) bool { return strings.IndexFunc(s, HiddenFormat) >= 0 }

// isControl reports whether r is a control character or a line or paragraph
// separator (Zl, Zp).
func isControl(r rune) bool {
	return unicode.IsControl(r) || unicode.In(r, unicode.Zl, unicode.Zp)
}

// hiddenFault returns [ErrControl] or [ErrFormat] for the first character of s
// that is a control character or a hidden formatting character, and nil when s
// holds none.
func hiddenFault(s string) error {
	for _, r := range s {
		switch {
		case isControl(r):
			return ErrControl
		case HiddenFormat(r):
			return ErrFormat
		}
	}
	return nil
}

// CheckWebsite checks a website field. An empty string passes. Any other value
// must meet all of these:
//
//   - it is valid UTF-8, and at most [MaxWebsite] bytes;
//   - it holds no white space, no control character, no hidden formatting
//     character and no line or paragraph separator, in the text or after the
//     percent-decoding of the host, the path, the query and the fragment;
//   - its scheme is http or https, in any case;
//   - it has a host;
//   - it has no user information.
//
// It returns nil, [ErrNotUTF8], [ErrTooLong] or [ErrWebsite]. When a hidden
// formatting character is the cause, the error is also [ErrFormat]. The zero width
// joiner, the zero width non-joiner and the tag characters pass (ruling R40).
func CheckWebsite(s string) error {
	if s == "" {
		return nil
	}
	switch {
	case !utf8.ValidString(s):
		return ErrNotUTF8
	case len(s) > MaxWebsite:
		return ErrTooLong
	case strings.IndexFunc(s, unicode.IsSpace) >= 0:
		return ErrWebsite
	}
	if err := hiddenFault(s); err != nil {
		return websiteFault(err)
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
	// url.Parse has decoded the host, the path and the fragment. The query stays
	// raw, so decode it here. A query with a bad escape is refused, because the
	// escape could hide a character.
	query, err := url.QueryUnescape(u.RawQuery)
	if err != nil {
		return ErrWebsite
	}
	if err := hiddenFault(u.Host + u.Path + query + u.Fragment); err != nil {
		return websiteFault(err)
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

// websiteFault returns [ErrWebsite] for a website whose text holds a control or a
// hidden formatting character. For [ErrFormat], the result is also [ErrFormat].
func websiteFault(cause error) error {
	if errors.Is(cause, ErrFormat) {
		return fmt.Errorf("%w: %w", ErrWebsite, ErrFormat)
	}
	return ErrWebsite
}
