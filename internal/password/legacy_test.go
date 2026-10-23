package password

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/bcrypt"
)

type legacyVector struct {
	Name   string `json:"name"`
	RawB64 string `json:"raw_b64"`
	Hash   string `json:"hash"`
	Want   bool   `json:"want"`
}

func loadVectors(t testing.TB) []legacyVector {
	t.Helper()
	data, err := os.ReadFile("testdata/legacy-vectors.json")
	if err != nil {
		t.Fatalf("read vectors: %v", err)
	}
	var vs []legacyVector
	if err := json.Unmarshal(data, &vs); err != nil {
		t.Fatalf("parse vectors: %v", err)
	}
	return vs
}

func vectorByName(t testing.TB, name string) legacyVector {
	t.Helper()
	for _, v := range loadVectors(t) {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("no vector named %q", name)
	return legacyVector{}
}

func TestVerifyLegacyVectors(t *testing.T) {
	vs := loadVectors(t)
	if len(vs) != 120 {
		t.Fatalf("got %d vectors, want 120", len(vs))
	}
	for _, v := range vs {
		t.Run(v.Name, func(t *testing.T) {
			t.Parallel()
			raw, err := base64.StdEncoding.DecodeString(v.RawB64)
			if err != nil {
				t.Fatalf("decode raw_b64: %v", err)
			}
			if got := VerifyLegacy(string(raw), v.Hash); got != v.Want {
				t.Errorf("VerifyLegacy = %t, want %t", got, v.Want)
			}
		})
	}
}

func TestVerifyLegacyEmptyHash(t *testing.T) {
	if VerifyLegacy("anything", "") {
		t.Error("empty stored hash verified")
	}
}

func TestVerifyLegacyUnknownFormat(t *testing.T) {
	for _, stored := range []string{
		"$P$9IQRaTwmfeRo7ud9Fh4E2PdI0S3r.L0",
		"$chain$md5,abcdef",
		"plain",
	} {
		if VerifyLegacy("test12345", stored) {
			t.Errorf("VerifyLegacy accepted unknown format %q", stored)
		}
	}
}

func TestVerifyLegacyRejectsHugeAndInvalid(t *testing.T) {
	v := vectorByName(t, "bcrypt-horse")
	start := time.Now()
	ok := VerifyLegacy(strings.Repeat("a", 4097), v.Hash)
	elapsed := time.Since(start)
	if ok {
		t.Error("4097-byte password verified")
	}
	if elapsed > 5*time.Millisecond {
		t.Errorf("4097-byte password took %v, want under 5ms", elapsed)
	}
	if VerifyLegacy("\xff", v.Hash) {
		t.Error("invalid UTF-8 verified")
	}
}

// A password made only of trim characters has an empty legacy form. A hash of the
// empty string must not match it.
func TestVerifyLegacyEmptyFormIsFalse(t *testing.T) {
	if f, ok := LegacyForm(" \t\r\n"); f != "" || !ok {
		t.Fatalf("precondition: LegacyForm = %q, %t", f, ok)
	}
	bc, err := bcrypt.GenerateFromPassword([]byte(""), bcrypt.MinCost)
	if err != nil {
		t.Fatalf("bcrypt: %v", err)
	}
	if !strings.HasPrefix(string(bc), "$2a$") {
		t.Fatalf("unexpected bcrypt prefix")
	}
	if VerifyLegacy("  ", string(bc)) {
		t.Error("empty form verified against a bcrypt hash of the empty string")
	}
	if VerifyLegacy("", string(bc)) {
		t.Error("empty password verified against a bcrypt hash of the empty string")
	}
	salt := []byte("0123456789abcdef")
	key := argon2.IDKey(nil, salt, 1, 8, 1, 32)
	enc := base64.RawStdEncoding
	stored := "$argon2id$v=19$m=8,t=1,p=1$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(key)
	if VerifyLegacy("\t", stored) {
		t.Error("empty form verified against an argon2id hash of the empty string")
	}
}

// A form over 4,096 bytes is refused even when the hash matches it. The test uses
// argon2id because bcrypt cannot hash more than 72 bytes.
func TestVerifyLegacyFormOverLimit(t *testing.T) {
	enc := base64.RawStdEncoding
	salt := []byte("0123456789abcdef")
	build := func(form string) string {
		key := argon2.IDKey([]byte(form), salt, 1, 8, 1, 32)
		return "$argon2id$v=19$m=8,t=1,p=1$" + enc.EncodeToString(salt) + "$" + enc.EncodeToString(key)
	}
	atLimit := strings.Repeat("a", 4096)
	if !VerifyLegacy(atLimit, build(atLimit)) {
		t.Error("4096-byte form is refused")
	}
	over := strings.Repeat("a", 4097)
	if VerifyLegacy(over, build(over)) {
		t.Error("4097-byte form verified")
	}
	// 2,000 ampersands are 2,000 bytes raw and 10,000 bytes as a form.
	amps := strings.Repeat("&", 2000)
	form, _ := LegacyForm(amps)
	if VerifyLegacy(amps, build(form)) {
		t.Error("password whose form passes 4096 bytes verified")
	}
}

func TestVerifyLegacyChainMalformed(t *testing.T) {
	v := vectorByName(t, "chain-horse")
	if !strings.HasPrefix(v.Hash, "$chain$phpass,") {
		t.Fatalf("unexpected vector hash prefix")
	}
	rest := strings.TrimPrefix(v.Hash, "$chain$phpass,")
	setting, bc := rest[:9], rest[9:]
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: chain vector does not verify")
	}
	cases := map[string]string{
		"nothing after the prefix": "$chain$phpass,",
		"setting only, short":      "$chain$phpass," + setting[:8],
		"setting only, 9":          "$chain$phpass," + setting,
		"setting and one char":     "$chain$phpass," + setting + "$",
		"rest is not bcrypt":       "$chain$phpass," + setting + "x" + bc[1:],
		"rest starts with $1":      "$chain$phpass," + setting + "$1" + bc[2:],
		"truncated bcrypt":         v.Hash[:len(v.Hash)-10],
		"bcrypt cost not a number": "$chain$phpass," + setting + "$2y$xx" + bc[6:],
		"empty setting chars":      "$chain$phpass,$2y$10$",
	}
	for name, stored := range cases {
		t.Run(name, func(t *testing.T) {
			if VerifyLegacy(string(raw), stored) {
				t.Error("malformed chain hash verified")
			}
		})
	}
}

