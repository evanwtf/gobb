// Package account holds the account helpers that the importer and, later, login share.
package account

import (
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

// CleanUsername returns the comparison form of a username. It applies NFKC, then
// Unicode case folding, then trims the Unicode spaces at both ends. Two names match
// when their forms are equal.
func CleanUsername(s string) string {
	return strings.TrimSpace(cases.Fold().String(norm.NFKC.String(s)))
}

// CleanEmail returns the comparison form of an email address. It trims the spaces at
// both ends, then lowercases the result.
func CleanEmail(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}
