package avatar

import (
	"bytes"
	"compress/zlib"
	"context"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"runtime"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/login"
)

// progressiveJPEG builds a progressive JPEG of w x h with the given components
// (each an h/v sampling pair) whose coefficients are all zero: a start of image,
// a table of quantization, the frame header, a table of Huffman codes with one
// code, and one scan of the DC coefficients. adobe is the transform byte of an
// Adobe segment, or -1 for none. The decoder allocates the coefficients and the
// planes from the frame header, as it does for a real file.
func progressiveJPEG(w, h int, comps [][2]int, adobe int) []byte {
	var b bytes.Buffer
	b.Write([]byte{0xFF, 0xD8})
	if adobe >= 0 {
		b.Write([]byte{0xFF, 0xEE, 0, 14, 'A', 'd', 'o', 'b', 'e', 0, 0, 0x64, 0, 0, 0, 0, byte(adobe)})
	}
	b.Write([]byte{0xFF, 0xDB, 0, 67, 0})
	b.Write(bytes.Repeat([]byte{1}, 64))
	n := len(comps)
	b.Write([]byte{0xFF, 0xC2, 0, byte(8 + 3*n), 8, byte(h >> 8), byte(h), byte(w >> 8), byte(w), byte(n)})
	for i, c := range comps {
		b.Write([]byte{byte(i + 1), byte(c[0]<<4 | c[1]), 0})
	}
	// One DC code of length 1 for the symbol 0 (a difference of size 0).
	b.Write([]byte{0xFF, 0xC4, 0, 20, 0x00, 1, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0x00})
	b.Write([]byte{0xFF, 0xDA, 0, byte(6 + 2*n), byte(n)})
	for i := range comps {
		b.Write([]byte{byte(i + 1), 0x00})
	}
	b.Write([]byte{0, 0, 0x00})
	hmax, vmax, blocks := 1, 1, 0
	for _, c := range comps {
		hmax, vmax = max(hmax, c[0]), max(vmax, c[1])
		blocks += c[0] * c[1]
	}
	mcus := ((w + 8*hmax - 1) / (8 * hmax)) * ((h + 8*vmax - 1) / (8 * vmax))
	b.Write(make([]byte, (mcus*blocks+7)/8+1)) // one 0 bit per block
	b.Write([]byte{0xFF, 0xD9})
	return b.Bytes()
}

func pngChunk(b *bytes.Buffer, typ string, data []byte) {
	_ = binary.Write(b, binary.BigEndian, uint32(len(data)))
	body := append([]byte(typ), data...)
	b.Write(body)
	_ = binary.Write(b, binary.BigEndian, crc32.ChecksumIEEE(body))
}

// interlacedPNG builds an Adam7 RGB 8-bit PNG of black pixels.
func interlacedPNG(w, h int) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9], ihdr[12] = 8, 2, 1
	pngChunk(&b, "IHDR", ihdr)
	var raw bytes.Buffer
	for _, p := range [][4]int{{0, 0, 8, 8}, {4, 0, 8, 8}, {0, 4, 4, 8}, {2, 0, 4, 4}, {0, 2, 2, 4}, {1, 0, 2, 2}, {0, 1, 1, 2}} {
		pw, ph := (w-p[0]+p[2]-1)/p[2], (h-p[1]+p[3]-1)/p[3]
		if pw <= 0 || ph <= 0 {
			continue
		}
		raw.Write(make([]byte, ph*(1+3*pw))) // filter type 0, black pixels
	}
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	_, _ = zw.Write(raw.Bytes())
	_ = zw.Close()
	pngChunk(&b, "IDAT", z.Bytes())
	pngChunk(&b, "IEND", nil)
	return b.Bytes()
}

// bands fills img with vertical bands, so a file of about 2 Mpx stays under the
// 1 MiB limit.
func bandColor(x int) uint8 { return uint8(x / 8 * 3) }

func encodePNG(t *testing.T, img image.Image) []byte {
	t.Helper()
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func bandedRGBA(w, h int) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			v := bandColor(x)
			o := img.PixOffset(x, y)
			img.Pix[o], img.Pix[o+1], img.Pix[o+2], img.Pix[o+3] = v, v/2, 255-v, 255
		}
	}
	return img
}

