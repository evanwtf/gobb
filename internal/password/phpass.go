package password

import (
	"crypto/md5"
	"strings"
)

const phpassAlphabet = "./0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"

// phpassSettingLen is the length of a portable-hash setting: one count character
// and eight salt characters.
const phpassSettingLen = 9

// phpassDigest computes the 22-character portable-hash digest of input.
// It follows the public phpass portable-hash description. The first setting
// character gives the iteration count as a power of two, from 2^7 to 2^30. The
// next eight characters are the salt. It returns false when the setting is bad.
func phpassDigest(setting, input string) (string, bool) {
	if len(setting) != phpassSettingLen {
		return "", false
	}
	logCount := strings.IndexByte(phpassAlphabet, setting[0])
	if logCount < 7 || logCount > 30 {
		return "", false
	}
	salt := setting[1:phpassSettingLen]

	sum := md5.Sum([]byte(salt + input))
	buf := make([]byte, md5.Size+len(input))
	copy(buf[md5.Size:], input)
	for count := uint32(1) << logCount; count > 0; count-- {
		copy(buf, sum[:])
		sum = md5.Sum(buf)
	}
	return phpassEncode(sum[:]), true
}

// phpassEncode writes src in the portable-hash encoding: each group of up to
// three bytes is read as a little-endian number and written six bits at a time
// from the low end. A group of n bytes gives n+1 characters.
func phpassEncode(src []byte) string {
	var out strings.Builder
	for i := 0; i < len(src); i += 3 {
		var v uint32
		n := min(3, len(src)-i)
		for j := 0; j < n; j++ {
			v |= uint32(src[i+j]) << (8 * j)
		}
		for j := 0; j <= n; j++ {
			out.WriteByte(phpassAlphabet[v&0x3f])
			v >>= 6
		}
	}
	return out.String()
}
