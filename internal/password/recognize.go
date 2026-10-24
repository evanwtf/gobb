package password

import (
	"fmt"
	"strings"
)

// Recognized reports whether Verify can match a stored hash under scheme. It parses the
// hash and applies the same shape and caps that Verify applies. It runs no bcrypt,
// argon2 or phpass work. The scheme is "gobb" or "legacy". Any other scheme gives
// false.
//
// A bcrypt hash passes when it is 60 bytes long and starts with $2a$, $2b$ or $2y$. Its
// cost is two digits, from 04 to 14. Its last 53 bytes use the bcrypt alphabet,
// ./A-Za-z0-9. Its last byte has a position in the alphabet that is a multiple of 4, so
// that its two unused low bits are zero. A chain hash passes when its count character
// gives an index from 7 to 18 in the phpass alphabet, and its bcrypt part passes.
func Recognized(scheme, hash string) bool {
	switch scheme {
	case "gobb":
		_, err := parsePHC(hash)
		return err == nil
	case "legacy":
		return legacyRecognized(hash)
	}
	return false
}

// legacyRecognized reports whether VerifyLegacy can check stored. The empty string
// is a known value. No password matches it, but it passes this check.
func legacyRecognized(stored string) bool {
	switch {
	case stored == "":
		return true
	case isBcryptHash(stored):
		return bcryptShapeOK(stored)
	case strings.HasPrefix(stored, argonPrefix):
		_, err := parsePHC(stored)
		return err == nil
	case strings.HasPrefix(stored, chainPrefix):
		return chainShapeOK(strings.TrimPrefix(stored, chainPrefix))
	}
	return false
}

// baseMemoryKiB is the memory that checking a hash needs when the hash is not
// argon2id: bcrypt holds about 4 KiB and phpass less, and the allowance covers
// the password form and the runtime.
const baseMemoryKiB = 1024

// MemoryKiB returns the memory, in KiB, that Verify needs to check a hash stored
// under scheme. For an argon2id hash it is the m of the hash. For every other
// hash it is 1,024. A chain hash costs its costliest layer, so a chain that holds
// an argon2id layer costs the m of that layer. A hash that Verify refuses before
// any work (a shape or a cap that fails) costs 1,024. MemoryKiB parses the hash
// and runs no hash.
//
// A caller that limits the memory of parallel checks asks MemoryKiB first and
// holds at least that much while Verify runs.
func MemoryKiB(scheme, hash string) uint64 {
	cost := uint64(baseMemoryKiB)
	var argon string
	switch scheme {
	case "gobb":
		argon = hash
	case "legacy":
		switch {
		case strings.HasPrefix(hash, argonPrefix):
			argon = hash
		case strings.HasPrefix(hash, chainPrefix):
			// The layers of a chain follow its setting. Count an argon2id layer
			// wherever it starts.
			if i := strings.Index(hash, argonPrefix); i >= 0 {
				argon = hash[i:]
			}
		}
	}
	if argon != "" {
		if p, err := parsePHC(argon); err == nil {
			cost = max(cost, uint64(p.m))
		}
	}
	return cost
}

// CostShape returns a key for the cost of checking a hash stored under scheme:
// the scheme, the format and its cost parameters, without the salt or the
// digest. Two hashes with one key cost about the same to check. The key of an
// argon2id hash is "<scheme> argon2id m=…,t=…,p=…", of a bcrypt hash "legacy
// bcrypt <cost>", and of a chain "legacy chain <count character> bcrypt <cost>".
// A hash that Verify refuses before any work, the empty hash and an unknown
// scheme give "". CostShape parses the hash and runs no hash.
func CostShape(scheme, hash string) string {
	argon := func() string {
		p, err := parsePHC(hash)
		if err != nil {
			return ""
		}
		return fmt.Sprintf("%s argon2id m=%d,t=%d,p=%d", scheme, p.m, p.t, p.p)
	}
	switch scheme {
	case "gobb":
		return argon()
	case "legacy":
		switch {
		case isBcryptHash(hash):
			if bcryptShapeOK(hash) {
				return "legacy bcrypt " + hash[4:6]
			}
		case strings.HasPrefix(hash, argonPrefix):
			return argon()
		case strings.HasPrefix(hash, chainPrefix):
			rest := strings.TrimPrefix(hash, chainPrefix)
			if chainShapeOK(rest) {
				return "legacy chain " + rest[:1] + " bcrypt " + rest[phpassSettingLen+4:phpassSettingLen+6]
			}
		}
	}
	return ""
}
