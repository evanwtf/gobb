package avatar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/webp"

	"github.com/evanwtf/gobb/internal/login"
)

// bitWriter writes bits least significant first, as VP8L reads them.
type bitWriter struct {
	buf  []byte
	nbit int
}

func (w *bitWriter) put(v uint32, n int) {
	for i := range n {
		if w.nbit%8 == 0 {
			w.buf = append(w.buf, 0)
		}
		if v>>uint(i)&1 == 1 {
			w.buf[len(w.buf)-1] |= 1 << uint(w.nbit%8)
		}
		w.nbit++
	}
}

// losslessWebP builds a WebP of w x h whose pixels are all 0 (transparent black):
// a VP8L stream with no transform and five prefix codes of one symbol each, so a
// pixel costs no bit. A file of a few dozen bytes can declare 16,000,000 pixels,
// as a hostile one does.
func losslessWebP(w, h int) []byte {
	var b bitWriter
	b.put(0x2f, 8)
	b.put(uint32(w-1), 14)
	b.put(uint32(h-1), 14)
	b.put(0, 1) // alpha is not used
	b.put(0, 3) // version
	b.put(0, 1) // no transform
	b.put(0, 1) // no color cache
	b.put(0, 1) // no meta prefix codes
	for range 5 {
		b.put(1, 1) // a simple code
		b.put(0, 1) // one symbol
		b.put(0, 1) // a one-bit symbol
		b.put(0, 1) // the symbol 0
	}
	chunk := append([]byte("VP8L"), le32(len(b.buf))...)
	chunk = append(chunk, b.buf...)
	if len(b.buf)%2 == 1 {
		chunk = append(chunk, 0)
	}
	body := append([]byte("WEBP"), chunk...)
	return append(append([]byte("RIFF"), le32(len(body))...), body...)
}

// withByte returns a copy of b with b[i] set to v.
func withByte(b []byte, i int, v byte) []byte {
	out := bytes.Clone(b)
	out[i] = v
	return out
}

func le32(n int) []byte { return []byte{byte(n), byte(n >> 8), byte(n >> 16), byte(n >> 24)} }

// The smallest WebP files of the three kinds, written out here: a lossy image of
// 1 x 1, the same with an alpha plane, and an animation. The text is base64.
const (
	lossyWebP64     = "UklGRiIAAABXRUJQVlA4IBYAAAAwAQCdASoBAAEADsD+JaQAA3AAAAAA"
	lossyAlphaWebP  = "UklGRkoAAABXRUJQVlA4WAoAAAAQAAAAAAAAAAAAQUxQSAwAAAARBxAR/Q9ERP8DAABWUDggGAAAABQBAJ0BKgEAAQAAAP4AAA3AAP7mtQAAAA=="
	animatedWebP64  = "UklGRlIAAABXRUJQVlA4WAoAAAASAAAAAAAAAAAAQU5JTQYAAP////9BTk1GJgAAAAAAAAAAAAAAAAAAAGQAAABWUDhMDQAAAC8AAAAQBxAREYiI/gcA"
	losslessSmall64 = "UklGRhoAAABXRUJQVlA4TA0AAAAvAAAAEAcQERGIiP4HAA=="
)

func fromBase64(t *testing.T, s string) []byte {
	t.Helper()
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func pngOf(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i], img.Pix[i+3] = 200, 255
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func jpegOf(t *testing.T, w, h int) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := jpeg.Encode(&b, image.NewRGBA(image.Rect(0, 0, w, h)), nil); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestCheckHeaderAccepts(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		ext  string
		w, h int
	}{
		{"png", pngOf(t, 30, 10), "png", 30, 10},
		{"jpeg", jpegOf(t, 24, 8), "jpg", 24, 8},
		{"webp lossless", losslessWebP(12, 5), "png", 12, 5},
		{"webp lossless, written by an encoder", fromBase64(t, losslessSmall64), "png", 1, 1},
		{"webp lossy", fromBase64(t, lossyWebP64), "png", 1, 1},
		{"webp lossy with alpha", fromBase64(t, lossyAlphaWebP), "png", 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// The fixture is a real file of its format: the library decodes it.
			if strings.HasPrefix(tc.name, "webp") {
				if _, err := webp.Decode(bytes.NewReader(tc.data)); err != nil {
					t.Fatalf("setup: the fixture does not decode: %v", err)
				}
			}
			h, err := CheckHeader(context.Background(), bytes.NewReader(tc.data), login.NewGate(1<<20), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			if h.Ext != tc.ext || h.Width != tc.w || h.Height != tc.h {
				t.Errorf("got %q, %dx%d; want %q, %dx%d", h.Ext, h.Width, h.Height, tc.ext, tc.w, tc.h)
			}
			// Data is a new file of the format of Ext, with the same size.
			var cfg image.Config
			var err2 error
			if tc.ext == "jpg" {
				cfg, err2 = jpeg.DecodeConfig(bytes.NewReader(h.Data))
			} else {
				cfg, err2 = png.DecodeConfig(bytes.NewReader(h.Data))
			}
			if err2 != nil || cfg.Width != tc.w || cfg.Height != tc.h {
				t.Errorf("Data is not a %s of %dx%d: %v", tc.ext, tc.w, tc.h, err2)
			}
		})
	}
}

