package avatar_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"hash/crc32"
	"image"
	"image/color"
	"image/color/palette"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"strings"
	"testing"
	"time"

	"github.com/evanwtf/gobb/internal/avatar"
	"github.com/evanwtf/gobb/internal/login"
)

const wait = 10 * time.Millisecond

// gate has room for any legal decode.
func gate() *login.Gate { return login.NewGate(1 << 30) }

func fill(img draw.Image, c color.Color) {
	draw.Draw(img, img.Bounds(), image.NewUniform(c), image.Point{}, draw.Src)
}

func pngBytes(t *testing.T, w, h int, c color.Color) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	fill(img, c)
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func decode(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatalf("output is not a PNG: %v", err)
	}
	return img
}

func TestMakeFormats(t *testing.T) {
	var jb bytes.Buffer
	ji := image.NewRGBA(image.Rect(0, 0, 40, 40))
	fill(ji, color.RGBA{200, 30, 30, 255})
	if err := jpeg.Encode(&jb, ji, nil); err != nil {
		t.Fatal(err)
	}
	var gb bytes.Buffer
	gi := image.NewPaletted(image.Rect(0, 0, 64, 64), palette.Plan9)
	fill(gi, color.RGBA{0, 0, 255, 255})
	if err := gif.Encode(&gb, gi, nil); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name string
		data []byte
	}{
		{"png 300x200", pngBytes(t, 300, 200, color.RGBA{10, 200, 10, 255})},
		{"jpeg 40x40", jb.Bytes()},
		{"gif 64x64", gb.Bytes()},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := avatar.Make(context.Background(), bytes.NewReader(tc.data), gate(), wait)
			if err != nil {
				t.Fatal(err)
			}
			img := decode(t, out)
			if b := img.Bounds(); b.Dx() != 128 || b.Dy() != 128 || b.Min != (image.Point{}) {
				t.Errorf("output bounds = %v, want 128x128 at the origin", b)
			}
		})
	}
}

// A one-color image keeps its color at every pixel, shrunk or grown.
func TestMakeKeepsColor(t *testing.T) {
	for _, size := range [][2]int{{300, 200}, {40, 40}, {128, 128}, {1, 1}} {
		out, err := avatar.Make(context.Background(), bytes.NewReader(pngBytes(t, size[0], size[1], color.RGBA{12, 34, 56, 255})), gate(), wait)
		if err != nil {
			t.Fatal(err)
		}
		img := decode(t, out)
		for _, p := range []image.Point{{0, 0}, {127, 127}, {64, 3}, {5, 120}} {
			r, g, b, a := img.At(p.X, p.Y).RGBA()
			if r>>8 != 12 || g>>8 != 34 || b>>8 != 56 || a>>8 != 255 {
				t.Errorf("%v: pixel %v = %d,%d,%d,%d", size, p, r>>8, g>>8, b>>8, a>>8)
			}
		}
	}
}

// The crop is the center square: a 300 x 100 image with red at the left and
// right thirds and blue in the middle third gives all blue.
func TestMakeCropsCenter(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 300, 100))
	fill(src, color.RGBA{255, 0, 0, 255})
	draw.Draw(src, image.Rect(100, 0, 200, 100), image.NewUniform(color.RGBA{0, 0, 255, 255}), image.Point{}, draw.Src)
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		t.Fatal(err)
	}
	out, err := avatar.Make(context.Background(), &b, gate(), wait)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	for _, p := range []image.Point{{0, 0}, {127, 0}, {0, 127}, {127, 127}, {64, 64}} {
		r, _, bl, _ := img.At(p.X, p.Y).RGBA()
		if r != 0 || bl>>8 != 255 {
			t.Errorf("pixel %v = r%d b%d, want blue", p, r>>8, bl>>8)
		}
	}
}

