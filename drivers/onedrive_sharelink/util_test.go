package onedrive_sharelink

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	internalNet "github.com/OpenListTeam/OpenList/v4/internal/net"
)

func TestNoRedirectClientUsesSharedSettings(t *testing.T) {
	client := NewNoRedirectCLient()
	sharedClient := internalNet.NewHttpClient()
	if client.Timeout != sharedClient.Timeout {
		t.Fatalf("expected shared timeout %s, got %s", sharedClient.Timeout, client.Timeout)
	}
	if reflect.TypeOf(client.Transport) != reflect.TypeOf(sharedClient.Transport) {
		t.Fatalf("expected shared transport type %T, got %T", sharedClient.Transport, client.Transport)
	}
}

func TestNoRedirectClientStopsRedirects(t *testing.T) {
	targetRequested := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/target" {
			targetRequested = true
			w.WriteHeader(http.StatusNoContent)
			return
		}
		http.Redirect(w, r, "/target", http.StatusFound)
	}))
	defer server.Close()

	resp, err := NewNoRedirectCLient().Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusFound {
		t.Fatalf("expected redirect response, got %d", resp.StatusCode)
	}
	if targetRequested {
		t.Fatal("redirect target was requested")
	}
}
