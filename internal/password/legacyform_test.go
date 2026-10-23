package password

import "testing"

func TestLegacyForm(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
		ok   bool
	}{
		{"ampersand", "a&b", "a&amp;b", true},
		{"double quote", `say "hi"`, "say &quot;hi&quot;", true},
		{"angle brackets", "<x>", "&lt;x&gt;", true},
		{"single quote stays", "it's", "it's", true},
		{"ampersand entity", "&amp;", "&amp;amp;", true},
		{"spaces at both ends", "  pw  ", "pw", true},
		{"vertical tab and tab", "\x0Bpw\t", "pw", true},
		{"CR LF becomes LF", "a\r\nb", "a\nb", true},
		{"lone CR becomes LF", "a\rb", "a\nb", true},
		{"NUL is removed", "a\x00b", "ab", true},
		{"byte order mark is removed", "\uFEFFpw", "pw", true},
		{"decomposed accent becomes NFC", "cafe\u0301", "caf\u00e9", true},
		{"invalid UTF-8 has no form", "\xff", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := LegacyForm(tc.raw)
			if got != tc.want || ok != tc.ok {
				t.Errorf("LegacyForm(%q) = %q, %t; want %q, %t", tc.raw, got, ok, tc.want, tc.ok)
			}
		})
	}
}
