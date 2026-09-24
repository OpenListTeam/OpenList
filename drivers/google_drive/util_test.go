package google_drive

import (
	"net/url"
	"strings"
	"testing"
)

func TestResolveDownloadStrategy_Media(t *testing.T) {
	cases := []string{
		"application/pdf",
		"image/jpeg",
		"image/png",
		"application/zip",
		"video/mp4",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		"application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
		"application/vnd.openxmlformats-officedocument.presentationml.presentation",
		"application/octet-stream",
	}
	for _, mime := range cases {
		t.Run(mime, func(t *testing.T) {
			s, err := resolveDownloadStrategy(mime)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", mime, err)
			}
			if s.Kind != kindMedia {
				t.Errorf("expected kindMedia for %q, got %v", mime, s.Kind)
			}
		})
	}
}

func TestResolveDownloadStrategy_Export(t *testing.T) {
	cases := []struct {
		src      string
		wantMIME string
		wantExt  string
	}{
		{mimeTypeGoogleDoc, mimeTypeDocx, ".docx"},
		{mimeTypeGoogleSheet, mimeTypeXlsx, ".xlsx"},
		{mimeTypeGoogleSlides, mimeTypePptx, ".pptx"},
		{mimeTypeGoogleDrawing, mimeTypePDF, ".pdf"},
		{mimeTypeGoogleScript, mimeTypeScriptJSON, ".json"},
	}
	for _, c := range cases {
		t.Run(c.src, func(t *testing.T) {
			s, err := resolveDownloadStrategy(c.src)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if s.Kind != kindExport {
				t.Errorf("expected kindExport, got %v", s.Kind)
			}
			if s.ExportMIME != c.wantMIME {
				t.Errorf("ExportMIME: got %q, want %q", s.ExportMIME, c.wantMIME)
			}
			if s.Ext != c.wantExt {
				t.Errorf("Ext: got %q, want %q", s.Ext, c.wantExt)
			}
		})
	}
}

func TestResolveDownloadStrategy_Unsupported(t *testing.T) {
	cases := []string{
		mimeTypeGoogleFolder,
		mimeTypeGoogleShortcut,
		mimeTypeGoogleForm,
		mimeTypeGoogleSite,
		mimeTypeGoogleMap,
		mimeTypeGoogleVid,
		"application/vnd.google-apps.unknown-future-type",
	}
	for _, mime := range cases {
		t.Run(mime, func(t *testing.T) {
			_, err := resolveDownloadStrategy(mime)
			if err == nil {
				t.Errorf("expected error for unsupported type %q, got nil", mime)
			}
		})
	}
}

// TestExportURLEncoding verifies that MIME types with special characters are
// properly percent-encoded in the export URL query string.
func TestExportURLEncoding(t *testing.T) {
	cases := []struct {
		mime string
		want string
	}{
		{
			mimeTypeDocx,
			// / and + must be encoded
			url.QueryEscape(mimeTypeDocx),
		},
		{
			mimeTypeScriptJSON,
			url.QueryEscape(mimeTypeScriptJSON),
		},
	}
	for _, c := range cases {
		t.Run(c.mime, func(t *testing.T) {
			q := url.Values{}
			q.Set("mimeType", c.mime)
			encoded := q.Encode()
			if !strings.Contains(encoded, c.want) {
				t.Errorf("encoded %q does not contain %q", encoded, c.want)
			}
		})
	}
}
