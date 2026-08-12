package tool

import (
	"testing"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	_115_open "github.com/OpenListTeam/OpenList/v4/drivers/115_open"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

func TestIsEd2kCapableTool(t *testing.T) {
	tests := []struct {
		name string
		want bool
	}{
		{name: "115 Cloud", want: true},
		{name: "115 Open", want: true},
		{name: "Thunder", want: true},
		{name: "ThunderX", want: true},
		{name: "ThunderBrowser", want: true},
		{name: "aria2", want: false},
		{name: "SimpleHttp", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isEd2kCapableTool(tt.name); got != tt.want {
				t.Fatalf("isEd2kCapableTool(%q) = %v, want %v", tt.name, got, tt.want)
			}
		})
	}
}

func TestEd2kToolForStorage(t *testing.T) {
	tests := []struct {
		name    string
		storage driver.Driver
		want    string
	}{
		{name: "115 Cloud", storage: &_115.Pan115{}, want: "115 Cloud"},
		{name: "115 Open", storage: &_115_open.Open115{}, want: "115 Open"},
		{name: "other", storage: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ed2kToolForStorage(tt.storage); got != tt.want {
				t.Fatalf("ed2kToolForStorage(%T) = %q, want %q", tt.storage, got, tt.want)
			}
		})
	}
}