// The charge of each format is at least the memory that Make allocates after the
// read (decodeSquare: the header, the decode, the resample; the encode runs after the gate share is back). The upload
// itself, up to 1 MiB, is the 1024 KiB of the charge that does not depend on the
// pixels, so the test builds data before it starts to count.
// The charge is at least the memory for an
// image of about 2 Mpx (R14). Total allocated bounds the live peak, so the check
// is conservative. The test logs the measured bytes a pixel next to the charged
// one. Nothing in this test runs in parallel, so the counter is the call's own.
func TestChargeCoversAllocation(t *testing.T) {
	const w, h = 1601, 1249 // about 2 Mpx, and not a multiple of 16

	var jb bytes.Buffer
	if err := jpeg.Encode(&jb, bandedRGBA(w, h), nil); err != nil {
		t.Fatal(err)
	}
	grayImg := image.NewGray(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			grayImg.Pix[y*grayImg.Stride+x] = bandColor(x)
		}
	}
	var gj bytes.Buffer
	if err := jpeg.Encode(&gj, grayImg, nil); err != nil {
		t.Fatal(err)
	}
	rgba64 := image.NewRGBA64(image.Rect(0, 0, w, h))
	for y := range h {
		for x := range w {
			v := uint16(bandColor(x)) * 257
			rgba64.SetRGBA64(x, y, color.RGBA64{v, v / 2, 65535 - v, 65535})
		}
	}
	pal := image.NewPaletted(image.Rect(0, 0, w, h), color.Palette(func() color.Palette {
		p := make(color.Palette, 256)
		for i := range p {
			p[i] = color.RGBA{uint8(i), uint8(i / 2), 255 - uint8(i), 255}
		}
		return p
	}()))
	for y := range h {
		for x := range w {
			pal.Pix[y*pal.Stride+x] = bandColor(x)
		}
	}
	var gb bytes.Buffer
	if err := gif.Encode(&gb, pal, nil); err != nil {
		t.Fatal(err)
	}

	s11 := [][2]int{{1, 1}, {1, 1}, {1, 1}}
	cases := []struct {
		name string
		data []byte
	}{
		{"jpeg baseline YCbCr 4:2:0", jb.Bytes()},
		{"jpeg progressive YCbCr 4:2:0", progressiveJPEG(w, h, [][2]int{{2, 2}, {1, 1}, {1, 1}}, -1)},
		{"jpeg progressive YCbCr 4:4:4", progressiveJPEG(w, h, s11, -1)},
		{"jpeg progressive RGB (Adobe)", progressiveJPEG(w, h, s11, 0)},
		{"jpeg progressive CMYK", progressiveJPEG(w, h, [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, 0)},
		{"jpeg gray", gj.Bytes()},
		{"png 8-bit", encodePNG(t, bandedRGBA(w, h))},
		{"png 16-bit", encodePNG(t, rgba64)},
		{"png interlaced 8-bit", interlacedPNG(w, h)},
		{"gif", gb.Bytes()},
	}
	gate := login.NewGate(1 << 20)
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if len(tc.data) > MaxBytes {
				t.Fatalf("setup: the file is %d bytes", len(tc.data))
			}
			_, kib, err := inspect(tc.data)
			if err != nil {
				t.Fatal(err)
			}
			var m0, m1 runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&m0)
			if _, err := decodeSquare(context.Background(), tc.data, gate, time.Second); err != nil {
				t.Fatal(err)
			}
			runtime.ReadMemStats(&m1)
			got := m1.TotalAlloc - m0.TotalAlloc
			px := float64(w * h)
			t.Logf("allocated %d bytes (%.2f B/px); charged %d bytes (%.2f B/px)", got, float64(got)/px, kib*1024, float64(kib*1024)/px)
			if got > kib*1024 {
				t.Errorf("decodeSquare allocated %d bytes, more than the charge of %d bytes", got, kib*1024)
			}
		})
	}
}

// The model of a JPEG selects its charge, and a PNG that is interlaced costs
// twice. A hand-built header shows each, with no decode.
func TestChargeByHeader(t *testing.T) {
	const w, h = 1600, 1600
	px := uint64(w * h)
	kib := func(perPixel, num, den uint64) uint64 { return px*perPixel*num/den/1024 + 1024 }
	cases := []struct {
		name string
		data []byte
		want uint64
	}{
		{"jpeg YCbCr", progressiveJPEG(w, h, [][2]int{{1, 1}, {1, 1}, {1, 1}}, -1), kib(15, 1, 1)},
		{"jpeg RGB", progressiveJPEG(w, h, [][2]int{{1, 1}, {1, 1}, {1, 1}}, 0), kib(19, 1, 1)},
		{"jpeg CMYK", progressiveJPEG(w, h, [][2]int{{1, 1}, {1, 1}, {1, 1}, {1, 1}}, -1), kib(24, 1, 1)},
		{"jpeg gray", progressiveJPEG(w, h, [][2]int{{1, 1}}, -1), kib(5, 1, 1)},
		{"png 8-bit", interlacedPNGFlag(w, h, 8, 0), kib(4, 1, 1)},
		{"png 16-bit", interlacedPNGFlag(w, h, 16, 0), kib(8, 1, 1)},
		{"png 8-bit interlaced", interlacedPNGFlag(w, h, 8, 1), kib(4, 2, 1)},
		{"png 16-bit interlaced", interlacedPNGFlag(w, h, 16, 1), kib(8, 2, 1)},
	}
	for _, tc := range cases {
		_, got, err := inspect(tc.data)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got != tc.want {
			t.Errorf("%s: charge = %d KiB, want %d", tc.name, got, tc.want)
		}
	}
	// A side is rounded up to a multiple of 16 for a JPEG: 1601 x 1601 charges as 1616 x 1616.
	_, got, err := inspect(progressiveJPEG(1601, 1601, [][2]int{{1, 1}, {1, 1}, {1, 1}}, -1))
	if want := uint64(1616*1616*15)/1024 + 1024; err != nil || got != want {
		t.Errorf("padded jpeg: charge = %d KiB (%v), want %d", got, err, want)
	}
}

// interlacedPNGFlag is a header only: the signature and an IHDR of RGB (or gray
// for 16 bits) with the given depth and interlace byte.
func interlacedPNGFlag(w, h int, depth, interlace byte) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], uint32(w))
	binary.BigEndian.PutUint32(ihdr[4:], uint32(h))
	ihdr[8], ihdr[9], ihdr[12] = depth, 2, interlace
	pngChunk(&b, "IHDR", ihdr)
	return b.Bytes()
}
