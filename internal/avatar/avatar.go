// Package avatar makes the stored avatar from an upload.
//
// An upload is untrusted. [Make] reads at most 1 MiB, finds the format from the
// first bytes (never from a name or a content type), checks the size from the
// header before it allocates a pixel, takes memory from the shared gate for the
// decode, and then draws a new 128 x 128 image and encodes it with the standard
// PNG encoder. No byte of the upload reaches the output except as a pixel, so
// metadata, comments and trailing data do not survive.
//
// [CheckHeader] checks the header image of the board with the same limits and the
// same gate. It keeps the file as it was sent, and it takes WebP too.
package avatar

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"sync"
	"time"

	"golang.org/x/image/webp"

	"github.com/evanwtf/gobb/internal/login"
)

// encoder is the one PNG encoder of the package. Its buffer pool keeps the
// encoder's tables (about 1.1 MB) between calls. The default settings are the same
// as those of [png.Encode], so the output does not change.
var encoder = &png.Encoder{BufferPool: new(bufferPool)}

// bufferPool is a [png.EncoderBufferPool] on a [sync.Pool].
type bufferPool struct{ pool sync.Pool }

func (p *bufferPool) Get() *png.EncoderBuffer {
	b, _ := p.pool.Get().(*png.EncoderBuffer)
	return b // nil makes the encoder allocate a new buffer
}

func (p *bufferPool) Put(b *png.EncoderBuffer) { p.pool.Put(b) }

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
	// ErrTooMuchMemory means the decode needs more memory than the whole budget of
	// the gate, although the side and the pixels are within the limits. A JPEG of
	// 3000 x 2500 can be one.
	ErrTooMuchMemory = errors.New("avatar: the image needs too much memory to decode")
)

type format int

const (
	fmtNone format = iota
	fmtPNG
	fmtJPEG
	fmtGIF
	// fmtWebP is a format of the header image only: [Make] refuses it.
	fmtWebP
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
//  4. It works out the memory of the decode (see [chargeKiB]; the figures depend
//     on the format and on the color model or bit depth) and returns
//     [ErrTooMuchMemory] when that is more than the whole budget of gate. Else it
//     takes the memory from gate, waiting up to wait, and returns [login.ErrBusy]
//     when the wait ends (or the error of ctx when ctx ends first). It gives the
//     memory back before it returns.
//  5. It decodes the image (the first frame of a GIF), crops the center square,
//     and resamples it to [Size] x [Size]: an area average when it shrinks, the
//     nearest pixel when it grows.
//  6. It gives the gate share back (after a garbage collection) and encodes the
//     result with the standard PNG encoder.
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
	small, err := decodeSquare(ctx, data, gate, wait)
	if err != nil {
		return nil, err
	}
	// The encode runs after the gate share is back: the decoded image is gone,
	// and what is left is the small result and the encoder's tables (about
	// 1 MiB). The encoder takes the tables from a pool, so a call does not
	// allocate them again. They are not part of the charge.
	var out bytes.Buffer
	if err := encoder.Encode(&out, small); err != nil {
		return nil, fmt.Errorf("avatar: encode the result: %w", err)
	}
	return out.Bytes(), nil
}

// decodeSquare is the part of [Make] that the gate bounds: the checks of the
// header, the share of the gate, the decode and the resample to [Size] x [Size].
// The bytes of data are held through the call; they are the 1024 KiB of the
// charge that does not depend on the pixels, with the resampler's tables. The
// release collects garbage first, so the decoded image does not sit on the heap
// under the next holder (a sign-in uses the same gate).
func decodeSquare(ctx context.Context, data []byte, gate *login.Gate, wait time.Duration) (*image.RGBA, error) {
	f, kib, err := inspect(data)
	if err != nil {
		return nil, err
	}
	if kib > gate.Budget() {
		// The gate would run this decode alone, and it needs more than the budget:
		// the process could run out of memory. Refuse it before any allocation.
		return nil, ErrTooMuchMemory
	}
	release, err := gate.AcquireCollected(ctx, kib, wait)
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
	return squareTo128(img), nil
}

