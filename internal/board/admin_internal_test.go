package board

import (
	"strings"
	"testing"
)

// The generated password has 16 characters, all from the alphabet, and the
// alphabet holds no look-alike pair: not 0 and O, not 1, l and I.
func TestNewPassword(t *testing.T) {
	if strings.ContainsAny(passwordAlphabet, "0O1lI") {
		t.Errorf("the alphabet %q holds a look-alike character", passwordAlphabet)
	}
	if len(passwordAlphabet) < 50 {
		t.Errorf("the alphabet has %d characters, want at least 50", len(passwordAlphabet))
	}
	seen := map[rune]bool{}
	for range 3000 {
		pw, err := newPassword()
		if err != nil {
			t.Fatal(err)
		}
		if len(pw) != 16 {
			t.Fatalf("password %q has %d characters", pw, len(pw))
		}
		for _, r := range pw {
			if !strings.ContainsRune(passwordAlphabet, r) {
				t.Fatalf("password %q holds %q, outside the alphabet", pw, r)
			}
			seen[r] = true
		}
	}
	// 48,000 draws from a uniform alphabet reach every character.
	if len(seen) != len(passwordAlphabet) {
		t.Errorf("%d of %d alphabet characters appeared", len(seen), len(passwordAlphabet))
	}
}
