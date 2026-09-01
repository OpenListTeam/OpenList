//go:build darwin

package local

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

func pdfThumbnailSupported() bool {
	return true
}

func renderPDFThumbnail(ctx context.Context, fullPath string) (*bytes.Buffer, error) {
	tempDir, err := os.MkdirTemp("", "openlist-pdf-thumb-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(tempDir)

	renderCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(renderCtx, "/usr/bin/qlmanage", "-t", "-s", "512", "-o", tempDir, fullPath)
	if output, err := cmd.CombinedOutput(); err != nil {
		if renderCtx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("render PDF thumbnail timed out: %w", renderCtx.Err())
		}
		if renderCtx.Err() != nil {
			return nil, fmt.Errorf("render PDF thumbnail canceled: %w", renderCtx.Err())
		}
		return nil, fmt.Errorf("render PDF thumbnail: %w: %s", err, bytes.TrimSpace(output))
	}

	thumbPath := filepath.Join(tempDir, filepath.Base(fullPath)+".png")
	data, err := os.ReadFile(thumbPath)
	if err != nil {
		return nil, fmt.Errorf("read rendered PDF thumbnail: %w", err)
	}
	return bytes.NewBuffer(data), nil
}
