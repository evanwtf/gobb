package password

import "strings"

// Recognized reports whether a stored hash has a form and cost that Verify can check.
// It parses the hash and applies the caps that Verify applies. It runs no bcrypt,
// argon2 or phpass work. The scheme is "gobb" or "legacy". Any other scheme gives
// false.
//
// A bcrypt hash passes when its cost is within the cap. The check does not read the
// rest of a bcrypt hash, so a hash with a damaged body can pass and still fail to
// match.
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
		return bcryptCostOK(stored)
	case strings.HasPrefix(stored, argonPrefix):
		_, err := parsePHC(stored)
		return err == nil
	case strings.HasPrefix(stored, chainPrefix):
		return chainShapeOK(strings.TrimPrefix(stored, chainPrefix))
	}
	return false
}
