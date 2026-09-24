package google_drive

import (
	"net/url"
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
	}{
		{mimeTypeGoogleDoc, mimeTypeDocx},
		{mimeTypeGoogleSheet, mimeTypeXlsx},
		{mimeTypeGoogleSlides, mimeTypePptx},
		{mimeTypeGoogleDrawing, mimeTypePDF},
		{mimeTypeGoogleScript, mimeTypeScriptJSON},
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

// TestResolveDownloadStrategy_MediaFallback verifies that unrecognised
// application/vnd.google-apps.* MIME types fall through to the media download
// path rather than being rejected. This preserves compatibility with Drive MIME
// types that are not Workspace-native documents.
func TestResolveDownloadStrategy_MediaFallback(t *testing.T) {
	cases := []string{
		// Documented Drive-specific types that are not Workspace editors.
		"application/vnd.google-apps.video",
		"application/vnd.google-apps.audio",
		"application/vnd.google-apps.photo",
		// Unknown type: should not be blocked by a blanket prefix rejection.
		"application/vnd.google-apps.unknown-future-type",
	}
	for _, mime := range cases {
		t.Run(mime, func(t *testing.T) {
			s, err := resolveDownloadStrategy(mime)
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", mime, err)
			}
			if s.Kind != kindMedia {
				t.Errorf("expected kindMedia fallback for %q, got %v", mime, s.Kind)
			}
		})
	}
}

func TestBuildDownloadURL(t *testing.T) {
	const fileID = "abc123fileID"

	t.Run("binary/media", func(t *testing.T) {
		rawURL := buildDownloadURL(fileID, downloadStrategy{Kind: kindMedia})
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if u.Scheme != "https" {
			t.Errorf("scheme: got %q, want https", u.Scheme)
		}
		if u.Host != "www.googleapis.com" {
			t.Errorf("host: got %q, want www.googleapis.com", u.Host)
		}
		wantPath := "/drive/v3/files/" + fileID
		if u.Path != wantPath {
			t.Errorf("path: got %q, want %q", u.Path, wantPath)
		}
		q := u.Query()
		if q.Get("alt") != "media" {
			t.Errorf("alt: got %q, want media", q.Get("alt"))
		}
		if q.Get("acknowledgeAbuse") != "true" {
			t.Errorf("acknowledgeAbuse: got %q, want true", q.Get("acknowledgeAbuse"))
		}
		if q.Get("supportsAllDrives") != "true" {
			t.Errorf("supportsAllDrives: got %q, want true", q.Get("supportsAllDrives"))
		}
		// includeItemsFromAllDrives is a files.list parameter, not files.get.
		if q.Has("includeItemsFromAllDrives") {
			t.Errorf("media URL must not contain includeItemsFromAllDrives")
		}
	})

	t.Run("Google Doc export", func(t *testing.T) {
		rawURL := buildDownloadURL(fileID, downloadStrategy{Kind: kindExport, ExportMIME: mimeTypeDocx})
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		if u.Scheme != "https" {
			t.Errorf("scheme: got %q, want https", u.Scheme)
		}
		wantPath := "/drive/v3/files/" + fileID + "/export"
		if u.Path != wantPath {
			t.Errorf("path: got %q, want %q", u.Path, wantPath)
		}
		q := u.Query()
		if q.Get("mimeType") != mimeTypeDocx {
			t.Errorf("mimeType: got %q, want %q", q.Get("mimeType"), mimeTypeDocx)
		}
		// Export URL must not carry media-download or shared-drive parameters.
		for _, forbidden := range []string{"includeItemsFromAllDrives", "supportsAllDrives", "acknowledgeAbuse", "alt"} {
			if q.Has(forbidden) {
				t.Errorf("export URL must not contain parameter %q", forbidden)
			}
		}
	})

	t.Run("Apps Script export MIME encoding", func(t *testing.T) {
		// mimeTypeScriptJSON contains '+' which must survive round-trip encoding.
		rawURL := buildDownloadURL(fileID, downloadStrategy{Kind: kindExport, ExportMIME: mimeTypeScriptJSON})
		u, err := url.Parse(rawURL)
		if err != nil {
			t.Fatalf("url.Parse: %v", err)
		}
		q := u.Query()
		if q.Get("mimeType") != mimeTypeScriptJSON {
			t.Errorf("mimeType: got %q, want %q", q.Get("mimeType"), mimeTypeScriptJSON)
		}
	})
}
