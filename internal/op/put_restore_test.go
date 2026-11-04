package op_test

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

// noOverwriteStorage stands in for the cloud drivers with NoOverwriteUpload
// (aliyundrive_open, dropbox, quark_open, ...): a flat folder of name→size
// whose operations honour the request context, as their HTTP clients do.
type noOverwriteStorage struct {
	model.Storage
	mu      sync.Mutex
	files   map[string]int64
	putting chan struct{}
	// whilePutting runs inside Put before it waits, e.g. to let another
	// upload take the name meanwhile
	whilePutting func()
	// cached lets op cache this storage's listings
	cached bool
	// folders lists the names that are folders
	folders map[string]bool
}

// noOverwriteSeq numbers the test storages. op caches listings, and lets
// uploads take turns, by mount path: a mount path made of a storage's address
// is taken again once the storage is collected, and the new one then got an
// earlier test's cached listing.
var noOverwriteSeq atomic.Int64

func newNoOverwriteStorage(files map[string]int64) *noOverwriteStorage {
	d := &noOverwriteStorage{files: files, putting: make(chan struct{})}
	d.SetStorage(model.Storage{MountPath: fmt.Sprintf("/no-overwrite-%d", noOverwriteSeq.Add(1))})
	return d
}

func (d *noOverwriteStorage) Config() driver.Config {
	return driver.Config{Name: "NoOverwriteTest", NoOverwriteUpload: true, NoCache: !d.cached, LocalSort: true}
}
func (d *noOverwriteStorage) GetAddition() driver.Additional { return &struct{}{} }
func (d *noOverwriteStorage) Init(context.Context) error     { return nil }
func (d *noOverwriteStorage) Drop(context.Context) error     { return nil }
func (d *noOverwriteStorage) GetRoot(context.Context) (model.Obj, error) {
	return &model.Object{ID: "root", Path: "/", Name: "root", IsFolder: true}, nil
}

func (d *noOverwriteStorage) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	var objs []model.Obj
	for name, size := range d.files {
		objs = append(objs, &model.Object{ID: name, Path: "/" + name, Name: name, Size: size, IsFolder: d.folders[name]})
	}
	return objs, nil
}

func (d *noOverwriteStorage) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, errs.NotImplement
}

func (d *noOverwriteStorage) Rename(ctx context.Context, src model.Obj, newName string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	d.files[newName] = d.files[src.GetName()]
	delete(d.files, src.GetName())
	return nil
}

func (d *noOverwriteStorage) Remove(ctx context.Context, obj model.Obj) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.files, obj.GetName())
	return nil
}

// Put never receives its data: like a multipart attempt whose session gets
// aborted, superseded or expired, it only ends when its context is canceled.
func (d *noOverwriteStorage) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	if d.whilePutting != nil {
		d.whilePutting()
	}
	close(d.putting)
	<-ctx.Done()
	return ctx.Err()
}

// movedAside matches the name Put moves an original aside to.
var movedAside = regexp.MustCompile(`\.openlist_to_delete\.[0-9A-Za-z]{8}$`)

// listing lists the files as name=size, with the original moved aside listed
// as <name>.openlist_to_delete.*.
func (d *noOverwriteStorage) listing() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []string
	for name, size := range d.files {
		name = movedAside.ReplaceAllString(name, ".openlist_to_delete.*")
		entries = append(entries, fmt.Sprintf("%s=%d", name, size))
	}
	sort.Strings(entries)
	return strings.Join(entries, ", ")
}

func cancelPendingOverwrite(t *testing.T, d *noOverwriteStorage) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		file := &stream.FileStream{Obj: &model.Object{Name: "report.pdf", Size: 50}, Reader: strings.NewReader("")}
		done <- op.Put(ctx, d, "/", file, nil)
	}()
	<-d.putting
	cancel()
	if err := <-done; err == nil {
		t.Fatal("the canceled upload reported success")
	}
}

// The original is renamed away while the upload runs and must come back when
// the upload is canceled, even though the canceled context is all the upload
// had: restoring with it stranded the original as <name>.openlist_to_delete.
func TestPutRestoresOriginalWhenCanceled(t *testing.T) {
	d := newNoOverwriteStorage(map[string]int64{"report.pdf": 100})
	cancelPendingOverwrite(t, d)
	if got := d.listing(); got != "report.pdf=100" {
		t.Fatalf("storage holds [%s], want the original restored as [report.pdf=100]", got)
	}
}