// A shrink averages: a 256x256 checkerboard of black and white 1-pixel cells
// gives mid gray. A growth takes the nearest pixel: a 2x2 image of four colors
// gives four solid quadrants.
func TestMakeResample(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 256, 256))
	for y := range 256 {
		for x := range 256 {
			if (x+y)%2 == 0 {
				src.Set(x, y, color.White)
			} else {
				src.Set(x, y, color.Black)
			}
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, src); err != nil {
		t.Fatal(err)
	}
	out, err := avatar.Make(context.Background(), &b, gate(), wait)
	if err != nil {
		t.Fatal(err)
	}
	r, _, _, _ := decode(t, out).At(50, 50).RGBA()
	if v := r >> 8; v < 126 || v > 129 {
		t.Errorf("average of a checkerboard = %d, want about 127", v)
	}

	four := image.NewRGBA(image.Rect(0, 0, 2, 2))
	cols := []color.RGBA{{255, 0, 0, 255}, {0, 255, 0, 255}, {0, 0, 255, 255}, {255, 255, 0, 255}}
	four.Set(0, 0, cols[0])
	four.Set(1, 0, cols[1])
	four.Set(0, 1, cols[2])
	four.Set(1, 1, cols[3])
	b.Reset()
	if err := png.Encode(&b, four); err != nil {
		t.Fatal(err)
	}
	out, err = avatar.Make(context.Background(), &b, gate(), wait)
	if err != nil {
		t.Fatal(err)
	}
	img := decode(t, out)
	for i, p := range []image.Point{{10, 10}, {120, 10}, {10, 120}, {120, 120}} {
		r, g, bl, _ := img.At(p.X, p.Y).RGBA()
		got := color.RGBA{uint8(r >> 8), uint8(g >> 8), uint8(bl >> 8), 255}
		if got != cols[i] {
			t.Errorf("quadrant %d = %v, want %v", i, got, cols[i])
		}
	}
}

// pngHeader builds a signature and an IHDR chunk with a correct CRC, and no more.
func pngHeader(w, h uint32) []byte {
	var b bytes.Buffer
	b.WriteString("\x89PNG\r\n\x1a\n")
	ihdr := make([]byte, 13)
	binary.BigEndian.PutUint32(ihdr[0:], w)
	binary.BigEndian.PutUint32(ihdr[4:], h)
	ihdr[8], ihdr[9] = 8, 2 // 8 bits, RGB
	_ = binary.Write(&b, binary.BigEndian, uint32(13))
	chunk := append([]byte("IHDR"), ihdr...)
	b.Write(chunk)
	_ = binary.Write(&b, binary.BigEndian, crc32.ChecksumIEEE(chunk))
	return b.Bytes()
}

func TestMakeRefusesHugeBeforeDecode(t *testing.T) {
	g := login.NewGate(1) // a budget of 1 KiB: any acquire for a decode would wait
	for _, d := range [][2]uint32{{50000, 50000}, {8001, 10}, {10, 8001}, {5000, 5000}} {
		data := pngHeader(d[0], d[1])
		if len(data) >= 1024 {
			t.Fatalf("test input is %d bytes", len(data))
		}
		_, err := avatar.Make(context.Background(), bytes.NewReader(data), g, wait)
		if !errors.Is(err, avatar.ErrDimensions) {
			t.Errorf("%dx%d: error = %v, want ErrDimensions", d[0], d[1], err)
		}
	}
	// The limits themselves pass the size check; the file has no pixels, so the
	// decode then fails as a format error.
	_, err := avatar.Make(context.Background(), bytes.NewReader(pngHeader(4000, 4000)), g, wait)
	if errors.Is(err, avatar.ErrDimensions) {
		t.Errorf("4000x4000 (16,000,000 pixels) = %v, want it past the size check", err)
	}
	// Nothing is held after the refusals: a full-budget acquire succeeds at once.
	rel, err := g.Acquire(context.Background(), 1, 0)
	if err != nil {
		t.Fatalf("the gate is held after the refusals: %v", err)
	}
	rel()
}

