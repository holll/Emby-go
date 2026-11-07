package imageutil

import (
	"image/jpeg"
	"io"
)

// EncodeJPEGTo encodes into a caller-owned writer with the shared decode limits
// and the standard library's default JPEG quality. It does not close the writer.
func EncodeJPEGTo(src io.ReadSeeker, dst io.Writer) error {
	img, err := decodeLimited(src)
	if err != nil {
		return err
	}
	return jpeg.Encode(dst, img, nil)
}
