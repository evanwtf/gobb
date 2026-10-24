package account

import (
	"fmt"
	"math/rand/v2"
	"strings"
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

// Removing an invisible rune can join a base letter to a combining mark. The second NFKC
// after the removal composes the pair, so a second clean changes nothing.
func TestCleanUsernameJoinsAfterRemoval(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"zero-width joiner between e and an accent", "e\u200d\u0301", "\u00e9"},
		{"zero-width space between a and a diaeresis", "a\u200b\u0308", "\u00e4"},
		{"zero-width space between Hangul jamo", "\u1100\u200b\u1161", "\uac00"},
		{"sharp s, filler, joiner and accent", "\u1e9e\u3164\u034f\u0301", "s\u015b"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			once := CleanUsername(tc.in)
			if once != tc.want {
				t.Errorf("CleanUsername(%s) = %s, want %s", codePoints(tc.in), codePoints(once), codePoints(tc.want))
			}
			if twice := CleanUsername(once); twice != once {
				t.Errorf("a second clean gives %s, want %s", codePoints(twice), codePoints(once))
			}
		})
	}
	// The first example must clean to the same form as the precomposed letter.
	if got, want := CleanUsername("e\u200d\u0301"), CleanUsername("\u00e9"); got != want {
		t.Errorf("CleanUsername(e, joiner, accent) = %s, want %s", codePoints(got), codePoints(want))
	}
}

// sweepPool lists the runes that the seeded sweep draws from. It mixes letters, combining
// marks, Hangul jamo, Cherokee and invisible runes.
func sweepPool() []rune {
	var pool []rune
	for _, rg := range [][2]rune{
		{'a', 'z'}, {'A', 'Z'},
		{0x00C0, 0x017F}, // Latin letters with accents
		{0x0300, 0x036F}, // combining marks
		{0x1100, 0x11FF}, // Hangul jamo
		{0x13A0, 0x13F5}, // Cherokee capital letters
		{0xAB70, 0xABBF}, // Cherokee small letters
	} {
		for r := rg[0]; r <= rg[1]; r++ {
			pool = append(pool, r)
		}
	}
	return append(pool, 0x200B, 0x200C, 0x200D, 0x034F, 0xFE0F, 0x3164) // invisible runes
}

// TestCleanUsernameIdempotentSweep cleans 200,000 seeded strings of one to four runes.
// A second clean must change nothing in any of them.
func TestCleanUsernameIdempotentSweep(t *testing.T) {
	pool := sweepPool()
	rng := rand.New(rand.NewPCG(1, 2026))
	var b strings.Builder
	for i := range 200000 {
		b.Reset()
		for range 1 + rng.IntN(4) {
			b.WriteRune(pool[rng.IntN(len(pool))])
		}
		in := b.String()
		once := CleanUsername(in)
		if twice := CleanUsername(once); twice != once {
			t.Fatalf("string %d, %s, cleans to %s, and a second clean gives %s",
				i, codePoints(in), codePoints(once), codePoints(twice))
		}
	}
}

// codePoints lists the code points of s for a failure message.
func codePoints(s string) string {
	parts := make([]string, 0, len(s))
	for _, r := range s {
		parts = append(parts, fmt.Sprintf("U+%04X", r))
	}
	return strings.Join(parts, " ")
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
