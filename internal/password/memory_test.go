package password

import (
	"strings"
	"testing"
)

func TestMemoryKiB(t *testing.T) {
	bcrypt10 := vectorByName(t, "bcrypt-horse").Hash
	legacyArgon := vectorByName(t, "argon2id-horse").Hash
	chain := vectorByName(t, "chain-horse").Hash
	rest := strings.TrimPrefix(chain, chainPrefix)
	setting := rest[:phpassSettingLen]
	// A chain whose last layer is argon2id with m=65536: the costliest layer counts.
	argonTail := argon2PHC("x", 65536, 1, 1, 16, 32)
	chainWithArgon := chainPrefix + setting + argonTail
	// A chain with a bcrypt layer only: every layer that is not argon2id costs 1,024.
	overCap := strings.Replace(capStoredHash(), "m=262144,", "m=999999999,", 1)
	if overCap == capStoredHash() || !strings.Contains(legacyArgon, "$argon2id$") {
		t.Fatal("precondition")
	}

	cases := []struct {
		name         string
		scheme, hash string
		want         uint64
	}{
		{"gobb hash", "gobb", mustHash(t, "x"), 19456},
		{"legacy argon2id", "legacy", argon2PHC("x", 65536, 1, 1, 16, 32), 65536},
		{"legacy argon2id at the cap", "legacy", capStoredHash(), 262144},
		{"gobb argon2id with other memory", "gobb", argon2PHC("x", 32768, 2, 1, 16, 32), 32768},
		{"bcrypt", "legacy", bcrypt10, 1024},
		{"chain of phpass and bcrypt", "legacy", chain, 1024},
		{"chain that ends in argon2id", "legacy", chainWithArgon, 65536},
		{"empty", "legacy", "", 1024},
		{"argon2id over the cap runs no work", "legacy", overCap, 1024},
		{"unknown scheme", "plain", "anything", 1024},
		{"garbage", "gobb", "$argon2id$nonsense", 1024},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := MemoryKiB(tc.scheme, tc.hash); got != tc.want {
				t.Errorf("MemoryKiB(%q, ...) = %d, want %d", tc.scheme, got, tc.want)
			}
		})
	}
}

// MemoryKiB agrees with Hash: a fresh hash costs gobbMemory.
func TestMemoryKiBOfNewHash(t *testing.T) {
	if got := MemoryKiB("gobb", mustHash(t, "x")); got != gobbMemory {
		t.Errorf("MemoryKiB of a new hash = %d, want %d", got, gobbMemory)
	}
}
