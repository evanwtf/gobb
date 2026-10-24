package account

import "testing"

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
		{"ideographic spaces", "　BOB　", "bob"},
		{"sharp s", "Straße", "strasse"},
		{"capitals with sharp s", "STRASSE", "strasse"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := CleanUsername(tc.in); got != tc.want {
				t.Errorf("CleanUsername(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
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
