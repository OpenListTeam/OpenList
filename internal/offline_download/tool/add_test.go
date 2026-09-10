package tool

import (
	"testing"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	_115_open "github.com/OpenListTeam/OpenList/v4/drivers/115_open"
	_123 "github.com/OpenListTeam/OpenList/v4/drivers/123"
	_123_open "github.com/OpenListTeam/OpenList/v4/drivers/123_open"
	"github.com/OpenListTeam/OpenList/v4/drivers/guangyapan"
	"github.com/OpenListTeam/OpenList/v4/drivers/pikpak"
	"github.com/OpenListTeam/OpenList/v4/drivers/thunder"
	"github.com/OpenListTeam/OpenList/v4/drivers/thunder_browser"
	"github.com/OpenListTeam/OpenList/v4/drivers/thunderx"
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

func TestToolNameForStorage(t *testing.T) {
	tests := []struct {
		name    string
		storage driver.Driver
		want    string
	}{
		{name: "115 Cloud", storage: &_115.Pan115{}, want: "115 Cloud"},
		{name: "115 Open", storage: &_115_open.Open115{}, want: "115 Open"},
		{name: "123Pan", storage: &_123.Pan123{}, want: "123Pan"},
		{name: "123 Open", storage: &_123_open.Open123{}, want: "123 Open"},
		{name: "GuangYaPan", storage: &guangyapan.GuangYaPan{}, want: "GuangYaPan"},
		{name: "PikPak", storage: &pikpak.PikPak{}, want: "PikPak"},
		{name: "Thunder", storage: &thunder.Thunder{}, want: "Thunder"},
		{name: "ThunderX", storage: &thunderx.ThunderX{}, want: "ThunderX"},
		{name: "ThunderBrowser", storage: &thunder_browser.ThunderBrowser{}, want: "ThunderBrowser"},
		{name: "other", storage: nil, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := toolNameForStorage(tt.storage); got != tt.want {
				t.Fatalf("toolNameForStorage(%T) = %q, want %q", tt.storage, got, tt.want)
			}
		})
	}
}

func TestFileNameFromURL(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		expect string
	}{
		{
			name:   "plain path",
			url:    "https://example.com/path/file.txt",
			expect: "file.txt",
		},
		{
			name:   "encoded name decoded once",
			url:    "https://example.com/path/my%20file.txt",
			expect: "my file.txt",
		},
		{
			name:   "double-encoded slash is not double-decoded",
			url:    "https://example.com/path/a%252Fb.txt",
			expect: "a%2Fb.txt",
		},
		{
			name:   "double-encoded name decoded once",
			url:    "https://example.com/path/%2520.txt",
			expect: "%20.txt",
		},
		{
			name:   "query ignored",
			url:    "https://example.com/file.txt?sign=abc%2Fdef",
			expect: "file.txt",
		},
		{
			name:   "trailing slash",
			url:    "https://example.com/path/",
			expect: "path",
		},
		{
			name:   "root path",
			url:    "https://example.com/",
			expect: "",
		},
		{
			name:   "no path",
			url:    "https://example.com",
			expect: "",
		},
		{
			name:   "magnet link",
			url:    "magnet:?xt=urn:btih:0123456789abcdef",
			expect: "",
		},
		{
			name:   "invalid url",
			url:    "http://example.com/%zz\x7f",
			expect: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fileNameFromURL(tt.url); got != tt.expect {
				t.Errorf("fileNameFromURL(%q) = %q, want %q", tt.url, got, tt.expect)
			}
		})
	}
}
