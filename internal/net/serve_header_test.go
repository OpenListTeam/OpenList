package net

import (
	"net/http"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// The client must not be able to smuggle credential or routing headers into the
// request that this server makes to the upstream storage, even when the
// proxy_ignore_headers setting has been emptied.
func TestProcessHeaderDropsUnsafeClientHeaders(t *testing.T) {
	conf.SlicesMap[conf.ProxyIgnoreHeaders] = nil

	origin := http.Header{}
	origin.Set("Authorization", "Bearer victim-token")
	origin.Set("Cookie", "session=victim")
	origin.Set("X-Forwarded-For", "127.0.0.1")
	origin.Set("Host", "internal.example")
	origin.Set("Range", "bytes=0-1023")

	result := ProcessHeader(origin, nil)

	for _, h := range []string{"Authorization", "Cookie", "X-Forwarded-For", "Host"} {
		if got := result.Get(h); got != "" {
			t.Errorf("header %q must not be forwarded upstream, got %q", h, got)
		}
	}
	if got := result.Get("Range"); got != "bytes=0-1023" {
		t.Errorf("Range must be preserved, got %q", got)
	}
}

// Headers supplied by the storage driver still win, since they carry the
// credentials needed to reach upstream.
func TestProcessHeaderOverrideWins(t *testing.T) {
	conf.SlicesMap[conf.ProxyIgnoreHeaders] = nil

	origin := http.Header{}
	origin.Set("Authorization", "Bearer victim-token")

	override := http.Header{}
	override.Set("Authorization", "Bearer driver-token")

	result := ProcessHeader(origin, override)
	if got := result.Get("Authorization"); got != "Bearer driver-token" {
		t.Errorf("driver header must be used, got %q", got)
	}
}

func TestProcessHeaderStillHonoursIgnoreSetting(t *testing.T) {
	conf.SlicesMap[conf.ProxyIgnoreHeaders] = []string{"x-custom"}
	t.Cleanup(func() { conf.SlicesMap[conf.ProxyIgnoreHeaders] = nil })

	origin := http.Header{}
	origin.Set("X-Custom", "drop-me")
	origin.Set("X-Keep", "keep-me")

	result := ProcessHeader(origin, nil)
	if got := result.Get("X-Custom"); got != "" {
		t.Errorf("configured ignore header must be dropped, got %q", got)
	}
	if got := result.Get("X-Keep"); got != "keep-me" {
		t.Errorf("unrelated header must be preserved, got %q", got)
	}
}
