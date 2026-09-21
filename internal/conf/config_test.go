package conf

import (
	"encoding/json"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
)

func TestDefaultCachePolicy(t *testing.T) {
	cfg := DefaultConfig(t.TempDir())
	if got := cfg.CachePolicy; got != cache.PolicyAuto {
		t.Fatalf("default cache policy = %q, want auto", got)
	}
	if err := json.Unmarshal([]byte(`{}`), cfg); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if got := cfg.CachePolicy; got != cache.PolicyAuto {
		t.Fatalf("cache policy after loading an old config = %q, want auto", got)
	}
}
