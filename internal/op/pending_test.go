package op

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

// lagDriver has no driver.Getter (like BaiduNetdisk), so Get falls back to
// the parent listing. Its List can hide names to simulate an eventually
// consistent backend, and can block after taking its snapshot to simulate a
// List that started before a write and finishes after it.
type lagDriver struct {
	model.Storage
	mu        sync.Mutex
	files     map[string][]string
	hidden    map[string]bool
	afterSnap func(dir string)
	onPut     func()
	lists     atomic.Int32
}

func (d *lagDriver) Config() driver.Config          { return driver.Config{Name: "LagTest", LocalSort: true} }
func (d *lagDriver) GetAddition() driver.Additional { return &struct{}{} }
func (d *lagDriver) Init(context.Context) error     { return nil }
func (d *lagDriver) Drop(context.Context) error     { return nil }
func (d *lagDriver) GetRootPath() string            { return "/" }

func (d *lagDriver) List(_ context.Context, dir model.Obj, _ model.ListArgs) ([]model.Obj, error) {
	d.lists.Add(1)
	p := dir.GetPath()
	d.mu.Lock()
	var names []string
	for _, n := range d.files[p] {
		if !d.hidden[n] {
			names = append(names, n)
		}
	}
	hook := d.afterSnap
	if hook != nil {
		d.afterSnap = nil
	}
	d.mu.Unlock()
	if hook != nil {
		hook(p)
	}
	objs := make([]model.Obj, 0, len(names))
	for _, n := range names {
		objs = append(objs, &model.Object{
			Name:     n,
			Path:     strings.TrimSuffix(p, "/") + "/" + n,
			IsFolder: !strings.Contains(n, "."),
			Size:     1,
			Modified: time.Now(),
		})
	}
	return objs, nil
}

func (d *lagDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

// Put returns the created object, like BaiduNetdisk.
func (d *lagDriver) Put(_ context.Context, dstDir model.Obj, file model.FileStreamer, _ driver.UpdateProgress) (model.Obj, error) {
	p := dstDir.GetPath()
	d.mu.Lock()
	d.files[p] = append(d.files[p], file.GetName())
	hook := d.onPut
	d.mu.Unlock()
	if hook != nil {
		hook()
	}
	return &model.Object{
		Name:     file.GetName(),
		Path:     strings.TrimSuffix(p, "/") + "/" + file.GetName(),
		Size:     file.GetSize(),
		Modified: time.Now(),
	}, nil
}

func (d *lagDriver) Remove(_ context.Context, obj model.Obj) error {
	dir, name := splitTestPath(obj.GetPath())
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[dir] = slices.DeleteFunc(d.files[dir], func(n string) bool { return n == name })
	return nil
}

func (d *lagDriver) setHidden(name string, hidden bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.hidden[name] = hidden
}

// placeholderDriver's Put does not return the created object.
type placeholderDriver struct{ *lagDriver }

func (d placeholderDriver) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	_, err := d.lagDriver.Put(ctx, dstDir, file, up)
	return err
}

var (
	_ driver.PutResult = (*lagDriver)(nil)
	_ driver.Remove    = (*lagDriver)(nil)
	_ driver.Put       = placeholderDriver{}
)

func splitTestPath(p string) (string, string) {
	i := strings.LastIndex(p, "/")
	if i <= 0 {
		return "/", p[i+1:]
	}
	return p[:i], p[i+1:]
}

func newLagDriver(t *testing.T) *lagDriver {
	t.Helper()
	d := &lagDriver{
		files:  map[string][]string{"/": {"dir"}, "/dir": {"old.bin"}},
		hidden: map[string]bool{},
	}
	d.Storage = model.Storage{
		MountPath:       fmt.Sprintf("/pending-test-%s-%d", t.Name(), time.Now().UnixNano()),
		CacheExpiration: 30,
		Status:          WORK,
	}
	t.Cleanup(func() { Cache.DeleteDirectoryTree(d, "/") })
	return d
}

func putFile(t *testing.T, d driver.Driver, name string) {
	t.Helper()
	file := &stream.FileStream{
		Obj:    &model.Object{Name: name, Size: 1, Modified: time.Now()},
		Reader: strings.NewReader("x"),
	}
	if err := Put(context.Background(), d, "/dir", file, nil); err != nil {
		t.Fatalf("put %s: %v", name, err)
	}
}

