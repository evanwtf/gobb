// Package account holds the account helpers that the importer and, later, login share.
package account

import (
	"strings"
	"unicode"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// invisible lists the tables of the runes that CleanUsername removes.
var invisible = []*unicode.RangeTable{
	unicode.Cf,
	unicode.Variation_Selector,
	unicode.Other_Default_Ignorable_Code_Point,
}

// CleanUsername returns the comparison form of a username. It applies NFKC, Unicode
// case folding, lowercasing and NFKC again. Then it removes the invisible runes. These
// are the runes in the category Cf, the runes with the Variation_Selector property, and
// the runes with the Other_Default_Ignorable_Code_Point property. Last, it trims the
// Unicode spaces at both ends. Two names match when their forms are equal.
//
// The caller must refuse invalid UTF-8 before it calls CleanUsername. The caller must
// also refuse an empty result. The result is empty when the input holds only spaces
// and invisible runes.
func CleanUsername(s string) string {
	s = norm.NFKC.String(s)
	s = cases.Fold().String(s)
	s = strings.ToLower(s)
	s = norm.NFKC.String(s)
	s = strings.Map(dropInvisible, s)
	return strings.TrimSpace(s)
}

// dropInvisible returns -1 for an invisible rune, so that strings.Map removes it. It
// returns every other rune unchanged.
func dropInvisible(r rune) rune {
	if unicode.In(r, invisible...) {
		return -1
	}
	return r
}

// CleanEmail returns the comparison form of an email address. It trims the spaces at
// both ends, then lowercases the result.
func CleanEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
