package qbittorrent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAddFromTorrentUploadsTorrentFile(t *testing.T) {
	torrentData := []byte("torrent-content")
	addCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			w.WriteHeader(http.StatusOK)
		case "/api/v2/torrents/add":
			addCalled = true
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Fatalf("ParseMultipartForm() error = %v", err)
			}
			if got := r.MultipartForm.Value["savepath"]; len(got) != 1 || got[0] != "/downloads" {
				t.Fatalf("savepath = %v", got)
			}
			if got := r.MultipartForm.Value["tags"]; len(got) != 1 || got[0] != "openlist-task-id" {
				t.Fatalf("tags = %v", got)
			}
			if got := r.MultipartForm.Value["autoTMM"]; len(got) != 1 || got[0] != "false" {
				t.Fatalf("autoTMM = %v", got)
			}
			if got := r.MultipartForm.Value["urls"]; len(got) != 0 {
				t.Fatalf("urls field should be absent, got %v", got)
			}

			files := r.MultipartForm.File["torrents"]
			if len(files) != 1 {
				t.Fatalf("torrents files = %d", len(files))
			}
			if files[0].Filename != "task-id.torrent" {
				t.Fatalf("torrent filename = %q", files[0].Filename)
			}
			file, err := files[0].Open()
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer file.Close()
			got, err := io.ReadAll(file)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(got) != string(torrentData) {
				t.Fatalf("torrent data = %q", got)
			}

			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.AddFromTorrent(torrentData, "/downloads", "task-id"); err != nil {
		t.Fatalf("AddFromTorrent() error = %v", err)
	}
	if !addCalled {
		t.Fatal("qBittorrent add endpoint was not called")
	}
}

func TestAddFromLinkUsesUrlsFieldAndAcceptsHTTP200(t *testing.T) {
	addCalled := false

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			w.WriteHeader(http.StatusOK)
		case "/api/v2/torrents/add":
			addCalled = true
			if err := r.ParseMultipartForm(1024 * 1024); err != nil {
				t.Fatalf("ParseMultipartForm() error = %v", err)
			}
			if got := r.MultipartForm.Value["urls"]; len(got) != 1 || got[0] != "magnet:?xt=urn:btih:test" {
				t.Fatalf("urls = %v", got)
			}
			if got := r.MultipartForm.File["torrents"]; len(got) != 0 {
				t.Fatalf("torrents field should be absent, got %v", got)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("Ok."))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := client.AddFromLink("magnet:?xt=urn:btih:test", "/downloads", "task-id"); err != nil {
		t.Fatalf("AddFromLink() error = %v", err)
	}
	if !addCalled {
		t.Fatal("qBittorrent add endpoint was not called")
	}
}

func TestAddFromLinkReportsNon2xxResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/app/version":
			w.WriteHeader(http.StatusOK)
		case "/api/v2/torrents/add":
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte("bad torrent"))
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := New(server.URL)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = client.AddFromLink("magnet:?xt=urn:btih:test", "/downloads", "task-id")
	if err == nil {
		t.Fatal("AddFromLink() error = nil")
	}
	if !strings.Contains(err.Error(), "400 Bad Request") || !strings.Contains(err.Error(), "bad torrent") {
		t.Fatalf("AddFromLink() error = %v", err)
	}
}