// getFile mirrors the S3 HeadObject path (fs.Get -> op.Get without excludeTemp).
func getFile(d driver.Driver, name string) (model.Obj, error) {
	return Get(context.Background(), d, "/dir/"+name)
}

func listNames(t *testing.T, d driver.Driver, refresh bool) []string {
	t.Helper()
	objs, err := List(context.Background(), d, "/dir", model.ListArgs{Refresh: refresh})
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	names := make([]string, 0, len(objs))
	for _, o := range objs {
		names = append(names, o.GetName())
	}
	return names
}

// A List that snapshots the backend before Put and is cached after Put must
// not drop the written object.
func TestPendingSurvivesListStartedBeforePut(t *testing.T) {
	d := newLagDriver(t)
	listNames(t, d, false)

	snapped, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	d.mu.Lock()
	d.afterSnap = func(string) {
		close(snapped)
		<-release
	}
	d.mu.Unlock()
	go func() {
		defer close(done)
		_, _ = List(context.Background(), d, "/dir", model.ListArgs{Refresh: true})
	}()
	<-snapped
	putFile(t, d, "new.bin")
	close(release)
	<-done

	if _, err := getFile(d, "new.bin"); err != nil {
		t.Fatalf("get after put: %v", err)
	}
}

// If the directory cache expires during the upload, a following List that
// does not return the new name yet must not hide the written object.
func TestPendingSurvivesLaggingListWithoutDirCache(t *testing.T) {
	d := newLagDriver(t)
	d.setHidden("new.bin", true)
	d.onPut = func() { Cache.DeleteDirectory(d, "/dir") }
	putFile(t, d, "new.bin")

	if names := listNames(t, d, false); !slices.Contains(names, "new.bin") {
		t.Fatalf("listing should include pending object, got %v", names)
	}
	if _, err := getFile(d, "new.bin"); err != nil {
		t.Fatalf("get after put: %v", err)
	}
}

// Once the backend List returns the name, the pending entry is dropped and
// the remote object is used.
func TestPendingClearedWhenSeen(t *testing.T) {
	d := newLagDriver(t)
	d.setHidden("new.bin", true)
	putFile(t, d, "new.bin")
	listNames(t, d, true)
	if _, ok := getPending(Key(d, "/dir/"), "new.bin"); !ok {
		t.Fatal("pending entry should remain while the backend hides the name")
	}

	d.setHidden("new.bin", false)
	listNames(t, d, true)
	if _, ok := getPending(Key(d, "/dir/"), "new.bin"); ok {
		t.Fatal("pending entry should be dropped once the backend lists the name")
	}
	if _, err := getFile(d, "new.bin"); err != nil {
		t.Fatalf("get after seen: %v", err)
	}
}

// A missing name is still reported as not found from the cached listing,
// without an extra List.
func TestPendingMissDoesNotList(t *testing.T) {
	d := newLagDriver(t)
	listNames(t, d, false)
	before := d.lists.Load()

	if _, err := getFile(d, "missing.bin"); !errs.IsObjectNotFound(err) {
		t.Fatalf("want ObjectNotFound, got %v", err)
	}
	if got := d.lists.Load(); got != before {
		t.Fatalf("miss triggered %d extra List call(s)", got-before)
	}
}

// A removed object must not come back from a List that still returns it.
func TestPendingDeleteHidesStaleListing(t *testing.T) {
	d := newLagDriver(t)
	putFile(t, d, "new.bin")
	if err := Remove(context.Background(), d, "/dir/new.bin"); err != nil {
		t.Fatalf("remove: %v", err)
	}

	d.mu.Lock()
	d.files["/dir"] = append(d.files["/dir"], "new.bin") // stale backend listing
	d.mu.Unlock()
	if names := listNames(t, d, true); slices.Contains(names, "new.bin") {
		t.Fatalf("removed object reappeared in listing: %v", names)
	}
	if _, err := getFile(d, "new.bin"); !errs.IsObjectNotFound(err) {
		t.Fatalf("want ObjectNotFound after remove, got %v", err)
	}
}

