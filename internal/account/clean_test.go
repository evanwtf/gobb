package account

import (
	"testing"
	"unicode"
)

func TestCleanUsername(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"capitalized", "Bob", "bob"},
		{"lowercase", "bob", "bob"},
		{"spaces around capitals", " BOB ", "bob"},
		{"fullwidth letters", "Ｂｏｂ", "bob"},
		// The ideographic space is a Unicode space. NFKC makes it a plain space,
		// and the trim removes it.
		{"ideographic spaces", "\u3000BOB\u3000", "bob"},
		{"sharp s", "Straße", "strasse"},
		{"capitals with sharp s", "STRASSE", "strasse"},
		// Invisible runes are removed wherever they stand.
		{"zero-width space", "b\u200bob", "bob"},
		{"soft hyphen", "bo\u00adb", "bob"},
		{"byte order mark", "\ufeffbob", "bob"},
		{"zero-width joiner", "bo\u200db", "bob"},
		{"combining grapheme joiner", "bo\u034fb", "bob"},
		// A caller refuses an empty result. These inputs give one.
		{"only invisible runes", "\u200b\u00ad", ""},
		{"only spaces", " \t ", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanUsername(tc.in); got != tc.want {
				t.Errorf("CleanUsername(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// Each pair must clean to the same form.
func TestCleanUsernameSameForm(t *testing.T) {
	pairs := []struct {
		name string
		a, b string
	}{
		// Cherokee capital letters and their small letters.
		{"Cherokee capital and small letters", "\u13a0\u13a1", "\uab70\uab71"},
		// Sharp s with a combining acute accent, and s with a precomposed acute accent.
		{"sharp s with a combining accent", "ß\u0301", "s\u015b"},
	}
	for _, tc := range pairs {
		t.Run(tc.name, func(t *testing.T) {
			a, b := CleanUsername(tc.a), CleanUsername(tc.b)
			if a != b {
				t.Errorf("CleanUsername(%q) = %q, CleanUsername(%q) = %q; want equal", tc.a, a, tc.b, b)
			}
		})
	}
}

// The output must be stable: cleaning it again changes nothing. Each case variant of a
// code point must clean to the same output. The loop covers every code point from U+0000
// to U+FFFF, except the surrogates.
func TestCleanUsernameIsStable(t *testing.T) {
	failures := 0
	for r := rune(0); r <= 0xFFFF; r++ {
		if 0xD800 <= r && r <= 0xDFFF {
			continue
		}
		want := CleanUsername(string(r))
		if got := CleanUsername(want); got != want {
			t.Errorf("U+%04X: output %q is not stable: cleans to %q", r, want, got)
			failures++
		}
		for v := unicode.SimpleFold(r); v != r; v = unicode.SimpleFold(v) {
			if got := CleanUsername(string(v)); got != want {
				t.Errorf("U+%04X: variant U+%04X cleans to %q, want %q", r, v, got, want)
				failures++
			}
		}
		if failures >= 20 {
			t.Fatal("stopping after 20 failures")
		}
	}
}

func TestCleanEmail(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"spaces around capitals", " A@Example.COM ", "a@example.com"},
		{"tab and newline", "\tB@EXAMPLE.org\n", "b@example.org"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanEmail(tc.in); got != tc.want {
				t.Errorf("CleanEmail(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
