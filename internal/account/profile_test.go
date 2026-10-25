package account

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckProfileText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		max  int
		want error
	}{
		{"empty", "", 100, nil},
		{"plain", "Lake Placid, NY", 100, nil},
		{"exactly max", strings.Repeat("a", 100), 100, nil},
		{"one over", strings.Repeat("a", 101), 100, ErrTooLong},
		{"counts characters, not bytes", strings.Repeat("é", 100), 100, nil},
		{"counts characters over", strings.Repeat("é", 101), 100, ErrTooLong},
		{"line break", "a\nb", 100, ErrControl},
		{"carriage return", "a\rb", 100, ErrControl},
		{"tab", "a\tb", 100, ErrControl},
		{"nul", "a\x00b", 100, ErrControl},
		{"del", "a\x7fb", 100, ErrControl},
		{"c1 control", "a\u0085b", 100, ErrControl},
		{"invalid utf-8", "a\xffb", 100, ErrNotUTF8},
		{"control and too long: too long first", strings.Repeat("a", 5) + "\n", 3, ErrTooLong},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckProfileText(tc.in, tc.max); !errors.Is(got, tc.want) {
				t.Errorf("CheckProfileText(%q, %d) = %v, want %v", tc.in, tc.max, got, tc.want)
			}
		})
	}
}

func TestCheckWebsite(t *testing.T) {
	long := "https://example.com/" + strings.Repeat("a", 200-len("https://example.com/"))
	tests := []struct {
		name string
		in   string
		want error
	}{
		{"empty", "", nil},
		{"https", "https://example.com/alice", nil},
		{"http", "http://example.com", nil},
		{"scheme in upper case", "HTTPS://Example.com/a", nil},
		{"port", "https://example.com:8443/x", nil},
		{"query", "https://example.com/?a=b&c=d", nil},
		{"exactly 200 bytes", long, nil},
		{"201 bytes", long + "a", ErrTooLong},
		{"javascript", "javascript:alert(1)", ErrWebsite},
		{"ftp", "ftp://x.org", ErrWebsite},
		{"no scheme", "example.com", ErrWebsite},
		{"scheme relative", "//example.com", ErrWebsite},
		{"no host", "https://", ErrWebsite},
		{"port only", "https://:8080/", ErrWebsite},
		{"user info", "https://a:b@x.org", ErrWebsite},
		{"empty user info", "https://@x.org", ErrWebsite},
		{"space", "https://example.com/a b", ErrWebsite},
		{"leading space", " https://example.com", ErrWebsite},
		{"unicode space", "https://example.com/a b", ErrWebsite},
		{"line break", "https://example.com/a\nb", ErrWebsite},
		{"tab", "https://example.com/\ta", ErrWebsite},
		{"nul", "https://example.com/\x00", ErrWebsite},
		{"invalid utf-8", "https://example.com/\xff", ErrNotUTF8},
		{"bad percent escape", "https://example.com/%zz", ErrWebsite},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := CheckWebsite(tc.in); !errors.Is(got, tc.want) {
				t.Errorf("CheckWebsite(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

func TestCheckBirthday(t *testing.T) {
	tests := []struct {
		name             string
		day, month, year int
		latest           int
		want             error
	}{
		{"unset", 0, 0, 0, 2007, nil},
		{"day and month", 2, 7, 0, 2007, nil},
		{"full date", 14, 3, 1984, 2007, nil},
		{"day alone", 14, 0, 0, 2007, ErrBirthdayPair},
		{"month alone", 0, 3, 0, 2007, ErrBirthdayPair},
		{"year alone", 0, 0, 1984, 2007, ErrYearAlone},
		{"year with day only", 14, 0, 1984, 2007, ErrBirthdayPair},
		{"month 13", 1, 13, 0, 2007, ErrNoSuchDate},
		{"negative month", 1, -1, 0, 2007, ErrNoSuchDate},
		{"day 32", 32, 1, 0, 2007, ErrNoSuchDate},
		{"negative day", -1, 1, 0, 2007, ErrNoSuchDate},
		{"31 April", 31, 4, 0, 2007, ErrNoSuchDate},
		{"31 January", 31, 1, 0, 2007, nil},
		{"29 February, no year", 29, 2, 0, 2007, nil},
		{"29 February 2000", 29, 2, 2000, 2007, nil},
		{"29 February 2001", 29, 2, 2001, 2007, ErrNoSuchDate},
		{"29 February 1900", 29, 2, 1900, 2007, ErrNoSuchDate},
		{"30 February, no year", 30, 2, 0, 2007, ErrNoSuchDate},
		{"year 1900", 1, 1, 1900, 2007, nil},
		{"year 1899", 31, 12, 1899, 2007, ErrYearTooEarly},
		{"negative year", 1, 1, -5, 2007, ErrYearTooEarly},
		{"latest year", 1, 1, 2007, 2007, nil},
		{"after latest year", 1, 1, 2008, 2007, ErrYearTooLate},
		{"no upper bound when latestYear is 0", 1, 1, 2999, 0, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := CheckBirthday(tc.day, tc.month, tc.year, tc.latest)
			if !errors.Is(got, tc.want) {
				t.Errorf("CheckBirthday(%d, %d, %d, %d) = %v, want %v",
					tc.day, tc.month, tc.year, tc.latest, got, tc.want)
			}
		})
	}
}