// A decode that does not fit the gate in the wait is busy, not an error of the file.
func TestMakeBusy(t *testing.T) {
	g := login.NewGate(1000)
	rel, err := g.Acquire(context.Background(), 1000, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_, err = avatar.Make(context.Background(), bytes.NewReader(pngBytes(t, 20, 20, color.White)), g, wait)
	if !errors.Is(err, login.ErrBusy) {
		t.Errorf("error = %v, want login.ErrBusy", err)
	}
}

// The gate is free again after each call, and the cost is at least 1024 KiB, so
// a gate of 2048 KiB runs one small decode at a time.
func TestMakeReleasesGate(t *testing.T) {
	g := login.NewGate(2048)
	for range 3 {
		if _, err := avatar.Make(context.Background(), bytes.NewReader(pngBytes(t, 20, 20, color.White)), g, wait); err != nil {
			t.Fatal(err)
		}
	}
	// A small decode costs 1024 + 20*20*4/1024 = 1025 KiB. With 1025 held, a second
	// call waits, so it is busy.
	rel, err := g.Acquire(context.Background(), 1025, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer rel()
	_, err = avatar.Make(context.Background(), bytes.NewReader(pngBytes(t, 20, 20, color.White)), g, wait)
	if !errors.Is(err, login.ErrBusy) {
		t.Errorf("error = %v, want ErrBusy while 1025 KiB are held", err)
	}
}

func TestMakeOutputIsFresh(t *testing.T) {
	// A JPEG with an APP1 segment that holds SECRET-EXIF, put after the SOI.
	var jb bytes.Buffer
	ji := image.NewRGBA(image.Rect(0, 0, 50, 50))
	fill(ji, color.RGBA{1, 2, 3, 255})
	if err := jpeg.Encode(&jb, ji, nil); err != nil {
		t.Fatal(err)
	}
	raw := jb.Bytes()
	payload := append([]byte("Exif\x00\x00"), "SECRET-EXIF"...)
	seg := []byte{0xFF, 0xE1, byte((len(payload) + 2) >> 8), byte((len(payload) + 2) & 0xFF)}
	seg = append(seg, payload...)
	withExif := append(append(append([]byte{}, raw[:2]...), seg...), raw[2:]...)
	if !bytes.Contains(withExif, []byte("SECRET-EXIF")) {
		t.Fatal("setup: no EXIF text")
	}

	// A GIF with bytes after its trailer.
	var gb bytes.Buffer
	gi := image.NewPaletted(image.Rect(0, 0, 20, 20), palette.Plan9)
	if err := gif.Encode(&gb, gi, nil); err != nil {
		t.Fatal(err)
	}
	withTail := append(append([]byte{}, gb.Bytes()...), "<script>alert(1)</script>"...)

	// A PNG with trailing bytes.
	withTrail := append(pngBytes(t, 30, 30, color.White), "<script>TRAILER</script>"...)

	for name, data := range map[string][]byte{"jpeg exif": withExif, "gif tail": withTail, "png trail": withTrail} {
		t.Run(name, func(t *testing.T) {
			out, err := avatar.Make(context.Background(), bytes.NewReader(data), gate(), wait)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(out, []byte("\x89PNG\r\n\x1a\n")) {
				t.Error("output does not start with the PNG signature")
			}
			for _, s := range []string{"SECRET-EXIF", "<script>", "TRAILER", "Exif"} {
				if bytes.Contains(out, []byte(s)) {
					t.Errorf("output holds %q", s)
				}
			}
			for _, c := range []string{"tEXt", "iTXt", "zTXt", "eXIf", "iCCP"} {
				if bytes.Contains(out, []byte(c)) {
					t.Errorf("output holds a %s chunk", c)
				}
			}
		})
	}
}

func TestMakeRefuses(t *testing.T) {
	big := pngBytes(t, 8, 8, color.White)
	tooBig := append(append([]byte{}, big...), bytes.Repeat([]byte{0}, 1<<20+1-len(big))...)
	if len(tooBig) != 1<<20+1 {
		t.Fatalf("setup: %d bytes", len(tooBig))
	}
	exactly := append(append([]byte{}, big...), bytes.Repeat([]byte{0}, 1<<20-len(big))...)

	webp := append(append([]byte("RIFF"), 0x1a, 0, 0, 0), []byte("WEBPVP8 ")...)
	webp = append(webp, make([]byte, 40)...)

	cases := []struct {
		name string
		data []byte
		want error
	}{
		{"1 MiB + 1", tooBig, avatar.ErrTooLarge},
		{"text named a.png", []byte("this is a.png, honest\n"), avatar.ErrFormat},
		{"webp", webp, avatar.ErrFormat},
		{"empty", nil, avatar.ErrFormat},
		{"png signature cut short", []byte("\x89PNG"), avatar.ErrFormat},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), avatar.ErrFormat},
		{"bmp", []byte("BM\x00\x00\x00\x00\x00\x00\x00\x00"), avatar.ErrFormat},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := avatar.Make(context.Background(), bytes.NewReader(tc.data), gate(), wait)
			if !errors.Is(err, tc.want) {
				t.Errorf("error = %v, want %v", err, tc.want)
			}
		})
	}
	// Exactly 1 MiB is not too large (trailing bytes after IEND are ignored).
	if _, err := avatar.Make(context.Background(), bytes.NewReader(exactly), gate(), wait); err != nil {
		t.Errorf("exactly 1 MiB: %v", err)
	}
	// A reader is never read far past the limit.
	r := &countReader{}
	_, err := avatar.Make(context.Background(), r, gate(), wait)
	if !errors.Is(err, avatar.ErrTooLarge) && !errors.Is(err, avatar.ErrFormat) {
		t.Errorf("endless reader: error = %v", err)
	}
	if r.n > 1<<20+1+4096 {
		t.Errorf("read %d bytes from an endless reader", r.n)
	}
}

