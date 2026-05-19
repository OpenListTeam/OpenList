package tool

import (
	"context"
	"errors"
	"net"
	"net/http"
	"testing"
)

func TestValidateOfflineDownloadURLRejectsCloudMetadataIP(t *testing.T) {
	err := ValidateOfflineDownloadURL(context.Background(), "http://169.254.169.254/")
	if !errors.Is(err, ErrCloudMetadataEndpoint) {
		t.Fatalf("expected cloud metadata error, got %v", err)
	}
}

func TestValidateOfflineDownloadURLRejectsCloudMetadataIPWithoutScheme(t *testing.T) {
	err := ValidateOfflineDownloadURL(context.Background(), "169.254.169.254")
	if !errors.Is(err, ErrCloudMetadataEndpoint) {
		t.Fatalf("expected cloud metadata error, got %v", err)
	}
}

func TestValidateOfflineDownloadURLRejectsCloudMetadataIPWithPort(t *testing.T) {
	err := ValidateOfflineDownloadURL(context.Background(), "http://169.254.169.254:80/")
	if !errors.Is(err, ErrCloudMetadataEndpoint) {
		t.Fatalf("expected cloud metadata error, got %v", err)
	}
}

func TestValidateOfflineDownloadURLAllowsPublicURL(t *testing.T) {
	err := ValidateOfflineDownloadURL(context.Background(), "http://8.8.8.8/")
	if err != nil {
		t.Fatalf("expected public URL to be allowed, got %v", err)
	}
}

func TestValidateOfflineDownloadURLAllowsPrivateURL(t *testing.T) {
	err := ValidateOfflineDownloadURL(context.Background(), "http://192.168.1.10:8080/")
	if err != nil {
		t.Fatalf("expected private URL to be allowed, got %v", err)
	}
}

func TestValidateOfflineDownloadURLRejectsDomainResolvingToCloudMetadataIP(t *testing.T) {
	previousLookup := lookupIPAddr
	lookupIPAddr = func(ctx context.Context, host string) ([]net.IPAddr, error) {
		if host != "metadata.example.test" {
			t.Fatalf("unexpected host lookup: %s", host)
		}
		return []net.IPAddr{{IP: net.ParseIP("169.254.169.254")}}, nil
	}
	defer func() {
		lookupIPAddr = previousLookup
	}()

	err := ValidateOfflineDownloadURL(context.Background(), "http://metadata.example.test/")
	if !errors.Is(err, ErrCloudMetadataEndpoint) {
		t.Fatalf("expected cloud metadata error, got %v", err)
	}
}

func TestOfflineDownloadHTTPClientRejectsRedirectToCloudMetadataIP(t *testing.T) {
	client := NewOfflineDownloadHTTPClient(http.Client{})
	req, err := http.NewRequest(http.MethodGet, "http://169.254.169.254/latest/meta-data/", nil)
	if err != nil {
		t.Fatalf("failed to build redirect request: %v", err)
	}
	err = client.CheckRedirect(req, nil)
	if !errors.Is(err, ErrCloudMetadataEndpoint) {
		t.Fatalf("expected cloud metadata error, got %v", err)
	}
}
