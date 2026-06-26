package pds

import (
	"context"
	"testing"
)

func TestInitRequiresToken(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			DomainID: "domain",
			DriveID:  "drive",
		},
	}

	if err := driver.Init(context.Background()); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestInitAcceptsRefreshTokenOnly(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			DomainID:     "domain",
			DriveID:      "drive",
			RefreshToken: "refresh",
		},
	}

	if err := driver.Init(context.Background()); err != nil {
		t.Fatalf("expected refresh token to be enough, got %v", err)
	}
	if driver.RootFolderID != "root" {
		t.Fatalf("expected default root folder id, got %q", driver.RootFolderID)
	}
}

func TestEscapeQueryValue(t *testing.T) {
	got := escapeQueryValue(`a\b"c`)
	want := `a\\b\"c`
	if got != want {
		t.Fatalf("escapeQueryValue() = %q, want %q", got, want)
	}
}
