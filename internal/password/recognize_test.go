package password

import (
	"strings"
	"testing"
	"time"
)

func TestRecognized(t *testing.T) {
	bcrypt10 := vectorByName(t, "bcrypt-horse").Hash
	bcrypt15 := strings.Replace(bcrypt10, "$10$", "$15$", 1)
	argon := vectorByName(t, "argon2id-horse").Hash
	chain := vectorByName(t, "chain-horse").Hash
	overMemory := strings.Replace(capStoredHash(), "m=262144,", "m=999999999,", 1)
	if bcrypt15 == bcrypt10 || overMemory == capStoredHash() {
		t.Fatal("precondition: an edit did not change the hash")
	}
	gobbHash := mustHash(t, "x")

	cases := []struct {
		name   string
		scheme string
		hash   string
		want   bool
	}{
		{"gobb hash", "gobb", gobbHash, true},
		{"argon2id memory over cap under gobb", "gobb", overMemory, false},
		{"bcrypt under gobb", "gobb", bcrypt10, false},
		{"empty under gobb", "gobb", "", false},
		{"empty under legacy", "legacy", "", true},
		{"bcrypt cost 10 under legacy", "legacy", bcrypt10, true},
		{"bcrypt cost 15 under legacy", "legacy", bcrypt15, false},
		{"argon2id under legacy", "legacy", argon, true},
		{"argon2id memory over cap under legacy", "legacy", overMemory, false},
		{"chain row under legacy", "legacy", chain, true},
		{"md5 under legacy", "legacy", "md5:abc", false},
		{"gobb hash under plain", "plain", gobbHash, false},
		{"bcrypt under plain", "plain", bcrypt10, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Recognized(tc.scheme, tc.hash); got != tc.want {
				t.Errorf("Recognized(%q, hash) = %t, want %t", tc.scheme, got, tc.want)
			}
		})
	}
}

// The hash is the real cost-10 vector with its cost edited to 14. Recognized parses
// the cost and runs no bcrypt work. A bcrypt run at cost 14 takes far longer than
// the bound, so a run would fail the test.
func TestRecognizedIsFast(t *testing.T) {
	orig := vectorByName(t, "bcrypt-horse").Hash
	hash := strings.Replace(orig, "$10$", "$14$", 1)
	if hash == orig {
		t.Fatal("no cost 10 in the vector hash")
	}
	start := time.Now()
	for range 1000 {
		if !Recognized("legacy", hash) {
			t.Fatal("bcrypt cost 14 is not recognized")
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("1,000 calls took %v, want under 50ms", elapsed)
	}
}

// Index 6 is below the phpass minimum of 7, so Recognized refuses it. Index 7 is the
// lowest allowed index and index 18 the highest. Index 19 is refused.
func TestRecognizedChainIndexBounds(t *testing.T) {
	rest := strings.TrimPrefix(vectorByName(t, "chain-horse").Hash, chainPrefix)
	setting, bc := rest[:phpassSettingLen], rest[phpassSettingLen:]
	cases := []struct {
		name  string
		index int
		want  bool
	}{
		{"index 6 refused", 6, false},
		{"index 7 recognized", 7, true},
		{"index 18 recognized", 18, true},
		{"index 19 refused", 19, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			stored := chainPrefix + string(phpassAlphabet[tc.index]) + setting[1:] + bc
			if got := Recognized("legacy", stored); got != tc.want {
				t.Errorf("Recognized = %t, want %t", got, tc.want)
			}
		})
	}
}

// A stored hash that VerifyLegacy accepts must be recognized. Each vector that
// verifies is a check of that rule.
func TestRecognizedCoversVerifiedVectors(t *testing.T) {
	verified := 0
	for _, v := range loadVectors(t) {
		if !v.Want {
			continue
		}
		verified++
		if !Recognized("legacy", v.Hash) {
			t.Errorf("vector %q verifies but is not recognized", v.Name)
		}
	}
	if verified == 0 {
		t.Fatal("no vector verifies, so the check proves nothing")
	}
}
