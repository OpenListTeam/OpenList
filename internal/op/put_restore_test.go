package op_test

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"

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
}

func newNoOverwriteStorage(files map[string]int64) *noOverwriteStorage {
	d := &noOverwriteStorage{files: files, putting: make(chan struct{})}
	d.SetStorage(model.Storage{MountPath: "/" + strings.ReplaceAll(fmt.Sprintf("%p", d), "0x", "no-overwrite-")})
	return d
}

func (d *noOverwriteStorage) Config() driver.Config {
	return driver.Config{Name: "NoOverwriteTest", NoOverwriteUpload: true, NoCache: true, LocalSort: true}
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
		objs = append(objs, &model.Object{ID: name, Path: "/" + name, Name: name, Size: size})
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

func (d *noOverwriteStorage) listing() string {
	d.mu.Lock()
	defer d.mu.Unlock()
	var entries []string
	for name, size := range d.files {
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
	if got := d.listing(); got != "report.pdf.openlist_to_delete=100, report.pdf=200" {
		t.Fatalf("storage holds [%s], want the newer upload kept and the original left aside", got)
	}
}
