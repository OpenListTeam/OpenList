package quark

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

func TestUploadMimetype(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		mimetype string
		want     string
	}{
		// Caller supplied a MIME type: it must win.
		{"explicit content type", "clip.mp4", "video/mp4", "video/mp4"},
		{"explicit octet-stream", "report.pdf", "application/octet-stream", "application/octet-stream"},
		// Empty MIME type: fall back to guessing from the file name.
		{"fallback by extension", "report.pdf", "", "application/pdf"},
		{"fallback unknown extension", "archive.bin", "", "application/octet-stream"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &stream.FileStream{
				Obj:      &model.Object{Name: tt.fileName},
				Mimetype: tt.mimetype,
			}
			if got := uploadMimetype(s); got != tt.want {
				t.Fatalf("uploadMimetype() = %q, want %q", got, tt.want)
			}
		})
	}
}
