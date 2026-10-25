// Package avatar makes the stored avatar from an upload.
//
// An upload is untrusted. [Make] reads at most 1 MiB, finds the format from the
// first bytes (never from a name or a content type), checks the size from the
// header before it allocates a pixel, takes memory from the shared gate for the
// decode, and then draws a new 128 x 128 image and encodes it with the standard
// PNG encoder. No byte of the upload reaches the output except as a pixel, so
// metadata, comments and trailing data do not survive.
package avatar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"time"

	"github.com/evanwtf/gobb/internal/login"
)

// The limits of an upload and the size of the result.
const (
	// MaxBytes is the largest upload, in bytes (1 MiB).
	MaxBytes = 1 << 20
	// MaxSide is the largest width or height, in pixels.
	MaxSide = 8000
	// MaxPixels is the largest width times height.
	MaxPixels = 16_000_000
	// Size is the width and the height of the result, in pixels.
	Size = 128
)

var (
	// ErrTooLarge means the upload is longer than [MaxBytes].
	ErrTooLarge = errors.New("avatar: the file is larger than 1 MiB")
	// ErrFormat means the file is not a PNG, a JPEG or a GIF that decodes. The
	// format comes from the first bytes of the file.
	ErrFormat = errors.New("avatar: not a PNG, JPEG or GIF image")
	// ErrDimensions means a side is over [MaxSide], or the pixels are over
	// [MaxPixels].
	ErrDimensions = errors.New("avatar: the image is too large in pixels")
)

type format int

const (
	fmtNone format = iota
	fmtPNG
	fmtJPEG
	fmtGIF
)

// sniff names the format by the first bytes: the PNG signature, the JPEG start of
// image marker followed by a marker prefix, or GIF87a or GIF89a.
func sniff(b []byte) format {
	switch {
	case bytes.HasPrefix(b, []byte("\x89PNG\r\n\x1a\n")):
		return fmtPNG
	case bytes.HasPrefix(b, []byte{0xFF, 0xD8, 0xFF}):
		return fmtJPEG
	case bytes.HasPrefix(b, []byte("GIF87a")), bytes.HasPrefix(b, []byte("GIF89a")):
		return fmtGIF
	}
	return fmtNone
}

// Make returns the PNG bytes of the avatar for the image that r holds.
//
//  1. It reads at most [MaxBytes] + 1 bytes, and returns [ErrTooLarge] for more.
//  2. It finds the format from the first bytes, and returns [ErrFormat] for
//     anything but PNG, JPEG and GIF (WebP and SVG included).
//  3. It reads the size from the header only, and returns [ErrDimensions] when a
//     side is over [MaxSide] or the pixels are over [MaxPixels].
//  4. It takes width x height x 4 / 1024 + 1024 KiB from gate, waiting up to
//     wait, and returns [login.ErrBusy] when the wait ends (or the error of ctx
//     when ctx ends first). It gives the memory back before it returns.
//  5. It decodes the image (the first frame of a GIF), crops the center square,
//     and resamples it to [Size] x [Size]: an area average when it shrinks, the
//     nearest pixel when it grows.
//  6. It encodes the result with the standard PNG encoder.
//
// A file whose header is sound but whose body does not decode gives [ErrFormat].
func Make(ctx context.Context, r io.Reader, gate *login.Gate, wait time.Duration) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("avatar: read the upload: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, ErrTooLarge
	}
	f := sniff(data)
	if f == fmtNone {
		return nil, ErrFormat
	}

	var cfg image.Config
	switch f {
	case fmtPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case fmtJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case fmtGIF:
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return nil, ErrFormat
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide || cfg.Width*cfg.Height > MaxPixels {
		return nil, ErrDimensions
	}

	kib := uint64(cfg.Width)*uint64(cfg.Height)*4/1024 + 1024
	release, err := gate.Acquire(ctx, kib, wait)
	if err != nil {
		return nil, err
	}
	defer release()

	var img image.Image
	switch f {
	case fmtPNG:
		img, err = png.Decode(bytes.NewReader(data))
	case fmtJPEG:
		img, err = jpeg.Decode(bytes.NewReader(data))
	case fmtGIF:
		img, err = gif.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return nil, ErrFormat
	}
	// The decoder may not return the size that the header gave: check again, so
	// the loops below stay inside the limits.
	if b := img.Bounds(); b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > MaxSide || b.Dy() > MaxSide || b.Dx()*b.Dy() > MaxPixels {
		return nil, ErrDimensions
	}

	var out bytes.Buffer
	if err := png.Encode(&out, squareTo128(img)); err != nil {
		return nil, fmt.Errorf("avatar: encode the result: %w", err)
	}
	return out.Bytes(), nil
}

// span is the share of one source pixel in up to two result pixels.
type span struct {
	idx [2]int
	w   [2]uint64
}

// squareTo128 crops the center square of src and resamples it to Size x Size.
// The side of the square is s. A result pixel i covers the source interval
// [i*s, (i+1)*s) and a source pixel k covers [k*Size, (k+1)*Size), both in units
// of 1/Size source pixel, so every weight is an exact integer.
func squareTo128(src image.Image) *image.RGBA {
	b := src.Bounds()
	s := min(b.Dx(), b.Dy())
	x0 := b.Min.X + (b.Dx()-s)/2
	y0 := b.Min.Y + (b.Dy()-s)/2
	dst := image.NewRGBA(image.Rect(0, 0, Size, Size))

	if s <= Size {
		// Growth, or the same size: the nearest pixel to the center of each
		// result pixel.
		for y := range Size {
			sy := y0 + (2*y+1)*s/(2*Size)
			for x := range Size {
				sx := x0 + (2*x+1)*s/(2*Size)
				r, g, bl, a := src.At(sx, sy).RGBA()
				o := dst.PixOffset(x, y)
				dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r>>8), uint8(g>>8), uint8(bl>>8), uint8(a>>8)
			}
		}
		return dst
	}

	// Shrink: the area average of premultiplied color.
	spans := make([]span, s)
	for k := range s {
		lo, hi := k*Size, (k+1)*Size
		i0, i1 := lo/s, (hi-1)/s
		sp := &spans[k]
		for j, i := 0, i0; i <= i1 && j < 2; i, j = i+1, j+1 {
			sp.idx[j] = i
			sp.w[j] = uint64(min(hi, (i+1)*s) - max(lo, i*s))
		}
	}
	sum := make([][4]uint64, Size*Size)
	for y := range s {
		sy := spans[y]
		for x := range s {
			sx := spans[x]
			r, g, bl, a := src.At(x0+x, y0+y).RGBA()
			c := [4]uint64{uint64(r), uint64(g), uint64(bl), uint64(a)}
			for j := range 2 {
				if sy.w[j] == 0 {
					continue
				}
				for i := range 2 {
					if sx.w[i] == 0 {
						continue
					}
					w := sy.w[j] * sx.w[i]
					p := &sum[sy.idx[j]*Size+sx.idx[i]]
					p[0] += c[0] * w
					p[1] += c[1] * w
					p[2] += c[2] * w
					p[3] += c[3] * w
				}
			}
		}
	}
	total := uint64(s) * uint64(s)
	for y := range Size {
		for x := range Size {
			p := sum[y*Size+x]
			o := dst.PixOffset(x, y)
			for c := range 4 {
				dst.Pix[o+c] = uint8((p[c] / total) >> 8)
			}
		}
	}
	return dst
}