// When another upload took the name meanwhile, the newer file wins: the
// original stays under its temporary name instead of being renamed over the
// newer one (some drivers then delete the "duplicate", i.e. the newer file).
func TestPutDoesNotRestoreOverNewerUpload(t *testing.T) {
	d := newNoOverwriteStorage(map[string]int64{"report.pdf": 100})
	d.whilePutting = func() {
		d.mu.Lock()
		d.files["report.pdf"] = 200 // a newer upload of the same name finished
		d.mu.Unlock()
	}
	cancelPendingOverwrite(t, d)
	if got := d.listing(); got != "report.pdf.openlist_to_delete.*=100, report.pdf=200" {
		t.Fatalf("storage holds [%s], want the newer upload kept and the original left aside", got)
	}
}

// A folder may have the name of the file being uploaded. It is no previous
// version of that file: the upload is refused and the folder left alone. Put
// took a folder listed with size 0 (SMB, most cloud drives) for an empty file
// and removed it with everything in it, and moved a larger one aside to remove
// it once the upload succeeded.
func TestPutRefusesToReplaceAFolder(t *testing.T) {
	for _, size := range []int64{0, 4096} {
		d := newNoOverwriteStorage(map[string]int64{"report": size})
		d.folders = map[string]bool{"report": true}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		file := &stream.FileStream{Obj: &model.Object{Name: "report", Size: 50}, Reader: strings.NewReader("")}
		err := op.Put(ctx, d, "/", file, nil)
		cancel()
		if !errors.Is(err, errs.NotFile) {
			t.Errorf("folder of size %d: Put = %v, want errs.NotFile", size, err)
		}
		if got, want := d.listing(), fmt.Sprintf("report=%d", size); got != want {
			t.Errorf("folder of size %d: storage holds [%s], want [%s] untouched", size, got, want)
		}
	}
}

// panickingPut is a noOverwriteStorage whose driver panics while uploading.
type panickingPut struct{ *noOverwriteStorage }

func (d *panickingPut) Put(context.Context, model.Obj, model.FileStreamer, driver.UpdateProgress) error {
	panic("driver bug")
}

// A driver that panics did not upload anything: the original comes back. The
// panic ended Put with no error set, which read as a success, so the original
// was removed.
func TestPutRestoresOriginalWhenTheUploadPanics(t *testing.T) {
	base := newNoOverwriteStorage(map[string]int64{"report.pdf": 100})
	d := &panickingPut{base}
	func() {
		defer func() {
			if r := recover(); r == nil {
				t.Error("the panic did not reach the caller")
			}
		}()
		file := &stream.FileStream{Obj: &model.Object{Name: "report.pdf", Size: 50}, Reader: strings.NewReader("")}
		_ = op.Put(context.Background(), d, "/", file, nil)
	}()
	if got := base.listing(); got != "report.pdf=100" {
		t.Fatalf("storage holds [%s], want the original restored as [report.pdf=100]", got)
	}
}

// The original moves aside under a name of its own, which nothing else has.
// The usual one may hold an original an earlier upload had to leave aside,
// here or on another server since this one cached the folder, and renaming
// over it lost what it held (some drivers delete it as a duplicate).
func TestPutMovesTheOriginalAsideUnderANameOfItsOwn(t *testing.T) {
	for _, pastCache := range []bool{false, true} {
		d := newNoOverwriteStorage(map[string]int64{"report.pdf": 100})
		if pastCache {
			d.cached = true
			d.GetStorage().CacheExpiration = 60
			if _, err := op.List(context.Background(), d, "/", model.ListArgs{}); err != nil {
				t.Fatal(err)
			}
		}
		d.mu.Lock()
		d.files["report.pdf.openlist_to_delete"] = 777
		d.mu.Unlock()
		cancelPendingOverwrite(t, d)
		if got := d.listing(); got != "report.pdf.openlist_to_delete=777, report.pdf=100" {
			t.Fatalf("left past the cache %v: storage holds [%s], want both files as they were", pastCache, got)
		}
	}
}
