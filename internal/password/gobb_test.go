package password

import (
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
)

var gobbHashFormat = regexp.MustCompile(
	`^\$argon2id\$v=19\$m=19456,t=2,p=1\$[A-Za-z0-9+/]{22}\$[A-Za-z0-9+/]{43}$`)

// mustHash returns the gobb hash of raw. It fails the test on an error. The test
// never prints the password or the hash.
func mustHash(t testing.TB, raw string) string {
	t.Helper()
	h, err := Hash(raw)
	if err != nil {
		t.Fatalf("Hash: %v", err)
	}
	return h
}

// argon2PHC returns an argon2id PHC string for form with explicit parameters. The
// salt is all zero bytes. Only the lengths matter to these tests.
func argon2PHC(form string, memory, passes uint32, lanes uint8, saltLen, keyLen int) string {
	salt := make([]byte, saltLen)
	key := argon2.IDKey([]byte(form), salt, passes, memory, lanes, uint32(keyLen))
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		memory, passes, lanes, enc.EncodeToString(salt), enc.EncodeToString(key))
}

// capStoredHash returns a gobb-format PHC string at the parsePHC caps. The key is all
// zero bytes, so no password matches it.
func capStoredHash() string {
	enc := base64.RawStdEncoding
	return fmt.Sprintf("$argon2id$v=19$m=%d,t=%d,p=%d$%s$%s",
		maxArgonMemory, maxArgonTime, maxArgonParallelism,
		enc.EncodeToString([]byte("0123456789abcdef")),
		enc.EncodeToString(make([]byte, 32)))
}

func TestHashRoundTrip(t *testing.T) {
	ok, rehash := Verify("s3cret", mustHash(t, "s3cret"), "gobb")
	if !ok || rehash {
		t.Errorf("Verify = %t, %t; want true, false", ok, rehash)
	}
}

func TestHashWrongPassword(t *testing.T) {
	ok, rehash := Verify("s3cre7", mustHash(t, "s3cret"), "gobb")
	if ok || rehash {
		t.Errorf("Verify = %t, %t; want false, false", ok, rehash)
	}
}

// The hash is made from the precomposed form. The check uses the decomposed form.
func TestHashNFCEquivalence(t *testing.T) {
	ok, rehash := Verify("cafe\u0301", mustHash(t, "caf\u00e9"), "gobb")
	if !ok || rehash {
		t.Errorf("decomposed form: Verify = %t, %t; want true, false", ok, rehash)
	}
}

func TestHashFormat(t *testing.T) {
	if !gobbHashFormat.MatchString(mustHash(t, "s3cret")) {
		t.Error("Hash output does not match the gobb format")
	}
}

// A fixed salt would make two hashes of one password equal.
func TestHashSaltIsRandom(t *testing.T) {
	first := mustHash(t, "s3cret")
	second := mustHash(t, "s3cret")
	if first == second {
		t.Error("two hashes of one password are equal")
	}
}

func TestHashRejectsBadPasswords(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want error
	}{
		{"invalid UTF-8", "\xff", ErrNotUTF8},
		{"empty", "", ErrEmpty},
		{"4,097 bytes", strings.Repeat("a", 4097), ErrTooLong},
		// 2,048 copies of U+0344 are 4,096 bytes raw. NFC makes each one two
		// characters, so the form is 8,192 bytes.
		{"NFC grows past 4,096", strings.Repeat("\u0344", 2048), ErrTooLong},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h, err := Hash(tc.raw)
			if !errors.Is(err, tc.want) || h != "" {
				t.Errorf("Hash error = %v, empty hash = %t; want %v", err, h == "", tc.want)
			}
		})
	}
}

// ErrTooLong names the limit. Its text follows maxGobbForm, so a change to the limit
// also changes the message.
func TestErrTooLongNamesTheLimit(t *testing.T) {
	want := strconv.Itoa(maxGobbForm) + " bytes"
	if !strings.Contains(ErrTooLong.Error(), want) {
		t.Errorf("ErrTooLong = %q; want it to contain %q", ErrTooLong.Error(), want)
	}
}

// A password of 4,096 raw bytes is accepted. So is decomposed text of 3,600 bytes,
// which is 2,400 bytes after NFC. A raw input over 4,096 bytes is refused before NFC
// (see TestRawOverLimitIsRefusedBeforeNFC).
func TestHashLengthLimit(t *testing.T) {
	cases := []struct {
		name string
		raw  string
	}{
		{"4,096 bytes", strings.Repeat("a", 4096)},
		{"decomposed, 3,600 bytes raw", strings.Repeat("e\u0301", 1200)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ok, rehash := Verify(tc.raw, mustHash(t, tc.raw), "gobb")
			if !ok || rehash {
				t.Errorf("Verify = %t, %t; want true, false", ok, rehash)
			}
		})
	}
}

