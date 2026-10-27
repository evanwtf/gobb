package avatar_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"testing"

	"github.com/evanwtf/gobb/internal/avatar"
	"github.com/evanwtf/gobb/internal/login"
)

// Icon keeps the size and the shape of the image, and the transparency.
func TestIconKeepsSize(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 30, 12))
	fill(img, color.NRGBA{R: 10, G: 200, B: 30, A: 255})
	img.SetNRGBA(0, 0, color.NRGBA{}) // one clear pixel
	var in bytes.Buffer
	if err := png.Encode(&in, img); err != nil {
		t.Fatal(err)
	}
	out, err := avatar.Icon(context.Background(), &in, gate(), wait)
	if err != nil {
		t.Fatal(err)
	}
	got := decode(t, out)
	if b := got.Bounds(); b.Dx() != 30 || b.Dy() != 12 {
		t.Errorf("size = %dx%d, want 30x12", b.Dx(), b.Dy())
	}
	if _, _, _, a := got.At(0, 0).RGBA(); a != 0 {
		t.Errorf("the clear pixel has alpha %d", a)
	}
	if r, g, b, a := got.At(5, 5).RGBA(); r>>8 != 10 || g>>8 != 200 || b>>8 != 30 || a>>8 != 255 {
		t.Errorf("pixel = %d,%d,%d,%d", r>>8, g>>8, b>>8, a>>8)
	}
}

// Icon takes the three formats, and gives a PNG for each.
func TestIconFormats(t *testing.T) {
	var jb bytes.Buffer
	ji := image.NewRGBA(image.Rect(0, 0, 20, 20))
	fill(ji, color.RGBA{R: 200, G: 30, B: 30, A: 255})
	if err := jpeg.Encode(&jb, ji, nil); err != nil {
		t.Fatal(err)
	}
	var gb bytes.Buffer
	if err := gif.Encode(&gb, image.NewPaletted(image.Rect(0, 0, 20, 20), []color.Color{color.Black, color.White}), nil); err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{"png": pngBytes(t, 20, 20, color.White), "jpeg": jb.Bytes(), "gif": gb.Bytes()} {
		out, err := avatar.Icon(context.Background(), bytes.NewReader(data), gate(), wait)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if b := decode(t, out).Bounds(); b.Dx() != 20 || b.Dy() != 20 {
			t.Errorf("%s: size = %dx%d", name, b.Dx(), b.Dy())
		}
	}
}

// Icon refuses a side over 512 pixels from the header alone, a file that is not
// an image, and a file over 1 MiB. It drops metadata like Make.
func TestIconRefuses(t *testing.T) {
	over := make([]byte, 1<<20+1)
	copy(over, pngBytes(t, 8, 8, color.White))
	for _, tc := range []struct {
		name string
		data []byte
		want error
	}{
		{"513 wide", pngHeader(513, 10), avatar.ErrDimensions},
		{"512 wide is fine to the header", pngHeader(512, 10), avatar.ErrFormat}, // header only: the body is missing
		{"text", []byte("<svg xmlns='http://www.w3.org/2000/svg'/>"), avatar.ErrFormat},
		{"too large", over, avatar.ErrTooLarge},
		{"empty", nil, avatar.ErrFormat},
	} {
		if _, err := avatar.Icon(context.Background(), bytes.NewReader(tc.data), gate(), wait); !errors.Is(err, tc.want) {
			t.Errorf("%s: error = %v, want %v", tc.name, err, tc.want)
		}
	}
	withTrail := append(pngBytes(t, 30, 30, color.White), "<script>TRAILER</script>"...)
	out, err := avatar.Icon(context.Background(), bytes.NewReader(withTrail), gate(), wait)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(out, []byte("TRAILER")) {
		t.Error("the output holds the trailing bytes")
	}
}

// Icon takes its memory from the gate and gives it back.
func TestIconUsesTheGate(t *testing.T) {
	g := login.NewGate(2048)
	rel, err := g.Acquire(context.Background(), 1025, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := avatar.Icon(context.Background(), bytes.NewReader(pngBytes(t, 20, 20, color.White)), g, wait); !errors.Is(err, login.ErrBusy) {
		t.Errorf("error = %v, want login.ErrBusy while the gate is held", err)
	}
	rel()
	for range 3 {
		if _, err := avatar.Icon(context.Background(), bytes.NewReader(pngBytes(t, 20, 20, color.White)), g, wait); err != nil {
			t.Fatal(err)
		}
	}
}
