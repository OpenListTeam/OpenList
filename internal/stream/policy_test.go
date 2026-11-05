package stream

import (
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type fileStreamerWithoutCachePolicy struct {
	model.FileStreamer
}

func TestGetCachePolicyOptionalInterface(t *testing.T) {
	previous := conf.CachePolicy
	conf.CachePolicy = cache.PolicyDisk
	t.Cleanup(func() { conf.CachePolicy = previous })

	file := fileStreamerWithoutCachePolicy{FileStreamer: &FileStream{}}
	policy, err := getCachePolicy(file)
	if err != nil {
		t.Fatalf("getCachePolicy() error = %v", err)
	}
	if policy != cache.PolicyDisk {
		t.Fatalf("getCachePolicy() = %q, want disk", policy)
	}
}