func TestVerifyLegacyArgon2Caps(t *testing.T) {
	v := vectorByName(t, "argon2id-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: argon2id vector does not verify")
	}
	for _, params := range []string{"m=262145,t=4,p=2", "m=65536,t=11,p=2", "m=65536,t=4,p=9"} {
		stored := strings.Replace(v.Hash, "m=65536,t=4,p=2", params, 1)
		start := time.Now()
		if VerifyLegacy(string(raw), stored) {
			t.Errorf("verified with params %s", params)
		}
		if time.Since(start) > 50*time.Millisecond {
			t.Errorf("params %s were not refused before work", params)
		}
	}
}

// A bcrypt cost above the limit is refused before any bcrypt work. The hash is the
// real cost-10 vector with its cost edited to 15. The test never runs a cost-15 bcrypt.
func TestVerifyLegacyRefusesHighBcryptCost(t *testing.T) {
	v := vectorByName(t, "bcrypt-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: bcrypt vector does not verify")
	}
	high := strings.Replace(v.Hash, "$10$", "$15$", 1)
	if high == v.Hash {
		t.Fatal("no cost 10 in the vector hash")
	}
	start := time.Now()
	ok := VerifyLegacy(string(raw), high)
	elapsed := time.Since(start)
	if ok {
		t.Error("bcrypt hash with cost 15 verified")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("bcrypt hash with cost 15 took %v, want under 50ms", elapsed)
	}
}

// The same cost limit applies to the bcrypt part of a chain hash.
func TestVerifyLegacyRefusesHighChainBcryptCost(t *testing.T) {
	v := vectorByName(t, "chain-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: chain vector does not verify")
	}
	high := strings.Replace(v.Hash, "$2y$10$", "$2y$15$", 1)
	if high == v.Hash {
		t.Fatal("no cost 10 in the chain bcrypt part")
	}
	start := time.Now()
	ok := VerifyLegacy(string(raw), high)
	elapsed := time.Since(start)
	if ok {
		t.Error("chain hash with bcrypt cost 15 verified")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("chain hash with bcrypt cost 15 took %v, want under 50ms", elapsed)
	}
}

