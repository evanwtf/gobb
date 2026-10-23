package password

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"

	"golang.org/x/crypto/argon2"
	"golang.org/x/text/unicode/norm"
)

// Errors that Hash returns. Verify returns false for the same passwords.
var (
	ErrNotUTF8 = errors.New("password: not valid UTF-8")
	ErrEmpty   = errors.New("password: empty")
	ErrTooLong = errors.New("password: longer than 4096 bytes")
)

// maxGobbForm is the longest form that Hash and Verify accept, in bytes.
const maxGobbForm = 4096

// The parameters of a new gobb hash. Verify asks for a new hash when a stored hash
// has other parameters or lengths.
const (
	gobbMemory      = 19456 // KiB
	gobbTime        = 2     // passes
	gobbParallelism = 1     // lanes
	gobbSaltLen     = 16    // bytes
	gobbKeyLen      = 32    // bytes
)

// Hash returns a gobb hash of raw. The hash is an argon2id PHC string with a new
// random salt. The password is hashed in NFC form, with no other change. Hash
// returns an error for invalid UTF-8, an empty password, or a password over 4,096
// bytes after NFC.
func Hash(raw string) (string, error) {
	form, err := gobbForm(raw)
	if err != nil {
		return "", err
	}
	salt := make([]byte, gobbSaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("password: salt: %w", err)
	}
	key := argon2.IDKey([]byte(form), salt, gobbTime, gobbMemory, gobbParallelism, gobbKeyLen)
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, gobbMemory, gobbTime, gobbParallelism,
		enc.EncodeToString(salt), enc.EncodeToString(key)), nil
}

// Verify reports whether raw matches stored. The scheme is "legacy" for a hash from
// the imported board, and "gobb" for a hash from Hash. needsRehash is true when raw
// matches and the stored hash should be replaced. A legacy hash always needs a
// rehash. A gobb hash needs one when its parameters or lengths differ from the
// current ones. An unknown scheme gives false, false.
func Verify(raw, stored, scheme string) (ok, needsRehash bool) {
	switch scheme {
	case "legacy":
		ok = VerifyLegacy(raw, stored)
		return ok, ok
	case "gobb":
		return verifyGobb(raw, stored)
	}
	return false, false
}

// verifyGobb checks raw against a gobb hash. It parses the stored hash, so the
// limits in parsePHC apply before any hash work.
func verifyGobb(raw, stored string) (ok, needsRehash bool) {
	form, err := gobbForm(raw)
	if err != nil {
		return false, false
	}
	p, err := parsePHC(stored)
	if err != nil {
		return false, false
	}
	got := argon2.IDKey([]byte(form), p.salt, p.t, p.m, p.p, uint32(len(p.hash)))
	if subtle.ConstantTimeCompare(got, p.hash) != 1 {
		return false, false
	}
	stale := p.m != gobbMemory || p.t != gobbTime || p.p != gobbParallelism ||
		len(p.salt) != gobbSaltLen || len(p.hash) != gobbKeyLen
	return true, stale
}

// gobbForm returns the form that gobb hashes: raw in NFC, with no other change. It
// refuses invalid UTF-8, an empty form and a form over maxGobbForm bytes. It does no
// hash work.
func gobbForm(raw string) (string, error) {
	if !utf8.ValidString(raw) {
		return "", ErrNotUTF8
	}
	form := norm.NFC.String(raw)
	if form == "" {
		return "", ErrEmpty
	}
	if len(form) > maxGobbForm {
		return "", ErrTooLong
	}
	return form, nil
}