func TestCheckHeaderRefuses(t *testing.T) {
	var gb bytes.Buffer
	if err := gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 4, 4), color.Palette{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	good := pngOf(t, 20, 20)
	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"a text file named like an image", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), ErrFormat},
		{"gif", gb.Bytes(), ErrFormat},
		{"an empty file", nil, ErrFormat},
		{"a PNG cut short", good[:len(good)/2], ErrFormat},
		{"a RIFF file that is not WebP", append([]byte("RIFF\x04\x00\x00\x00WAVE"), 0, 0, 0, 0), ErrFormat},
		{"a WebP with a bad VP8L signature", withByte(losslessWebP(4, 4), 20, 0), ErrFormat},
		{"a WebP cut short", losslessWebP(4, 4)[:24], ErrFormat},
		{"an animated WebP", fromBase64(t, animatedWebP64), ErrFormat},
		{"a file over 1 MiB", append(pngOf(t, 20, 20), make([]byte, MaxBytes)...), ErrTooLarge},
		{"a file of exactly 1 MiB + 1", append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, MaxBytes+1-8)...), ErrTooLarge},
		{"a lossless WebP over 8,000 pixels wide", losslessWebP(8001, 10), ErrDimensions},
		{"a lossless WebP over 16,000,000 pixels", losslessWebP(4001, 4001), ErrDimensions},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := CheckHeader(context.Background(), bytes.NewReader(tc.data), login.NewGate(1<<20), time.Second)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
	// A file of exactly 1 MiB is not too large.
	data := append(pngOf(t, 20, 20), make([]byte, MaxBytes)...)[:MaxBytes]
	if _, err := CheckHeader(context.Background(), bytes.NewReader(data), login.NewGate(1<<20), time.Second); errors.Is(err, ErrTooLarge) {
		t.Errorf("1 MiB exactly: %v", err)
	}
}

// Make still refuses WebP: only the header image takes it.
func TestMakeStillRefusesWebP(t *testing.T) {
	_, err := Make(context.Background(), bytes.NewReader(losslessWebP(8, 8)), login.NewGate(1<<20), time.Second)
	if !errors.Is(err, ErrFormat) {
		t.Errorf("Make of a WebP = %v, want ErrFormat", err)
	}
}

