package op

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type linkLifecycleDriver struct {
	model.Storage
	links func() *model.Link
	calls atomic.Int32
}

func (d *linkLifecycleDriver) Config() driver.Config          { return driver.Config{} }
func (d *linkLifecycleDriver) GetAddition() driver.Additional { return nil }
func (d *linkLifecycleDriver) Init(context.Context) error     { return nil }
func (d *linkLifecycleDriver) Drop(context.Context) error     { return nil }
func (d *linkLifecycleDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}
func (d *linkLifecycleDriver) Get(context.Context, string) (model.Obj, error) {
	return &model.Object{Name: "file", Path: "/file"}, nil
}
func (d *linkLifecycleDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	d.calls.Add(1)
	return d.links(), nil
}

func resetLinkLifecycleState(t *testing.T) {
	t.Helper()
	oldCache := Cache
	Cache, linkG = NewCacheManager(), singleflight.Group[*objWithLink]{}
	t.Cleanup(func() { Cache, linkG = oldCache, singleflight.Group[*objWithLink]{} })
}

func acquireTestLink(t *testing.T, d *linkLifecycleDriver) *model.Link {
	t.Helper()
	link, _, err := Link(context.Background(), d, "/file", model.LinkArgs{})
	if err != nil {
		t.Fatal(err)
	}
	return link
}

func TestLinkLifecycleModes(t *testing.T) {
	t.Run("TTL descriptor remains reusable after close", func(t *testing.T) {
		resetLinkLifecycleState(t)
		ttl := time.Minute
		d := &linkLifecycleDriver{
			Storage: model.Storage{MountPath: "/ttl"},
			links:   func() *model.Link { return &model.Link{URL: "https://example.test/file", Expiration: &ttl} },
		}

		first := acquireTestLink(t, d)
		_ = first.Close()
		second := acquireTestLink(t, d)
		if second.URL != first.URL || d.calls.Load() != 1 {
			t.Fatalf("TTL link was not reused: calls=%d", d.calls.Load())
		}
		_ = second.Close()
	})

	t.Run("references keep shared resources alive until final close", func(t *testing.T) {
		resetLinkLifecycleState(t)
		var closes atomic.Int32
		d := &linkLifecycleDriver{
			Storage: model.Storage{MountPath: "/reference"},
			links: func() *model.Link {
				return &model.Link{
					URL:              "https://example.test/file",
					SyncClosers:      utils.NewSyncClosers(utils.CloseFunc(func() error { closes.Add(1); return nil })),
					RequireReference: true,
				}
			},
		}

		first := acquireTestLink(t, d)
		second := acquireTestLink(t, d)
		_ = first.Close()
		if closes.Load() != 0 {
			t.Fatal("shared resource closed while another reference was active")
		}
		_ = second.Close()
		if closes.Load() != 1 {
			t.Fatalf("final close count = %d, want 1", closes.Load())
		}
		third := acquireTestLink(t, d)
		_ = third.Close()
		if d.calls.Load() != 2 || closes.Load() != 2 {
			t.Fatalf("stale link was not replaced: calls=%d closes=%d", d.calls.Load(), closes.Load())
		}
	})

	t.Run("close-invalidated link is reacquired", func(t *testing.T) {
		resetLinkLifecycleState(t)
		d := &linkLifecycleDriver{
			Storage: model.Storage{MountPath: "/close-invalidated"},
			links: func() *model.Link {
				return &model.Link{SyncClosers: utils.NewSyncClosers(utils.CloseFunc(func() error { return nil }))}
			},
		}

		first := acquireTestLink(t, d)
		_ = first.Close()
		second := acquireTestLink(t, d)
		_ = second.Close()
		if d.calls.Load() != 2 {
			t.Fatalf("driver calls = %d, want 2", d.calls.Load())
		}
	})

	t.Run("TTL with owned resources is rejected and released", func(t *testing.T) {
		resetLinkLifecycleState(t)
		ttl := time.Minute
		var closes atomic.Int32
		d := &linkLifecycleDriver{
			Storage: model.Storage{MountPath: "/conflict"},
			links: func() *model.Link {
				return &model.Link{
					Expiration:       &ttl,
					SyncClosers:      utils.NewSyncClosers(utils.CloseFunc(func() error { closes.Add(1); return nil })),
					RequireReference: true,
				}
			},
		}

		_, _, err := Link(context.Background(), d, "/file", model.LinkArgs{})
		if err == nil || !strings.Contains(err.Error(), "expiration cannot be combined") {
			t.Fatalf("unexpected error: %v", err)
		}
		if closes.Load() != 1 {
			t.Fatalf("rejected link close count = %d, want 1", closes.Load())
		}
	})
}