// inspect reads the header of data and returns its format and the memory that a
// decode needs, in KiB (see [chargeKiB]). It allocates no pixels. It returns
// [ErrFormat] when the first bytes are none of the three formats or the header
// does not parse, and [ErrDimensions] when a side is over [MaxSide] or the pixels
// are over [MaxPixels].
func inspect(data []byte) (format, uint64, error) {
	return inspectAs(data, sniff(data))
}

// inspectAs is [inspect] for the format f, which the caller found from the first
// bytes. It reads the header of data as an f.
func inspectAs(data []byte, f format) (format, uint64, error) {
	var (
		cfg image.Config
		err error
	)
	switch f {
	case fmtPNG:
		cfg, err = png.DecodeConfig(bytes.NewReader(data))
	case fmtJPEG:
		cfg, err = jpeg.DecodeConfig(bytes.NewReader(data))
	case fmtGIF:
		cfg, err = gif.DecodeConfig(bytes.NewReader(data))
	case fmtWebP:
		cfg, err = webp.DecodeConfig(bytes.NewReader(data))
	default:
		return fmtNone, 0, ErrFormat
	}
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 {
		return fmtNone, 0, ErrFormat
	}
	if cfg.Width > MaxSide || cfg.Height > MaxSide || cfg.Width*cfg.Height > MaxPixels {
		return fmtNone, 0, ErrDimensions
	}
	// The PNG header is sound, so the interlace byte is the last byte of the
	// first chunk: 8 (signature) + 8 (chunk length and type) + 13 - 1.
	interlaced := f == fmtPNG && len(data) > 28 && data[28] == 1
	return f, chargeKiB(f, cfg, interlaced), nil
}

// chargeKiB is the memory that the decode of an image needs, in KiB, worked out
// from the header: 1024 KiB, plus a number of bytes for each pixel that depends on
// what the decoder holds. The tests in charge_internal_test.go compare each figure
// with the bytes that the decoder allocates.
//
//   - JPEG: by color model, on sides rounded up to a multiple of 32. The decoder
//     pads each plane to whole MCUs of 8 x the largest sampling factor, and a
//     factor may be 4 (a frame with a scan for each component can give all three
//     components 4 x 2 or 2 x 4), so the MCU is up to 32 x 32 pixels. Gray 5 bytes
//     a pixel, any file of three components 19, and 24 for CMYK and any other
//     model. Three components charge as RGB whatever the header says: the
//     decoder reads an Adobe segment anywhere before the end of the image, and it
//     decides between YCbCr (15) and RGB (19) at the end, but
//     [jpeg.DecodeConfig] stops at the first scan and cannot see a segment that
//     comes after it. A progressive file keeps 32-bit coefficients for each
//     component.
//   - PNG: 8 bytes a pixel for a 16-bit file, else 4; twice that for an
//     interlaced file (Adam7), where the decoder allocates an image for each
//     pass besides the final image. The passes are garbage once merged, but the
//     collector may not have run, so the charge counts them all.
//   - GIF: 4.
//   - WebP (header image only): 2 for a lossy file with no alpha, 8 for any other.
func chargeKiB(f format, cfg image.Config, interlaced bool) uint64 {
	w, h := uint64(cfg.Width), uint64(cfg.Height)
	perPixel := uint64(4)
	switch f {
	case fmtJPEG:
		w, h = (w+31)&^31, (h+31)&^31
		switch cfg.ColorModel {
		case color.GrayModel:
			perPixel = 5
		case color.YCbCrModel, color.RGBAModel:
			perPixel = 19
		default: // CMYK and anything else
			perPixel = 24
		}
	case fmtPNG:
		switch cfg.ColorModel {
		case color.Gray16Model, color.RGBA64Model, color.NRGBA64Model:
			perPixel = 8
		}
	case fmtWebP:
		// A lossy file with no alpha holds one YCbCr image (1.5 bytes a pixel). With
		// alpha it also holds the alpha plane, and a compressed alpha plane is
		// decoded as a whole NRGBA image first (1 + 4 more). A lossless file is one
		// NRGBA image (4) and its transforms. The charge is 8 for the last two, with
		// room to spare, and 2 for the first.
		perPixel = 8
		if cfg.ColorModel == color.YCbCrModel {
			perPixel = 2
		}
	}
	total := w * h * perPixel
	if interlaced {
		total *= 2
	}
	return total/1024 + 1024
}