// A phpass count index above the limit is refused before the portable-hash loop.
// Index 19 is 'H' in the alphabet. It means 2^19 MD5 rounds.
func TestVerifyLegacyRefusesHighPhpassIndex(t *testing.T) {
	v := vectorByName(t, "chain-horse")
	raw, err := base64.StdEncoding.DecodeString(v.RawB64)
	if err != nil {
		t.Fatal(err)
	}
	if !VerifyLegacy(string(raw), v.Hash) {
		t.Fatal("precondition: chain vector does not verify")
	}
	rest := strings.TrimPrefix(v.Hash, chainPrefix)
	setting, bc := rest[:phpassSettingLen], rest[phpassSettingLen:]
	high := chainPrefix + string(phpassAlphabet[19]) + setting[1:] + bc
	start := time.Now()
	ok := VerifyLegacy(string(raw), high)
	elapsed := time.Since(start)
	if ok {
		t.Error("chain hash with phpass index 19 verified")
	}
	if elapsed > 50*time.Millisecond {
		t.Errorf("chain hash with phpass index 19 took %v, want under 50ms", elapsed)
	}
}

// The $2y$ rewrite changes the prefix and nothing else.
func TestNormalizeBcryptPrefix(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"2y prefix becomes 2a", "$2y$10$abc", "$2a$10$abc"},
		{"2a prefix stays", "$2a$10$abc", "$2a$10$abc"},
		{"2b prefix stays", "$2b$10$abc", "$2b$10$abc"},
		{"2y later in the string stays", "$2b$10$abc$2y$", "$2b$10$abc$2y$"},
		{"2y not at the start stays", "x$2y$10$abc", "x$2y$10$abc"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := normalizeBcryptPrefix(tc.in); got != tc.want {
				t.Errorf("normalizeBcryptPrefix(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestParsePHC(t *testing.T) {
	enc := base64.RawStdEncoding
	salt := enc.EncodeToString([]byte("0123456789abcdef"))
	h32 := enc.EncodeToString(make([]byte, 32))
	build := func(version, params, s, h string) string {
		return "$argon2id$" + version + "$" + params + "$" + s + "$" + h
	}
	const ok = "m=65536,t=4,p=2"

	good, err := parsePHC(build("v=19", ok, salt, h32))
	if err != nil {
		t.Fatalf("valid string refused: %v", err)
	}
	if good.m != 65536 || good.t != 4 || good.p != 2 || len(good.salt) != 16 || len(good.hash) != 32 {
		t.Errorf("parsed fields wrong: m=%d t=%d p=%d salt=%d hash=%d",
			good.m, good.t, good.p, len(good.salt), len(good.hash))
	}
	// The edges of every cap are valid.
	for _, params := range []string{"m=262144,t=10,p=8", "m=8,t=1,p=1", "m=16,t=1,p=2"} {
		if _, err := parsePHC(build("v=19", params, salt, h32)); err != nil {
			t.Errorf("params %s refused: %v", params, err)
		}
	}
	for _, n := range []int{16, 64} {
		if _, err := parsePHC(build("v=19", ok, salt, enc.EncodeToString(make([]byte, n)))); err != nil {
			t.Errorf("hash of %d bytes refused: %v", n, err)
		}
	}

	cases := []struct {
		name  string
		input string
		field string // text the error must contain
	}{
		{"empty string", "", "fields"},
		{"missing hash field", "$argon2id$v=19$" + ok + "$" + salt, "fields"},
		{"extra field", build("v=19", ok, salt, h32) + "$x", "fields"},
		{"wrong algorithm", "$argon2i$v=19$" + ok + "$" + salt + "$" + h32, "algorithm"},
		{"no leading dollar", "argon2id$v=19$" + ok + "$" + salt + "$" + h32 + "$", "fields"},
		{"version 16", build("v=16", ok, salt, h32), "version"},
		{"version 20", build("v=20", ok, salt, h32), "version"},
		{"version not a number", build("v=x", ok, salt, h32), "version"},
		{"version label missing", build("19", ok, salt, h32), "version"},
		{"memory over cap", build("v=19", "m=262145,t=4,p=2", salt, h32), "memory"},
		{"memory 32-bit overflow", build("v=19", "m=4294967296,t=4,p=2", salt, h32), "memory"},
		{"memory not a number", build("v=19", "m=big,t=4,p=2", salt, h32), "memory"},
		{"memory below 8p", build("v=19", "m=15,t=4,p=2", salt, h32), "memory"},
		{"memory zero", build("v=19", "m=0,t=4,p=1", salt, h32), "memory"},
		{"time over cap", build("v=19", "m=65536,t=11,p=2", salt, h32), "time"},
		{"time zero", build("v=19", "m=65536,t=0,p=2", salt, h32), "time"},
		{"time negative", build("v=19", "m=65536,t=-1,p=2", salt, h32), "time"},
		{"parallelism over cap", build("v=19", "m=65536,t=4,p=9", salt, h32), "parallelism"},
		{"parallelism zero", build("v=19", "m=65536,t=4,p=0", salt, h32), "parallelism"},
		{"parallelism 8-bit overflow", build("v=19", "m=65536,t=4,p=256", salt, h32), "parallelism"},
		{"parameter missing", build("v=19", "m=65536,t=4", salt, h32), "parameters"},
		{"parameter extra", build("v=19", ok+",x=1", salt, h32), "parameters"},
		{"parameters out of order", build("v=19", "t=4,m=65536,p=2", salt, h32), "memory"},
		{"salt has padding", build("v=19", ok, salt+"==", h32), "salt"},
		{"salt not base64", build("v=19", ok, "!!!!", h32), "salt"},
		{"salt has a newline", build("v=19", ok, salt[:4]+"\n"+salt[4:], h32), "salt"},
		{"salt empty", build("v=19", ok, "", h32), "salt"},
		{"hash not base64", build("v=19", ok, salt, "***"), "hash"},
		{"hash has padding", build("v=19", ok, salt, h32+"="), "hash"},
		{"hash 15 bytes", build("v=19", ok, salt, enc.EncodeToString(make([]byte, 15))), "hash"},
		{"hash 65 bytes", build("v=19", ok, salt, enc.EncodeToString(make([]byte, 65))), "hash"},
		{"hash empty", build("v=19", ok, salt, ""), "hash"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := parsePHC(tc.input)
			if err == nil {
				t.Fatal("parsePHC accepted the input")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("error %q does not name %q", err, tc.field)
			}
		})
	}
}

func FuzzVerifyLegacy(f *testing.F) {
	// The seeds use cheap parameters. TestVerifyLegacyVectors runs the real vectors.
	const form = "test12345"
	salt := []byte("0123456789abcdef")
	enc := base64.RawStdEncoding
	key := argon2.IDKey([]byte(form), salt, 1, 8, 1, 32)
	f.Add(form, "$argon2id$v=19$m=8,t=1,p=1$"+enc.EncodeToString(salt)+"$"+enc.EncodeToString(key))

	// bcrypt at cost 4. The board writes $2y$, so the second seed uses that marker.
	bc, err := bcrypt.GenerateFromPassword([]byte(form), 4)
	if err != nil {
		f.Fatalf("bcrypt: %v", err)
	}
	f.Add(form, string(bc))
	f.Add(form, "$2y$"+string(bc[len("$2a$"):]))

	// A chain hash at cost 4 with phpass index 7, which is '5' in the alphabet.
	const setting = "5IQRaTwmf"
	digest, ok := phpassDigest(setting, form)
	if !ok {
		f.Fatal("phpass digest refused the setting")
	}
	cbc, err := bcrypt.GenerateFromPassword([]byte(digest), 4)
	if err != nil {
		f.Fatalf("bcrypt: %v", err)
	}
	f.Add(form, chainPrefix+setting+"$2y$"+string(cbc[len("$2a$"):]))

	f.Add("x", "$chain$phpass,")
	f.Add("x", "$chain$phpass,12345678$2")
	f.Add("x", "$argon2id$v=19$m=8,t=1,p=1$AAAAAAAAAAAAAAAA$AAAAAAAAAAAAAAAAAAAAAA")
	f.Add("\xff", "$2y$04$")
	f.Fuzz(func(t *testing.T, raw, stored string) {
		_ = VerifyLegacy(raw, stored)
	})
}