// A bad password gives false, false before any hashing. The stored hash sits at the
// parsePHC caps, so a check that ran argon2 would exceed the 100 ms bound. The bound
// is loose on purpose, so scheduler delays on a shared runner do not fail it.
func TestVerifyRefusesBadPasswords(t *testing.T) {
	stored := capStoredHash()
	if _, err := parsePHC(stored); err != nil {
		t.Fatalf("precondition: parsePHC refuses capStoredHash: %v", err)
	}
	cases := []struct {
		name string
		raw  string
	}{
		{"invalid UTF-8", "\xff"},
		{"empty", ""},
		{"NFC grows past 4,096", strings.Repeat("\u0344", 2048)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			ok, rehash := Verify(tc.raw, stored, "gobb")
			elapsed := time.Since(start)
			if ok || rehash {
				t.Errorf("Verify = %t, %t; want false, false", ok, rehash)
			}
			if elapsed > 100*time.Millisecond {
				t.Errorf("Verify took %v, want under 100ms", elapsed)
			}
		})
	}
}

func TestVerifyGobbRejectsHuge(t *testing.T) {
	stored := mustHash(t, "s3cret")
	start := time.Now()
	ok, rehash := Verify(strings.Repeat("a", 4097), stored, "gobb")
	elapsed := time.Since(start)
	if ok || rehash {
		t.Errorf("Verify = %t, %t; want false, false", ok, rehash)
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("4,097-byte password took %v, want under 5ms", elapsed)
	}
}

func TestVerifyLegacySchemeNeedsRehash(t *testing.T) {
	v := vectorByName(t, "bcrypt-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if ok, rehash := Verify(string(raw), v.Hash, "legacy"); !ok || !rehash {
		t.Errorf("matching legacy hash: Verify = %t, %t; want true, true", ok, rehash)
	}
	if ok, rehash := Verify("not the password", v.Hash, "legacy"); ok || rehash {
		t.Errorf("wrong password: Verify = %t, %t; want false, false", ok, rehash)
	}
}

// A gobb hash with other parameters or lengths matches, and it needs a rehash.
func TestVerifyOldParamsNeedRehash(t *testing.T) {
	cases := []struct {
		name   string
		stored string
	}{
		{"time 1", argon2PHC("s3cret", 19456, 1, 1, 16, 32)},
		{"memory 16 KiB", argon2PHC("s3cret", 16, 2, 1, 16, 32)},
		{"two lanes", argon2PHC("s3cret", 16, 2, 2, 16, 32)},
		{"two lanes only", argon2PHC("s3cret", 19456, 2, 2, 16, 32)},
		{"key 16 bytes", argon2PHC("s3cret", 19456, 2, 1, 16, 16)},
		{"salt 8 bytes", argon2PHC("s3cret", 19456, 2, 1, 8, 32)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if ok, rehash := Verify("s3cret", tc.stored, "gobb"); !ok || !rehash {
				t.Errorf("Verify = %t, %t; want true, true", ok, rehash)
			}
		})
	}
}

// A stored value that is not a gobb PHC string gives false, false.
func TestVerifyGobbRefusesOtherFormats(t *testing.T) {
	v := vectorByName(t, "bcrypt-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	for _, stored := range []string{v.Hash, "", "$argon2id$v=19$m=8,t=1,p=1$AAAAAAAAAAAAAAAA$"} {
		if ok, rehash := Verify(string(raw), stored, "gobb"); ok || rehash {
			t.Errorf("Verify = %t, %t; want false, false", ok, rehash)
		}
	}
}

func TestVerifyUnknownScheme(t *testing.T) {
	stored := mustHash(t, "s3cret")
	for _, scheme := range []string{"", "bcrypt", "GOBB", "Legacy"} {
		if ok, rehash := Verify("s3cret", stored, scheme); ok || rehash {
			t.Errorf("scheme %q: Verify = %t, %t; want false, false", scheme, ok, rehash)
		}
	}
}

// The 4,096-byte limit counts the raw input, before NFC. 1,500 copies of a
// decomposed "e with acute" are 4,500 bytes raw and 3,000 bytes in NFC.
func TestRawOverLimitIsRefusedBeforeNFC(t *testing.T) {
	raw := strings.Repeat("é", 1500)
	if len(raw) != 4500 {
		t.Fatalf("precondition: len = %d", len(raw))
	}
	if form, err := gobbForm(strings.Repeat("é", 1500)); err != nil || len(form) != 3000 {
		t.Fatalf("precondition: the NFC form is %d bytes, err %v", len(form), err)
	}
	if _, err := Hash(raw); !errors.Is(err, ErrTooLong) {
		t.Errorf("Hash error = %v, want ErrTooLong", err)
	}

	// A hash of the NFC form must not match the longer raw input.
	stored := mustHash(t, strings.Repeat("é", 1500))
	if ok, _ := Verify(raw, stored, "gobb"); ok {
		t.Error("Verify accepted 4,500 raw bytes")
	}
	if ok, _ := Verify(strings.Repeat("é", 1500), stored, "gobb"); !ok {
		t.Error("Verify refused the 3,000-byte NFC password")
	}
}

func TestHugeInputIsRefusedFast(t *testing.T) {
	huge := strings.Repeat("a", 3<<20)
	start := time.Now()
	_, err := Hash(huge)
	ok, _ := Verify(huge, capStoredHash(), "gobb")
	elapsed := time.Since(start)
	if !errors.Is(err, ErrTooLong) {
		t.Errorf("Hash error = %v, want ErrTooLong", err)
	}
	if ok {
		t.Error("Verify accepted 3 MB")
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("3 MB took %v for Hash and Verify, want under 5ms", elapsed)
	}
}