func TestCheckHeaderGate(t *testing.T) {
	data := losslessWebP(2000, 2000)
	_, kib, err := inspectAs(data, fmtWebP, MaxSide)
	if err != nil {
		t.Fatal(err)
	}
	if want := uint64(2000*2000*8)/1024 + 1024; kib != want {
		t.Errorf("charge = %d KiB, want %d", kib, want)
	}
	// The whole budget is less than the charge: refused with no wait.
	small := login.NewGate(kib - 1)
	if _, err := CheckHeader(context.Background(), bytes.NewReader(data), small, time.Second); !errors.Is(err, ErrTooMuchMemory) {
		t.Errorf("a gate too small: %v, want ErrTooMuchMemory", err)
	}
	// A busy gate gives ErrBusy when the wait ends.
	g := login.NewGate(kib)
	rel, err := g.Acquire(context.Background(), kib, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckHeader(context.Background(), bytes.NewReader(data), g, 20*time.Millisecond); !errors.Is(err, login.ErrBusy) {
		t.Errorf("a busy gate: %v, want ErrBusy", err)
	}
	rel()
	// The gate is free again after a check, and after a refusal.
	for _, d := range [][]byte{data, []byte("not an image")} {
		if _, err := CheckHeader(context.Background(), bytes.NewReader(d), g, time.Second); err != nil && !errors.Is(err, ErrFormat) {
			t.Fatal(err)
		}
	}
	rel, err = g.Acquire(context.Background(), kib, 0)
	if err != nil {
		t.Errorf("the gate is not free after the checks: %v", err)
	} else {
		rel()
	}
}

// The charge of a WebP covers what the decoder allocates. A lossless file of the
// largest legal size costs as much as any other, and a few dozen bytes declare it.
func TestWebPChargeCoversTheDecode(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"lossless 2000 x 2000", losslessWebP(2000, 2000)},
		{"lossless 4000 x 4000", losslessWebP(4000, 4000)},
		{"lossless 8000 x 2000", losslessWebP(8000, 2000)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f, kib, err := inspectAs(tc.data, sniffHeader(tc.data), MaxSide)
			if err != nil || f != fmtWebP {
				t.Fatalf("inspect: %v %v", f, err)
			}
			cfg, _ := webp.DecodeConfig(bytes.NewReader(tc.data))
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			if _, err := webp.Decode(bytes.NewReader(tc.data)); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&m1)
			got := m1.TotalAlloc - m0.TotalAlloc
			px := float64(cfg.Width * cfg.Height)
			t.Logf("allocated %d bytes (%.2f B/px); charged %d bytes (%.2f B/px)", got, float64(got)/px, kib*1024, float64(kib*1024)/px)
			if got > kib*1024 {
				t.Errorf("the decode allocated %d bytes, more than the charge of %d bytes", got, kib*1024)
			}
		})
	}
	// The charge by model: 2 bytes a pixel for lossy with no alpha, 8 for the rest.
	px := uint64(1600 * 1600)
	for _, tc := range []struct {
		name  string
		model color.Model
		per   uint64
	}{
		{"lossy", color.YCbCrModel, 2},
		{"lossy with alpha", color.NYCbCrAModel, 8},
		{"lossless", color.NRGBAModel, 8},
	} {
		got := chargeKiB(fmtWebP, image.Config{ColorModel: tc.model, Width: 1600, Height: 1600}, false)
		if want := px*tc.per/1024 + 1024; got != want {
			t.Errorf("%s: charge = %d KiB, want %d", tc.name, got, want)
		}
	}
}

func TestCheckHeaderDoesNotRetainTheReader(t *testing.T) {
	// A reader that gives more than 1 MiB is read to 1 MiB + 1 and no more.
	r := &countingReader{r: strings.NewReader(strings.Repeat("x", 3*MaxBytes))}
	if _, err := CheckHeader(context.Background(), r, login.NewGate(1<<20), time.Second); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("error = %v", err)
	}
	if r.n > MaxBytes+1 {
		t.Errorf("read %d bytes, want at most %d", r.n, MaxBytes+1)
	}
}

type countingReader struct {
	r interface{ Read([]byte) (int, error) }
	n int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += n
	return n, err
}

// TestCheckHeaderDropsMetadata: a text chunk of a PNG and an APP1 segment of a
// JPEG (where EXIF lives) do not reach the stored file.
func TestCheckHeaderDropsMetadata(t *testing.T) {
	const marker = "GPS 40.0 -74.0 secret-place"
	p := pngOf(t, 16, 8)
	// A tEXt chunk after the IHDR chunk (8 signature + 25 IHDR bytes).
	chunk := []byte("tEXt" + "Comment\x00" + marker)
	var withText bytes.Buffer
	withText.Write(p[:33])
	var n [4]byte
	binary.BigEndian.PutUint32(n[:], uint32(len(chunk)-4))
	withText.Write(n[:])
	withText.Write(chunk)
	binary.BigEndian.PutUint32(n[:], crc32.ChecksumIEEE(chunk))
	withText.Write(n[:])
	withText.Write(p[33:])
	j := jpegOf(t, 16, 8)
	app1 := append([]byte{0xFF, 0xE1, 0, byte(2 + len("Exif\x00\x00"+marker))}, []byte("Exif\x00\x00"+marker)...)
	withExif := append(append(append([]byte{}, j[:2]...), app1...), j[2:]...)
	for name, data := range map[string][]byte{"png": withText.Bytes(), "jpeg": withExif} {
		if !bytes.Contains(data, []byte(marker)) {
			t.Fatalf("%s: setup: the marker is not in the upload", name)
		}
		h, err := CheckHeader(context.Background(), bytes.NewReader(data), login.NewGate(1<<20), time.Second)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if bytes.Contains(h.Data, []byte(marker)) {
			t.Errorf("%s: the stored file keeps the metadata", name)
		}
	}
}