// pixelReader returns a function that gives the premultiplied 16-bit color of a
// pixel of src. For the types that the decoders return, it reads the pixel as a
// struct, which allocates nothing; image.Image.At boxes each color in an
// interface, and over 16,000,000 pixels that is 64 to 128 MB of garbage. Any
// other type goes through At.
func pixelReader(src image.Image) func(x, y int) (r, g, b, a uint32) {
	switch s := src.(type) {
	case *image.RGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.RGBAAt(x, y).RGBA() }
	case *image.NRGBA:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.NRGBAAt(x, y).RGBA() }
	case *image.RGBA64:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.RGBA64At(x, y).RGBA() }
	case *image.NRGBA64:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.NRGBA64At(x, y).RGBA() }
	case *image.Gray:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.GrayAt(x, y).RGBA() }
	case *image.Gray16:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.Gray16At(x, y).RGBA() }
	case *image.YCbCr:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.YCbCrAt(x, y).RGBA() }
	case *image.CMYK:
		return func(x, y int) (uint32, uint32, uint32, uint32) { return s.CMYKAt(x, y).RGBA() }
	case *image.Paletted:
		return func(x, y int) (uint32, uint32, uint32, uint32) {
			if !(image.Point{X: x, Y: y}).In(s.Rect) {
				return 0, 0, 0, 0
			}
			return s.Palette[s.ColorIndexAt(x, y)].RGBA()
		}
	}
	return func(x, y int) (uint32, uint32, uint32, uint32) { return src.At(x, y).RGBA() }
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
	at := pixelReader(src)

	if s <= Size {
		// Growth, or the same size: the nearest pixel to the center of each
		// result pixel.
		for y := range Size {
			sy := y0 + (2*y+1)*s/(2*Size)
			for x := range Size {
				sx := x0 + (2*x+1)*s/(2*Size)
				r, g, bl, a := at(sx, sy)
				o := dst.PixOffset(x, y)
				dst.Pix[o], dst.Pix[o+1], dst.Pix[o+2], dst.Pix[o+3] = uint8(r>>8), uint8(g>>8), uint8(bl>>8), uint8(a>>8)
			}
		}
		return dst
	}

	// Shrink: the area average of premultiplied color. A result row i takes the
	// source rows that overlap it. Each source row is reduced across to Size
	// sums, then added to the sums of the result row with its weight, so the
	// memory is two rows of Size sums and not the whole result.
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
	across := make([][4]uint64, Size)
	down := make([][4]uint64, Size)
	total := uint64(s) * uint64(s)
	for i := range Size {
		clear(down)
		// Source rows k overlap result row i when k*Size < (i+1)*s and (k+1)*Size > i*s.
		for k := i * s / Size; k <= ((i+1)*s-1)/Size && k < s; k++ {
			wy := uint64(min((k+1)*Size, (i+1)*s) - max(k*Size, i*s))
			if wy == 0 {
				continue
			}
			clear(across)
			for x := range s {
				r, g, bl, a := at(x0+x, y0+k)
				sx := &spans[x]
				for j := range 2 {
					if w := sx.w[j]; w != 0 {
						p := &across[sx.idx[j]]
						p[0] += uint64(r) * w
						p[1] += uint64(g) * w
						p[2] += uint64(bl) * w
						p[3] += uint64(a) * w
					}
				}
			}
			for x := range Size {
				for c := range 4 {
					down[x][c] += across[x][c] * wy
				}
			}
		}
		for x := range Size {
			o := dst.PixOffset(x, i)
			for c := range 4 {
				dst.Pix[o+c] = uint8((down[x][c] / total) >> 8)
			}
		}
	}
	return dst
}
