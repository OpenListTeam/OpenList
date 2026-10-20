//go:build !darwin

package local

import (
	"bytes"
	"context"
	"errors"
)

func pdfThumbnailSupported() bool {
	return false
}

func renderPDFThumbnail(context.Context, string) (*bytes.Buffer, error) {
	return nil, errors.New("PDF thumbnails are not supported on this platform")
}
