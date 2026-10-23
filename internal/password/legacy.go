package password

import (
	"crypto/md5"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

// maxLegacyForm is the longest legacy form VerifyLegacy hashes, in bytes.
const maxLegacyForm = 4096

// Limits on the work that a stored bcrypt or phpass hash may demand. Each step up in
// a bcrypt cost doubles the work. A phpass index n gives 2^n MD5 rounds.
const (
	maxBcryptCost  = 14
	maxPhpassIndex = 18
)

// Limits on the cost that a stored argon2id hash may demand. A stored hash must
// not make a login use unbounded memory or time.
const (
	maxArgonMemory      = 262144 // KiB
	maxArgonTime        = 10
	maxArgonParallelism = 8
	minArgonHashLen     = 16
	maxArgonHashLen     = 64
)

const (
	chainPrefix = "$chain$phpass,"
	argonPrefix = "$argon2id$"
)

// VerifyLegacy reports whether raw matches stored, a password hash from the
// imported board. It supports bcrypt ($2y$, $2a$, $2b$), argon2id and the
// chain format ($chain$phpass,). It returns false for any other format, for a
// password with no legacy form and for a form over 4,096 bytes.
func VerifyLegacy(raw, stored string) bool {
	form, ok := LegacyForm(raw)
	if !ok || form == "" || len(form) > maxLegacyForm {
		return false
	}
	switch {
	case strings.HasPrefix(stored, "$2y$"),
		strings.HasPrefix(stored, "$2a$"),
		strings.HasPrefix(stored, "$2b$"):
		return bcryptMatches(stored, form)
	case strings.HasPrefix(stored, argonPrefix):
		return argon2idMatches(stored, form)
	case strings.HasPrefix(stored, chainPrefix):
		return chainMatches(strings.TrimPrefix(stored, chainPrefix), form)
	}
	return false
}

// bcryptMatches compares input with a bcrypt hash. It refuses a cost above
// maxBcryptCost before any bcrypt work. x/crypto accepts the $2y$ marker, so the
// rewrite to $2a$ is defensive.
func bcryptMatches(hash, input string) bool {
	if !bcryptCostOK(hash) {
		return false
	}
	hash = normalizeBcryptPrefix(hash)
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(input)) == nil
}

// bcryptCostOK reports whether hash is a bcrypt hash with a cost of at most
// maxBcryptCost. It does no bcrypt work.
func bcryptCostOK(hash string) bool {
	cost, err := bcrypt.Cost([]byte(hash))
	return err == nil && cost <= maxBcryptCost
}

// normalizeBcryptPrefix changes a $2y$ prefix to $2a$. It changes nothing else.
func normalizeBcryptPrefix(hash string) string {
	if rest, ok := strings.CutPrefix(hash, "$2y$"); ok {
		return "$2a$" + rest
	}
	return hash
}

func argon2idMatches(stored, form string) bool {
	p, err := parsePHC(stored)
	if err != nil {
		return false
	}
	got := argon2.IDKey([]byte(form), p.salt, p.t, p.m, p.p, uint32(len(p.hash)))
	return subtle.ConstantTimeCompare(got, p.hash) == 1
}

// chainMatches checks the part of a chain hash after "$chain$phpass,". That part
// is a 9-character portable-hash setting and a bcrypt hash of the portable-hash
// digest. The digest covers the form, or the lowercase hex MD5 of the form.
func chainMatches(rest, form string) bool {
	if len(rest) < phpassSettingLen || !strings.HasPrefix(rest[phpassSettingLen:], "$2") {
		return false
	}
	setting, bc := rest[:phpassSettingLen], rest[phpassSettingLen:]
	// Refuse work above the limits, and a malformed bcrypt hash, before the
	// portable-hash loop.
	if strings.IndexByte(phpassAlphabet, setting[0]) > maxPhpassIndex || !bcryptCostOK(bc) {
		return false
	}
	sum := md5.Sum([]byte(form))
	for _, in := range []string{form, hex.EncodeToString(sum[:])} {
		d, ok := phpassDigest(setting, in)
		if ok && bcryptMatches(bc, d) {
			return true
		}
	}
	return false
}

// phc holds the fields of a parsed argon2id PHC string.
type phc struct {
	m    uint32 // memory, KiB
	t    uint32 // passes
	p    uint8  // lanes
	salt []byte
	hash []byte
}

// parsePHC parses "$argon2id$v=19$m=<KiB>,t=<n>,p=<n>$<salt>$<hash>". It checks
// every field and the cost limits before any hashing. The error names the field
// that failed. It does not repeat the input, because the input is a secret.
func parsePHC(s string) (phc, error) {
	var zero phc
	parts := strings.Split(s, "$")
	if len(parts) != 6 || parts[0] != "" {
		return zero, errors.New("phc: fields: want 5 fields after a leading $")
	}
	if parts[1] != "argon2id" {
		return zero, errors.New("phc: algorithm: not argon2id")
	}
	v, ok := strings.CutPrefix(parts[2], "v=")
	if !ok || v != "19" {
		return zero, errors.New("phc: version: want v=19")
	}
	params := strings.Split(parts[3], ",")
	if len(params) != 3 {
		return zero, errors.New("phc: parameters: want m, t and p")
	}
	m, err := phcUint(params[0], "m=")
	if err != nil {
		return zero, errors.New("phc: memory: not a number")
	}
	t, err := phcUint(params[1], "t=")
	if err != nil {
		return zero, errors.New("phc: time: not a number")
	}
	p, err := phcUint(params[2], "p=")
	if err != nil {
		return zero, errors.New("phc: parallelism: not a number")
	}
	if p < 1 || p > maxArgonParallelism {
		return zero, fmt.Errorf("phc: parallelism: want 1 to %d", maxArgonParallelism)
	}
	if t < 1 || t > maxArgonTime {
		return zero, fmt.Errorf("phc: time: want 1 to %d", maxArgonTime)
	}
	if m < 8*p || m > maxArgonMemory {
		return zero, fmt.Errorf("phc: memory: want 8 times parallelism to %d KiB", maxArgonMemory)
	}
	salt, err := phcBase64(parts[4])
	if err != nil || len(salt) == 0 {
		return zero, errors.New("phc: salt: not valid unpadded base64")
	}
	hash, err := phcBase64(parts[5])
	if err != nil || len(hash) < minArgonHashLen || len(hash) > maxArgonHashLen {
		return zero, fmt.Errorf("phc: hash: want unpadded base64 of %d to %d bytes",
			minArgonHashLen, maxArgonHashLen)
	}
	return phc{m: uint32(m), t: uint32(t), p: uint8(p), salt: salt, hash: hash}, nil
}

func phcUint(field, prefix string) (uint32, error) {
	num, ok := strings.CutPrefix(field, prefix)
	if !ok {
		return 0, errors.New("missing label")
	}
	n, err := strconv.ParseUint(num, 10, 32)
	return uint32(n), err
}

// phcBase64 decodes unpadded standard base64. Go's decoder skips CR and LF, so
// the function refuses them first.
func phcBase64(s string) ([]byte, error) {
	if strings.ContainsAny(s, "\r\n") {
		return nil, errors.New("line break")
	}
	return base64.RawStdEncoding.DecodeString(s)
}
