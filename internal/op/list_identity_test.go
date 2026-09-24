package op

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type listIdentityDriver struct {
	model.Storage
	list func(model.ListArgs) []model.Obj
}

func newListIdentityDriver(mount string, list func(model.ListArgs) []model.Obj) *listIdentityDriver {
	return &listIdentityDriver{
		Storage: model.Storage{MountPath: mount, CacheExpiration: 10},
		list:    list,
	}
}

func (d *listIdentityDriver) Config() driver.Config { return driver.Config{} }
func (d *listIdentityDriver) GetAddition() driver.Additional {
	return nil
}
func (d *listIdentityDriver) Init(context.Context) error { return nil }
func (d *listIdentityDriver) Drop(context.Context) error { return nil }
func (d *listIdentityDriver) GetRoot(context.Context) (model.Obj, error) {
	return &model.Object{Name: "root", Path: "/", IsFolder: true}, nil
}
func (d *listIdentityDriver) List(_ context.Context, _ model.Obj, args model.ListArgs) ([]model.Obj, error) {
	return d.list(args), nil
}
func (d *listIdentityDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func listedName(t *testing.T, objs []model.Obj, err error) string {
	t.Helper()
	if err != nil {
		t.Fatalf("list failed: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("expected one object, got %d", len(objs))
	}
	return objs[0].GetName()
}

func listName(t *testing.T, d driver.Driver, args model.ListArgs) string {
	t.Helper()
	objs, err := List(context.Background(), d, "/", args)
	return listedName(t, objs, err)
}

func TestDirectoryCacheOwnsSnapshotViews(t *testing.T) {
	original := []model.Obj{&model.Object{Name: "before"}}
	cached := newDirectoryCache(original)
	original[0] = &model.Object{Name: "after"}
	if got := cached.GetSortedObjects(&listIdentityDriver{}); got[0].GetName() != "before" {
		t.Fatalf("cached snapshot changed through caller slice: %q", got[0].GetName())
	}
}

func TestListNormalizesCanonicalRequestPath(t *testing.T) {
	Cache = NewCacheManager()
	var calls atomic.Int32
	d := newListIdentityDriver("/canonical", func(args model.ListArgs) []model.Obj {
		calls.Add(1)
		return []model.Obj{&model.Object{Name: args.ReqPath}}
	})

	if got := listName(t, d, model.ListArgs{}); got != "/canonical" {
		t.Fatalf("canonical request path = %q, want /canonical", got)
	}
	if got := listName(t, d, model.ListArgs{}); got != "/canonical" {
		t.Fatalf("cached canonical request path = %q, want /canonical", got)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("canonical driver calls = %d, want 1", got)
	}
}

func TestListDoesNotReuseSpecialVariants(t *testing.T) {
	tests := []struct {
		name   string
		mount  string
		first  model.ListArgs
		second model.ListArgs
		label  func(model.ListArgs) string
	}{
		{
			name:   "request path",
			mount:  "/request-path",
			first:  model.ListArgs{ReqPath: "/alias-a"},
			second: model.ListArgs{ReqPath: "/alias-b"},
			label:  func(args model.ListArgs) string { return args.ReqPath },
		},
		{
			name:   "placeholder visibility",
			mount:  "/placeholder",
			first:  model.ListArgs{ReqPath: "/placeholder"},
			second: model.ListArgs{ReqPath: "/placeholder", S3ShowPlaceholder: true},
			label:  func(args model.ListArgs) string { return fmt.Sprintf("placeholder=%t", args.S3ShowPlaceholder) },
		},
		{
			name:   "storage details",
			mount:  "/details",
			first:  model.ListArgs{ReqPath: "/details"},
			second: model.ListArgs{ReqPath: "/details", WithStorageDetails: true},
			label:  func(args model.ListArgs) string { return fmt.Sprintf("details=%t", args.WithStorageDetails) },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			Cache = NewCacheManager()
			var calls atomic.Int32
			d := newListIdentityDriver(tt.mount, func(args model.ListArgs) []model.Obj {
				calls.Add(1)
				return []model.Obj{&model.Object{Name: tt.label(args)}}
			})

			first := listName(t, d, tt.first)
			second := listName(t, d, tt.second)
			if first == second {
				t.Fatalf("different list variants shared %q", first)
			}
			if got := calls.Load(); got != 2 {
				t.Fatalf("driver calls = %d, want 2", got)
			}
		})
	}
}

func TestRefreshDoesNotJoinOrLoseToOrdinaryLoad(t *testing.T) {
	Cache = NewCacheManager()
	normalStarted := make(chan struct{})
	refreshStarted := make(chan struct{})
	releaseNormal := make(chan struct{})
	d := newListIdentityDriver("/refresh", func(args model.ListArgs) []model.Obj {
		if args.Refresh {
			close(refreshStarted)
			return []model.Obj{&model.Object{Name: "fresh"}}
		}
		close(normalStarted)
		<-releaseNormal
		return []model.Obj{&model.Object{Name: "ordinary"}}
	})

	type result struct {
		objs []model.Obj
		err  error
	}
	normalResult := make(chan result, 1)
	go func() {
		objs, err := List(context.Background(), d, "/", model.ListArgs{})
		normalResult <- result{objs: objs, err: err}
	}()
	<-normalStarted

	refreshResult := make(chan result, 1)
	go func() {
		objs, err := List(context.Background(), d, "/", model.ListArgs{Refresh: true})
		refreshResult <- result{objs: objs, err: err}
	}()

	select {
	case <-refreshStarted:
	case <-time.After(100 * time.Millisecond):
		close(releaseNormal)
		<-normalResult
		<-refreshResult
		t.Fatal("refresh joined an ordinary in-flight load")
	}
	refresh := <-refreshResult
	if got := listedName(t, refresh.objs, refresh.err); got != "fresh" {
		t.Fatalf("refresh result = %q, want fresh", got)
	}
	close(releaseNormal)
	normal := <-normalResult
	if got := listedName(t, normal.objs, normal.err); got != "ordinary" {
		t.Fatalf("ordinary result = %q, want ordinary", got)
	}

	if got := listName(t, d, model.ListArgs{}); got != "fresh" {
		t.Fatalf("cached result after refresh = %q, want fresh", got)
	}
}

func TestMutationInvalidatesInFlightListCommit(t *testing.T) {
	Cache = NewCacheManager()
	started := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	d := newListIdentityDriver("/mutation", func(model.ListArgs) []model.Obj {
		if calls.Add(1) == 1 {
			close(started)
			<-release
			return []model.Obj{&model.Object{Name: "stale"}}
		}
		return []model.Obj{&model.Object{Name: "fresh"}}
	})

	done := make(chan struct{})
	go func() {
		_, _ = List(context.Background(), d, "/", model.ListArgs{})
		close(done)
	}()
	<-started
	Cache.mutateDirectories([]string{Key(d, "/")}, func() {})
	close(release)
	<-done

	if got := listName(t, d, model.ListArgs{}); got != "fresh" {
		t.Fatalf("cached stale in-flight result: got %q", got)
	}
}

type compositeListDriver struct {
	*listIdentityDriver
	backing driver.Driver
}

func (d *compositeListDriver) List(ctx context.Context, _ model.Obj, _ model.ListArgs) ([]model.Obj, error) {
	return List(ctx, d.backing, "/", model.ListArgs{Refresh: true})
}

func TestNestedListProjectsOnlyPublicSnapshot(t *testing.T) {
	Cache = NewCacheManager()
	backing := newListIdentityDriver("/backing", func(model.ListArgs) []model.Obj {
		return []model.Obj{&model.Object{Name: "file"}}
	})
	outer := &compositeListDriver{
		listIdentityDriver: newListIdentityDriver("/public", nil),
		backing:            backing,
	}
	var projected []string
	SetSnapshotProjector(func(_ context.Context, parent string, _ []model.Obj) {
		projected = append(projected, parent)
	})
	t.Cleanup(func() { SetSnapshotProjector(nil) })

	if got := listName(t, outer, model.ListArgs{Refresh: true}); got != "file" {
		t.Fatalf("outer result = %q, want file", got)
	}
	if len(projected) != 1 || projected[0] != "/public" {
		t.Fatalf("projected parents = %v, want [/public]", projected)
	}
}

var _ driver.Driver = (*listIdentityDriver)(nil)
var _ driver.GetRooter = (*listIdentityDriver)(nil)
