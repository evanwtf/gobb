// Package password prepares passwords for hashing and checking.
package password

import (
	"strings"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// lineFixer turns CR LF and a lone CR into LF. It removes NUL and U+FEFF.
// CR LF is listed first, so it wins over a lone CR.
var lineFixer = strings.NewReplacer(
	"\r\n", "\n",
	"\r", "\n",
	"\x00", "",
	"\uFEFF", "",
)

// htmlEscaper escapes &, ", < and >. It leaves single quotes as they are.
var htmlEscaper = strings.NewReplacer(
	"&", "&amp;",
	`"`, "&quot;",
	"<", "&lt;",
	">", "&gt;",
)

// LegacyForm returns the legacy form of raw, as spec 10 steps 1 to 4 define it.
// The steps run in this order: line endings and removals, HTML escape, NFC, trim.
// It returns false when raw is not valid UTF-8, because such a password has no legacy form.
func LegacyForm(raw string) (string, bool) {
	if !utf8.ValidString(raw) {
		return "", false
	}
	s := lineFixer.Replace(raw)
	s = htmlEscaper.Replace(s)
	s = norm.NFC.String(s)
	return strings.Trim(s, " \t\n\r\x00\x0B"), true
}
