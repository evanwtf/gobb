package password

import "strings"

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
