package db

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestConvertLegacyRole(t *testing.T) {
	tests := []struct {
		name    string
		raw     string
		want    int
		needFix bool
	}{
		{"current admin", "2", 0, false},
		{"current guest", "1", 0, false},
		{"current general", "0", 0, false},
		{"legacy admin", "[2]", model.ADMIN, true},
		{"legacy guest", "[1]", model.GUEST, true},
		{"legacy general", "[0]", model.GENERAL, true},
		{"legacy new general", "[3]", model.GENERAL, true},
		{"legacy multi role prefers admin", "[0,2]", model.ADMIN, true},
		{"legacy multi role prefers guest", "[0,1]", model.GUEST, true},
		{"legacy with spaces", " [2] ", model.ADMIN, true},
		{"empty", "", 0, false},
		{"unknown format", "admin", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, needFix := convertLegacyRole(tt.raw)
			if needFix != tt.needFix {
				t.Fatalf("convertLegacyRole(%q) needFix = %v, want %v", tt.raw, needFix, tt.needFix)
			}
			if needFix && got != tt.want {
				t.Fatalf("convertLegacyRole(%q) = %d, want %d", tt.raw, got, tt.want)
			}
		})
	}
}
