package password

import (
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
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

// Each hash sits at a cap: a bcrypt cost of 14, a chain hash with phpass count index
// 18 and a bcrypt cost of 14, and an argon2id memory of 256 MiB. Recognized parses each
// hash and runs no bcrypt, argon2 or phpass work. One bcrypt run at cost 14 takes far
// longer than the bound, so a run would fail the test.
func TestRecognizedIsFast(t *testing.T) {
	orig := vectorByName(t, "bcrypt-horse").Hash
	bcrypt14 := strings.Replace(orig, "$10$", "$14$", 1)
	rest := strings.TrimPrefix(vectorByName(t, "chain-horse").Hash, chainPrefix)
	setting, bc := rest[:phpassSettingLen], rest[phpassSettingLen:]
	chainBC14 := strings.Replace(bc, "$2y$10$", "$2y$14$", 1)
	if bcrypt14 == orig || chainBC14 == bc {
		t.Fatal("precondition: no cost 10 to edit")
	}
	chain18 := chainPrefix + string(phpassAlphabet[18]) + setting[1:] + chainBC14
	hashes := []string{bcrypt14, chain18, capStoredHash()}

	start := time.Now()
	for range 1000 {
		for _, h := range hashes {
			if !Recognized("legacy", h) {
				t.Fatalf("hash of %d bytes is not recognized", len(h))
			}
		}
	}
	if elapsed := time.Since(start); elapsed > 50*time.Millisecond {
		t.Errorf("3,000 calls took %v, want under 50ms", elapsed)
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

// Each case breaks one rule of the bcrypt shape in the real cost-10 vector. Recognized
// must refuse each hash, and VerifyLegacy must refuse the password for it.
func TestDeadBcryptHashesAreRefused(t *testing.T) {
	v := vectorByName(t, "bcrypt-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: bcrypt vector does not verify")
	}
	h := v.Hash
	const saltAt, bodyAt = 7, 40
	cases := []struct {
		name string
		hash string
	}{
		{"59 bytes", h[:len(h)-1]},
		{"61 bytes, with a trailing A", h + "A"},
		{"salt character outside the alphabet", h[:saltAt] + "!" + h[saltAt+1:]},
		{"hash character outside the alphabet", h[:bodyAt] + "!" + h[bodyAt+1:]},
		{"last character with unused bits set", h[:len(h)-1] + "/"},
		{"cost with a plus sign", h[:4] + "+4" + h[6:]},
		{"cost below the bcrypt minimum", h[:4] + "03" + h[6:]},
		{"cost above the cap", h[:4] + "15" + h[6:]},
		{"marker $2x$", h[:2] + "x" + h[3:]},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Recognized("legacy", tc.hash) {
				t.Error("Recognized accepted the hash")
			}
			if VerifyLegacy(string(raw), tc.hash) {
				t.Error("VerifyLegacy accepted the password for the hash")
			}
		})
	}
}

// Each case breaks the bcrypt part of the real chain vector. The bcrypt part is the last
// 60 bytes of the chain hash.
func TestDeadChainHashesAreRefused(t *testing.T) {
	v := vectorByName(t, "chain-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: chain vector does not verify")
	}
	h := v.Hash
	bcAt := len(chainPrefix) + phpassSettingLen
	if len(h) != bcAt+60 {
		t.Fatalf("precondition: chain hash is %d bytes, want %d", len(h), bcAt+60)
	}
	cases := []struct {
		name string
		hash string
	}{
		{"one byte cut from the end", h[:len(h)-1]},
		{"a trailing A", h + "A"},
		{"bcrypt salt character outside the alphabet", h[:bcAt+7] + "!" + h[bcAt+8:]},
		{"bcrypt hash character outside the alphabet", h[:bcAt+40] + "!" + h[bcAt+41:]},
		{"bcrypt last character with unused bits set", h[:len(h)-1] + "/"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if Recognized("legacy", tc.hash) {
				t.Error("Recognized accepted the chain hash")
			}
			if VerifyLegacy(string(raw), tc.hash) {
				t.Error("VerifyLegacy accepted the password for the chain hash")
			}
		})
	}
}

// The shape rules accept every hash that the bcrypt library writes. Each hash has a new
// random salt.
func TestBcryptShapeAcceptsGeneratedHashes(t *testing.T) {
	for range 64 {
		h, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
		if err != nil {
			t.Fatal(err)
		}
		if !bcryptShapeOK(string(h)) || !Recognized("legacy", string(h)) {
			t.Fatalf("library hash of %d bytes is refused by the shape rules", len(h))
		}
	}
}
