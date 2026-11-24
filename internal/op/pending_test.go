package op

import (
	"container/heap"
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

// A pending object participates in the configured local sort order.
func TestPendingAddRespectsListSort(t *testing.T) {
	d := newLagDriver(t)
	d.Storage.OrderBy = "name"
	d.setHidden("aaa.bin", true)
	putFile(t, d, "aaa.bin")

	if names := listNames(t, d, true); !slices.Equal(names, []string{"aaa.bin", "old.bin"}) {
		t.Fatalf("listing should preserve name order, got %v", names)
	}
}

// A placeholder must not replace a real object that a concurrent List cached.
func TestPendingPlaceholderDoesNotReplaceRemoteCache(t *testing.T) {
	d := newLagDriver(t)
	placeholder := placeholderDriver{d}
	d.onPut = func() {
		listNames(t, d, true)
	}

	putFile(t, placeholder, "new.bin")
	if cache, ok := Cache.dirCache.Get(Key(placeholder, "/dir")); ok {
		for _, obj := range cache.objs {
			if obj.GetName() == "new.bin" && model.ObjHasMask(obj, model.Temp) {
				t.Fatalf("cached remote object was replaced by placeholder: %+v", obj)
			}
		}
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
		for i := range pendings.expiries {
			if pendings.expiries[i].dirKey == dirKey && pendings.expiries[i].name == name && pendings.expiries[i].isAdd {
				pendings.expiries[i].expiresAt = e.expiresAt
			}
		}
		heap.Init(&pendings.expiries)
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
		for i := range pendings.expiries {
			if pendings.expiries[i].dirKey == dirA && pendings.expiries[i].name == "gone.bin" && !pendings.expiries[i].isAdd {
				pendings.expiries[i].expiresAt = pendings.dirs[dirA].del["gone.bin"]
			}
		}
		heap.Init(&pendings.expiries)
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

// objsObserve is invoked from the process-wide update hook. Tests publish a
// callback here instead of replacing objsUpdateHooks, which List reads from
// another goroutine.
type objsObserveFunc func(parent string, objs []model.Obj)

var objsObserve atomic.Pointer[objsObserveFunc]

func init() {
	RegisterObjsUpdateHook(func(_ context.Context, parent string, objs []model.Obj) {
		if observe := objsObserve.Load(); observe != nil {
			(*observe)(parent, objs)
		}
	})
}

func withObjsObserve(t *testing.T, fn objsObserveFunc) {
	t.Helper()
	objsObserve.Store(&fn)
	t.Cleanup(func() { objsObserve.Store(nil) })
}

// TestUpdateHookDoesNotShareResortSlice covers a List with nothing pending.
// mergePendingState then returns the caller's slice, and the post-merge name
// sort plus folder-front reorders it in place. The objs-update hook is already
// running on that slice, so the two must not share a backing array.
func TestUpdateHookDoesNotShareResortSlice(t *testing.T) {
	d := newLagDriver(t)
	d.Storage.OrderBy = "name"
	d.Storage.OrderDirection = "asc"
	d.Storage.ExtractFolder = "front"
	d.mu.Lock()
	d.files["/dir"] = []string{"b", "a.txt", "c.txt"}
	d.mu.Unlock()

	hookObjs := make(chan []model.Obj, 1)
	wantParent := Key(d, "/dir")
	withObjsObserve(t, func(parent string, objs []model.Obj) {
		if parent != wantParent {
			return
		}
		// Touch every slot. With -race this races the in-place re-sort when
		// the hook was handed the slice that sort still owns.
		for _, obj := range objs {
			_ = obj.GetName()
			_ = obj.IsDir()
		}
		select {
		case hookObjs <- objs:
		default:
		}
	})

	names := listNames(t, d, true)
	if !slices.Equal(names, []string{"b", "a.txt", "c.txt"}) {
		t.Fatalf("listing order = %v, want folder b then a.txt, c.txt", names)
	}

	var seen []model.Obj
	select {
	case seen = <-hookObjs:
	case <-time.After(2 * time.Second):
		t.Fatal("objs update hook did not run")
	}
	if len(seen) == 0 {
		t.Fatal("hook received an empty listing")
	}
	cache, ok := Cache.dirCache.Get(wantParent)
	if !ok || len(cache.objs) == 0 {
		t.Fatal("directory cache missing after list")
	}
	if &seen[0] == &cache.objs[0] {
		t.Fatal("update hook and cached listing share a slice that is sorted in place")
	}
}

// A pending add is still merged and ordered when folder-front sorting is on,
// including while an update hook reads the pre-merge listing.
func TestPendingAddRespectsFolderFrontWithUpdateHook(t *testing.T) {
	d := newLagDriver(t)
	d.Storage.OrderBy = "name"
	d.Storage.OrderDirection = "asc"
	d.Storage.ExtractFolder = "front"
	d.mu.Lock()
	d.files["/dir"] = []string{"b", "old.bin"}
	d.mu.Unlock()
	d.setHidden("aaa.bin", true)
	putFile(t, d, "aaa.bin")

	hookObjs := make(chan []model.Obj, 1)
	wantParent := Key(d, "/dir")
	withObjsObserve(t, func(parent string, objs []model.Obj) {
		if parent != wantParent {
			return
		}
		for _, obj := range objs {
			_ = obj.GetName()
			_ = obj.IsDir()
		}
		select {
		case hookObjs <- append([]model.Obj(nil), objs...):
		default:
		}
	})

	names := listNames(t, d, true)
	if !slices.Equal(names, []string{"b", "aaa.bin", "old.bin"}) {
		t.Fatalf("listing order = %v, want folder b then aaa.bin, old.bin", names)
	}
	select {
	case seen := <-hookObjs:
		if slices.Contains(seenNames(seen), "aaa.bin") {
			t.Fatalf("hook should see the remote listing without the pending add, got %v", seenNames(seen))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("objs update hook did not run")
	}
	if _, err := getFile(d, "aaa.bin"); err != nil {
		t.Fatalf("get pending add: %v", err)
	}
}

func seenNames(objs []model.Obj) []string {
	names := make([]string, len(objs))
	for i, obj := range objs {
		names[i] = obj.GetName()
	}
	return names
}
