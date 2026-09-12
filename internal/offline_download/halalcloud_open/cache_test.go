package halalcloudopen

import (
	"context"
	"path"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type cacheTestDriver struct {
	model.Storage

	mu          sync.Mutex
	listings    map[string][]model.Obj
	listCalls   map[string]int
	blockPath   string
	listStarted chan struct{}
	listRelease chan struct{}
	blockOnce   sync.Once
}

func (d *cacheTestDriver) Config() driver.Config {
	return driver.Config{}
}

func (d *cacheTestDriver) GetAddition() driver.Additional {
	return nil
}

func (d *cacheTestDriver) Init(context.Context) error {
	return nil
}

func (d *cacheTestDriver) Drop(context.Context) error {
	return nil
}

func (d *cacheTestDriver) Get(_ context.Context, objectPath string) (model.Obj, error) {
	return &model.Object{
		Path:     objectPath,
		Name:     path.Base(objectPath),
		IsFolder: true,
	}, nil
}

func (d *cacheTestDriver) List(_ context.Context, dir model.Obj, _ model.ListArgs) ([]model.Obj, error) {
	d.mu.Lock()
	objectPath := dir.GetPath()
	d.listCalls[objectPath]++
	objects := append([]model.Obj(nil), d.listings[objectPath]...)
	d.mu.Unlock()

	if objectPath == d.blockPath {
		d.blockOnce.Do(func() {
			close(d.listStarted)
			<-d.listRelease
		})
	}
	return objects, nil
}

func (d *cacheTestDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, nil
}

func TestInvalidateDestinationCacheClearsDescendants(t *testing.T) {
	driver := &cacheTestDriver{
		Storage: model.Storage{
			MountPath:       "/halalcloud-cache-test-" + t.Name(),
			CacheExpiration: 60,
		},
		listings: map[string][]model.Obj{
			"/downloads": {
				&model.Object{Path: "/downloads/bundle", Name: "bundle", IsFolder: true},
			},
			"/downloads/bundle": {
				&model.Object{Path: "/downloads/bundle/old.txt", Name: "old.txt"},
			},
		},
		listCalls: make(map[string]int),
	}
	defer op.Cache.DeleteDirectoryTree(driver, "/downloads")

	ctx := context.Background()
	listArgs := model.ListArgs{SkipHook: true}
	if _, err := op.List(ctx, driver, "/downloads", listArgs); err != nil {
		t.Fatalf("List(destination) error = %v", err)
	}
	if _, err := op.List(ctx, driver, "/downloads/bundle", listArgs); err != nil {
		t.Fatalf("List(descendant) error = %v", err)
	}

	driver.mu.Lock()
	driver.listings["/downloads/bundle"] = []model.Obj{
		&model.Object{Path: "/downloads/bundle/new.txt", Name: "new.txt"},
	}
	driver.mu.Unlock()

	// The parent listing may expire independently while a descendant remains
	// cached; invalidation must still remove that descendant.
	op.Cache.DeleteDirectory(driver, "/downloads")
	invalidateDestinationCache(driver, "/downloads")

	objects, err := op.List(ctx, driver, "/downloads/bundle", listArgs)
	if err != nil {
		t.Fatalf("List(descendant after invalidation) error = %v", err)
	}
	if len(objects) != 1 || objects[0].GetName() != "new.txt" {
		t.Fatalf("List(descendant after invalidation) = %#v, want new.txt", objects)
	}

	driver.mu.Lock()
	calls := driver.listCalls["/downloads/bundle"]
	driver.mu.Unlock()
	if calls != 2 {
		t.Fatalf("descendant List call count = %d, want 2", calls)
	}
}

func TestInvalidateDestinationCachePreventsStaleInFlightWrite(t *testing.T) {
	driver := &cacheTestDriver{
		Storage: model.Storage{
			MountPath:       "/halalcloud-cache-race-" + t.Name(),
			CacheExpiration: 60,
		},
		listings: map[string][]model.Obj{
			"/downloads": {
				&model.Object{Path: "/downloads/old.txt", Name: "old.txt"},
			},
		},
		listCalls:   make(map[string]int),
		blockPath:   "/downloads",
		listStarted: make(chan struct{}),
		listRelease: make(chan struct{}),
	}
	defer op.Cache.DeleteDirectoryTree(driver, "/downloads")

	resultCh := make(chan []model.Obj, 1)
	errCh := make(chan error, 1)
	go func() {
		objects, err := op.List(context.Background(), driver, "/downloads", model.ListArgs{SkipHook: true})
		resultCh <- objects
		errCh <- err
	}()
	select {
	case <-driver.listStarted:
	case <-time.After(time.Second):
		t.Fatal("List() did not start")
	}

	driver.mu.Lock()
	driver.listings["/downloads"] = []model.Obj{
		&model.Object{Path: "/downloads/new.txt", Name: "new.txt"},
	}
	driver.mu.Unlock()
	invalidateDestinationCache(driver, "/downloads")
	close(driver.listRelease)

	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("initial List() error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("initial List() did not finish")
	}
	<-resultCh

	objects, err := op.List(context.Background(), driver, "/downloads", model.ListArgs{SkipHook: true})
	if err != nil {
		t.Fatalf("List() after invalidation error = %v", err)
	}
	if len(objects) != 1 || objects[0].GetName() != "new.txt" {
		t.Fatalf("List() after invalidation = %#v, want new.txt", objects)
	}

	driver.mu.Lock()
	calls := driver.listCalls["/downloads"]
	driver.mu.Unlock()
	if calls != 2 {
		t.Fatalf("List() call count = %d, want 2", calls)
	}
}

var _ driver.Driver = (*cacheTestDriver)(nil)
var _ driver.Getter = (*cacheTestDriver)(nil)