// Placeholders created when a driver's Put returns no object keep the Temp
// mask, so callers that need the real object (excludeTemp) do not get them.
func TestPendingPlaceholderKeepsTempMask(t *testing.T) {
	d := placeholderDriver{newLagDriver(t)}
	d.setHidden("new.bin", true)
	putFile(t, d, "new.bin")

	if _, err := getFile(d, "new.bin"); err != nil {
		t.Fatalf("get placeholder: %v", err)
	}
	if _, err := Get(context.Background(), d, "/dir/new.bin", true); !errs.IsObjectNotFound(err) {
		t.Fatalf("excludeTemp get should not return placeholder, got %v", err)
	}
}

// Entries under a removed directory are dropped with it.
func TestPendingClearedWithDirectory(t *testing.T) {
	d := newLagDriver(t)
	putFile(t, d, "new.bin")
	clearPendingTree(Key(d, "/dir"))
	if _, ok := getPending(Key(d, "/dir/"), "new.bin"); ok {
		t.Fatal("pending entry should be cleared with its directory")
	}
}

// pendingTTL must reclaim directories that are never opened again. A later
// pending operation on a different directory is what drops them.
func TestPendingGCReclaimsUnvisitedExpiredDirs(t *testing.T) {
	clearAllPending()
	t.Cleanup(clearAllPending)

	const (
		dirA = "/pending-gc/a/"
		dirB = "/pending-gc/b/"
	)
	expired := &model.Object{Name: "expired.bin", Size: 1}
	alive := &model.Object{Name: "alive.bin", Size: 1}
	other := &model.Object{Name: "other.bin", Size: 1}

	expireAdd := func(dirKey, name string) {
		t.Helper()
		pendings.mu.Lock()
		defer pendings.mu.Unlock()
		d := pendings.dirs[dirKey]
		e := d.add[name]
		e.expiresAt = time.Now().Add(-time.Second)
		d.add[name] = e
	}

	triggers := []struct {
		name  string
		touch func()
	}{
		{name: "addPending", touch: func() { addPending(dirB, other) }},
		{name: "delPending", touch: func() { delPending(dirB, "gone.bin") }},
		{name: "getPending", touch: func() { getPending(dirB, "missing.bin") }},
		{name: "mergePending", touch: func() { mergePending(dirB, nil) }},
	}
	for _, tc := range triggers {
		t.Run(tc.name, func(t *testing.T) {
			clearAllPending()
			addPending(dirA, expired)
			expireAdd(dirA, expired.GetName())

			tc.touch()

			pendings.mu.Lock()
			defer pendings.mu.Unlock()
			if _, ok := pendings.dirs[dirA]; ok {
				t.Fatalf("expired directory %s still present after %s on %s", dirA, tc.name, dirB)
			}
		})
	}

	t.Run("expired delete", func(t *testing.T) {
		clearAllPending()
		delPending(dirA, "gone.bin")
		pendings.mu.Lock()
		pendings.dirs[dirA].del["gone.bin"] = time.Now().Add(-time.Second)
		pendings.mu.Unlock()

		addPending(dirB, other)

		pendings.mu.Lock()
		defer pendings.mu.Unlock()
		if _, ok := pendings.dirs[dirA]; ok {
			t.Fatalf("directory %s with only an expired delete still present", dirA)
		}
	})

	t.Run("unexpired survives", func(t *testing.T) {
		clearAllPending()
		addPending(dirA, expired)
		addPending(dirA, alive)
		expireAdd(dirA, expired.GetName())

		addPending(dirB, other)

		if obj, ok := getPending(dirA, alive.GetName()); !ok || obj != alive {
			t.Fatalf("unexpired pending add should stay visible, ok=%v", ok)
		}
		if _, ok := getPending(dirA, expired.GetName()); ok {
			t.Fatal("expired pending add should not stay visible")
		}

		pendings.mu.Lock()
		defer pendings.mu.Unlock()
		d, ok := pendings.dirs[dirA]
		if !ok {
			t.Fatal("directory A should remain while an entry is still unexpired")
		}
		if _, ok := d.add[expired.GetName()]; ok {
			t.Fatal("expired entry on A should be dropped by a sweep triggered from B")
		}
		got, ok := d.add[alive.GetName()]
		if !ok || got.obj != alive {
			t.Fatalf("unexpired entry on A should survive the sweep, ok=%v", ok)
		}
		if _, ok := pendings.dirs[dirB].add[other.GetName()]; !ok {
			t.Fatal("pending add on B should be recorded")
		}
	})
}
