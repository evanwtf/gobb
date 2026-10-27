package avatar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"time"

	"golang.org/x/image/webp"

	"github.com/evanwtf/gobb/internal/login"
)

// Header is a header image that [CheckHeader] accepted.
type Header struct {
	// Data is the file to store: the pixels of the upload, encoded again, so that
	// no metadata of the upload survives.
	Data []byte
	// Ext is the file name extension that fits Data: "png" or "jpg".
	Ext string
	// Width and Height are the size in pixels.
	Width, Height int
}

// CheckHeader reads the header image that r holds and checks it as [Make] checks
// an avatar, with one more format and one difference: the image keeps its size.
//
//  1. It reads at most [MaxBytes] + 1 bytes, and returns [ErrTooLarge] for more.
//
//  2. It finds the format from the first bytes, and returns [ErrFormat] for
//     anything but PNG, JPEG and WebP (GIF and SVG included).
//
//  3. It reads the size from the header only, and returns [ErrDimensions] when a
//     side is over [MaxSide] or the pixels are over [MaxPixels].
//
//  4. It takes the memory of the decode from gate as [Make] does ([ErrTooMuchMemory],
//     [login.ErrBusy], or the error of ctx), decodes the whole image to see that
//     it is sound, and gives the memory back.
//
//  5. It encodes the pixels again: a JPEG as a JPEG of quality [headerQuality], a
//     PNG or a WebP as a PNG (the standard library has no WebP encoder). Only
//     pixels reach Data, so metadata such as the place where a photo was taken
//     does not. A result over [maxHeaderOut] bytes gives [ErrTooLarge].
//
// A file whose header is sound but whose body does not decode gives [ErrFormat].
func CheckHeader(ctx context.Context, r io.Reader, gate *login.Gate, wait time.Duration) (Header, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return Header{}, fmt.Errorf("avatar: read the upload: %w", err)
	}
	if len(data) > MaxBytes {
		return Header{}, ErrTooLarge
	}
	f := sniffHeader(data)
	if f == fmtGIF {
		return Header{}, ErrFormat
	}
	_, kib, err := inspectAs(data, f, MaxSide)
	if err != nil {
		return Header{}, err
	}
	if kib > gate.Budget() {
		return Header{}, ErrTooMuchMemory
	}
	release, err := gate.AcquireCollected(ctx, kib, wait)
	if err != nil {
		return Header{}, err
	}
	defer release()
	var img image.Image
	switch f {
	case fmtPNG:
		img, err = png.Decode(bytes.NewReader(data))
	case fmtJPEG:
		img, err = jpeg.Decode(bytes.NewReader(data))
	case fmtWebP:
		img, err = webp.Decode(bytes.NewReader(data))
	}
	if err != nil {
		return Header{}, ErrFormat
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 || b.Dx() > MaxSide || b.Dy() > MaxSide || b.Dx()*b.Dy() > MaxPixels {
		return Header{}, ErrDimensions
	}
	var out bytes.Buffer
	ext := "png"
	if f == fmtJPEG {
		ext = "jpg"
		err = jpeg.Encode(&out, img, &jpeg.Options{Quality: headerQuality})
	} else {
		err = png.Encode(&out, img)
	}
	if err != nil {
		return Header{}, fmt.Errorf("avatar: encode the header image: %w", err)
	}
	if out.Len() > maxHeaderOut {
		return Header{}, ErrTooLarge
	}
	return Header{Data: out.Bytes(), Ext: ext, Width: b.Dx(), Height: b.Dy()}, nil
}

// headerQuality is the JPEG quality of a header image that came as a JPEG.
const headerQuality = 90

// maxHeaderOut is the largest header image that [CheckHeader] gives back, in
// bytes. A WebP becomes a PNG, which can be larger than the upload.
const maxHeaderOut = 8 * MaxBytes

// sniffHeader is [sniff] with WebP: the bytes "RIFF", four bytes of length, and
// "WEBP".
func sniffHeader(b []byte) format {
	if len(b) >= 12 && bytes.HasPrefix(b, []byte("RIFF")) && bytes.Equal(b[8:12], []byte("WEBP")) {
		return fmtWebP
	}
	return sniff(b)
}
