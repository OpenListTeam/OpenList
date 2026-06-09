package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/gowebdav"
)

func TestGetMapsPropfindNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("expected PROPFIND request, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &WebDav{
		client: gowebdav.NewClient(server.URL, "", ""),
	}

	_, err := d.Get(context.Background(), "/missing")
	if !errs.IsObjectNotFound(err) {
		t.Fatalf("expected object not found, got %v", err)
	}
}

func TestOpGetRecognizesPropfindNotFound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "PROPFIND" {
			t.Fatalf("expected PROPFIND request, got %s", r.Method)
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	d := &WebDav{
		client: gowebdav.NewClient(server.URL, "", ""),
	}

	_, err := op.Get(context.Background(), d, "/missing")
	if !errs.IsObjectNotFound(err) {
		t.Fatalf("expected object not found, got %v", err)
	}
}