type countReader struct{ n int }

func (c *countReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0x89
	}
	c.n += len(p)
	return len(p), nil
}

// A corrupt body that passes the header check is a format error, not a panic.
func TestMakeCorrupt(t *testing.T) {
	data := pngHeader(10, 10) // header only: no IDAT
	_, err := avatar.Make(context.Background(), bytes.NewReader(data), gate(), wait)
	if !errors.Is(err, avatar.ErrFormat) {
		t.Errorf("error = %v, want ErrFormat", err)
	}
	_, err = avatar.Make(context.Background(), strings.NewReader("\xff\xd8\xff\xe0\x00\x10JFIF"), gate(), wait)
	if !errors.Is(err, avatar.ErrFormat) {
		t.Errorf("truncated JPEG: error = %v, want ErrFormat", err)
	}
}

func TestMakeAnimatedGIF(t *testing.T) {
	red, blue := color.RGBA{255, 0, 0, 255}, color.RGBA{0, 0, 255, 255}
	pal := color.Palette{red, blue}
	f1 := image.NewPaletted(image.Rect(0, 0, 32, 32), pal)
	fill(f1, red)
	f2 := image.NewPaletted(image.Rect(0, 0, 32, 32), pal)
	fill(f2, blue)
	var b bytes.Buffer
	if err := gif.EncodeAll(&b, &gif.GIF{Image: []*image.Paletted{f1, f2}, Delay: []int{5, 5}}); err != nil {
		t.Fatal(err)
	}
	out, err := avatar.Make(context.Background(), &b, gate(), wait)
	if err != nil {
		t.Fatal(err)
	}
	r, g, bl, _ := decode(t, out).At(64, 64).RGBA()
	if r>>8 != 255 || g != 0 || bl != 0 {
		t.Errorf("pixel = %d,%d,%d, want the first frame (red)", r>>8, g>>8, bl>>8)
	}
}

// A context that has ended stops the wait for the gate.
func TestMakeContext(t *testing.T) {
	g := login.NewGate(1000)
	rel, _ := g.Acquire(context.Background(), 1000, 0)
	defer rel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := avatar.Make(ctx, bytes.NewReader(pngBytes(t, 20, 20, color.White)), g, time.Minute)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

// FuzzMake: any input gives PNG bytes of 128 x 128 or one of the named errors,
// and never a panic.
func FuzzMake(f *testing.F) {
	var jb, gb, pb bytes.Buffer
	ji := image.NewRGBA(image.Rect(0, 0, 17, 9))
	fill(ji, color.RGBA{9, 9, 9, 255})
	_ = jpeg.Encode(&jb, ji, nil)
	_ = gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 7, 11), palette.Plan9), nil)
	_ = png.Encode(&pb, ji)
	f.Add(pb.Bytes())
	f.Add(jb.Bytes())
	f.Add(gb.Bytes())
	f.Add(pngHeader(50000, 50000))
	f.Add(pngHeader(1, 1))
	f.Add([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "))
	f.Add([]byte(""))
	g := login.NewGate(64 << 10) // 64 MiB
	f.Fuzz(func(t *testing.T, data []byte) {
		out, err := avatar.Make(context.Background(), bytes.NewReader(data), g, wait)
		switch {
		case err == nil:
			img, derr := png.Decode(bytes.NewReader(out))
			if derr != nil {
				t.Fatalf("output is not a PNG: %v", derr)
			}
			if b := img.Bounds(); b.Dx() != 128 || b.Dy() != 128 {
				t.Fatalf("output is %v", b)
			}
		case errors.Is(err, avatar.ErrTooLarge), errors.Is(err, avatar.ErrFormat),
			errors.Is(err, avatar.ErrDimensions), errors.Is(err, login.ErrBusy):
		default:
			t.Fatalf("unnamed error: %v", err)
		}
	})
}
