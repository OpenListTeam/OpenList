//go:build darwin

package local

import (
	"bytes"
	"context"
	"errors"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRenderPDFThumbnailDarwin(t *testing.T) {
	tempDir := t.TempDir()
	textPath := filepath.Join(tempDir, "source.txt")
	pdfPath := filepath.Join(tempDir, "source 文件.pdf")
	if err := os.WriteFile(textPath, []byte("OpenList PDF thumbnail integration test\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command("/usr/sbin/cupsfilter", textPath)
	pdfData, err := cmd.Output()
	if err != nil {
		t.Fatalf("create fixture PDF: %v", err)
	}
	if err := os.WriteFile(pdfPath, pdfData, 0o600); err != nil {
		t.Fatal(err)
	}

	thumb, err := renderPDFThumbnail(context.Background(), pdfPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(thumb.Bytes(), []byte("\x89PNG\r\n\x1a\n")) {
		t.Fatal("rendered thumbnail is not PNG")
	}
	cfg, err := png.DecodeConfig(bytes.NewReader(thumb.Bytes()))
	if err != nil {
		t.Fatalf("decode thumbnail: %v", err)
	}
	if cfg.Width <= 0 || cfg.Height <= 0 {
		t.Fatalf("invalid thumbnail dimensions: %dx%d", cfg.Width, cfg.Height)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := renderPDFThumbnail(canceledCtx, pdfPath); !errors.Is(err, context.Canceled) {
		t.Fatalf("renderPDFThumbnail with canceled context returned %v, want context.Canceled", err)
	}
}
