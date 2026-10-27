package avatar

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"io"
	"time"

	"github.com/evanwtf/gobb/internal/login"
)

// MaxIconSide is the largest width or height of a smiley or rank image, in
// pixels.
const MaxIconSide = 512

// Icon returns the PNG bytes of a smiley or rank image for the image that r
// holds. It has the checks of [Make], with these differences:
//
//   - The sides may be at most [MaxIconSide] pixels, and the check runs on the
//     header, before the gate.
//   - The result keeps the size and the shape of the image. It is not cropped
//     and not resampled.
//   - An animated GIF keeps its first frame. A later frame costs memory that the
//     header does not state.
//
// The encoder draws the result from the pixels alone, so no byte of the upload
// reaches the output except as a pixel: metadata, comments and trailing data do
// not survive. The errors are those of [Make].
func Icon(ctx context.Context, r io.Reader, gate *login.Gate, wait time.Duration) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("avatar: read the upload: %w", err)
	}
	if len(data) > MaxBytes {
		return nil, ErrTooLarge
	}
	var out bytes.Buffer
	// The encode runs under the share of the gate: the image is small (at most
	// 512 x 512 pixels), and the encoder's buffers are pooled.
	err = underGate(ctx, data, gate, wait, MaxIconSide, func(img image.Image) error {
		if err := encoder.Encode(&out, img); err != nil {
			return fmt.Errorf("avatar: encode the result: %w", err)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
