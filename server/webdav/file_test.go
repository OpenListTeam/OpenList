package webdav

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestIsSameStorageSameDir(t *testing.T) {
	testCases := []struct {
		src, dstDir string
		want        bool
	}{
		{"/a/b/c.txt", "/a/b", true},
		{"/storage/data/main/file.tmp", "/storage/data/main", true},
		{"/dav/folder/sub/file.txt", "/dav/folder/sub", true},
		{"/file.txt", "/", true},
	}
	for _, tc := range testCases {
		if got := isSameStorage(tc.src, tc.dstDir); got != tc.want {
			t.Errorf("isSameStorage(%q, %q) = %v, want %v", tc.src, tc.dstDir, got, tc.want)
		}
	}
}

// moveProbe counts the driver operations a move is allowed to use. Renaming
// must not depend on file data, so any copy or link is a bug in the caller.
type moveProbe struct {
	copies  atomic.Int32
	links   atomic.Int32
	renames atomic.Int32
}

func (p *moveProbe) probe() *moveProbe { return p }

// renameOnlyDriver can rename but cannot transfer file data, like Quark.
// With claimDestination set, its first rename simulates another writer taking
// the destination after the handler's existence check, then refuses it.
type renameOnlyDriver struct {
	local.Local
	moveProbe
	root             string
	claimDestination atomic.Bool
	claimed          atomic.Bool
}

func (d *renameOnlyDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = "WebDAVMoveRegression"
	return c
}

func (d *renameOnlyDriver) Copy(context.Context, model.Obj, model.Obj) error {
	d.copies.Add(1)
	return errs.NotSupport
}

func (d *renameOnlyDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	d.links.Add(1)
	return nil, errors.New("file data transfer is unavailable")
}

func (d *renameOnlyDriver) Rename(ctx context.Context, obj model.Obj, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	d.renames.Add(1)
	if d.claimDestination.Load() && d.claimed.CompareAndSwap(false, true) {
		if err := os.WriteFile(filepath.Join(d.root, name), []byte("other writer"), 0o600); err != nil {
			return err
		}
		return errors.New("destination already exists")
	}
	return d.Local.Rename(ctx, obj, name)
}

type moveFixture struct {
	root  string
	mount string
	src   string
	dst   string
	probe *moveProbe
	ctx   context.Context
	h     *Handler
}

func setupMoveFixture(t *testing.T, newDriver func(root string) driver.Driver) *moveFixture {
	t.Helper()
	root := t.TempDir()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	instance := newDriver(root)
	op.RegisterDriver(func() driver.Driver { return instance })
	addition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatal(err)
	}
	mount := "/" + t.Name()
	id, err := op.CreateStorage(context.Background(), model.Storage{
		Driver: "WebDAVMoveRegression", MountPath: mount, Addition: addition,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(context.Background(), id); err != nil {
			t.Error(err)
		}
		sqlDB, _ := database.DB()
		_ = sqlDB.Close()
	})
	registered, err := op.GetStorageByMountPath(mount)
	if err != nil {
		t.Fatal(err)
	}
	user := &model.User{Username: "rename-only", BasePath: "/", Role: model.GENERAL, Permission: 1 << 4}
	return &moveFixture{
		root: root, mount: mount,
		src:   path.Join(mount, "original.mp4"),
		dst:   path.Join(mount, "renamed.mp4"),
		probe: registered.(interface{ probe() *moveProbe }).probe(),
		ctx:   context.WithValue(context.Background(), conf.UserKey, user),
		h:     &Handler{Prefix: "/dav", LockSystem: NewMemLS()},
	}
}

func setupRenameFixture(t *testing.T) *moveFixture {
	t.Helper()
	return setupMoveFixture(t, func(root string) driver.Driver { return &renameOnlyDriver{} })
}

func (f *moveFixture) move() *httptest.ResponseRecorder {
	r := httptest.NewRequest("MOVE", "http://example.test/dav"+f.src, nil).WithContext(f.ctx)
	r.Header.Set("Destination", "http://example.test/dav"+f.dst)
	r.Header.Set("Overwrite", "F")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

func (f *moveFixture) assertNoTransfer(t *testing.T) {
	t.Helper()
	if f.probe.copies.Load() != 0 || f.probe.links.Load() != 0 {
		t.Fatalf("rename accessed file data: copies=%d links=%d", f.probe.copies.Load(), f.probe.links.Load())
	}
}

// A destination created by another writer after the existence check must
// survive a MOVE that then fails.
func TestSameDirectoryMoveKeepsRecreatedDestination(t *testing.T) {
	f := setupMoveFixture(t, func(root string) driver.Driver {
		d := &renameOnlyDriver{root: root}
		d.claimDestination.Store(true)
		return d
	})
	if err := os.WriteFile(filepath.Join(f.root, "original.mp4"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	w := f.move()
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("MOVE status=%d want=%d body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	if b, err := os.ReadFile(filepath.Join(f.root, "renamed.mp4")); err != nil || string(b) != "other writer" {
		t.Fatalf("failed MOVE destroyed the destination created by another writer: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(f.root, "original.mp4")); err != nil || string(b) != "source" {
		t.Fatalf("failed MOVE destroyed the source: %v", err)
	}
	f.assertNoTransfer(t)
}

// A failing dead-property transaction must undo the move.
func TestSameDirectoryMoveRollsBackOnTransactionFailure(t *testing.T) {
	f := setupRenameFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "original.mp4"), []byte("source"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := db.GetDb().Create(&model.WebDAVProperty{Path: f.src, Namespace: "urn:test", Name: "label"}).Error; err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	injected := errors.New("simulated transaction failure")
	err := runDeadPropsFileMutation(ctx,
		func(tx *gorm.DB) error { return moveDeadProps(tx, f.src, f.dst) },
		func() (*fileMutation, error) {
			m, err := moveWithRollback(ctx, f.src, f.dst, f.mount, f.mount, "original.mp4", "renamed.mp4", false, true)
			if err != nil {
				return m, err
			}
			cancel() // compensation must not inherit the failed request's cancellation
			return m, injected
		},
	)
	if !errors.Is(err, injected) {
		t.Fatalf("unexpected error: %v", err)
	}
	f.assertNoTransfer(t)
	if b, err := os.ReadFile(filepath.Join(f.root, "original.mp4")); err != nil || string(b) != "source" {
		t.Fatalf("rollback lost source: %v", err)
	}
	if _, err := os.Stat(filepath.Join(f.root, "renamed.mp4")); !os.IsNotExist(err) {
		t.Fatalf("rollback left destination: %v", err)
	}
	var props []model.WebDAVProperty
	if err := db.GetDb().Find(&props).Error; err != nil || len(props) != 1 || props[0].Path != f.src {
		t.Fatalf("dead properties did not roll back: %+v, %v", props, err)
	}
}

// A source recreated while the dead-property transaction was running must be
// preserved by the rollback instead of overwritten.
func TestSameDirectoryMoveRollbackPreservesRecreatedSource(t *testing.T) {
	f := setupRenameFixture(t)
	if err := os.WriteFile(filepath.Join(f.root, "original.mp4"), []byte("original content"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := moveWithRollback(f.ctx, f.src, f.dst, f.mount, f.mount, "original.mp4", "renamed.mp4", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "original.mp4"), []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.rollback(); err == nil {
		content, _ := os.ReadFile(filepath.Join(f.root, "original.mp4"))
		t.Fatalf("rollback should refuse to overwrite a recreated source; source now contains %q", content)
	}
	for name, want := range map[string]string{"original.mp4": "new content", "renamed.mp4": "original content"} {
		if b, err := os.ReadFile(filepath.Join(f.root, name)); err != nil || string(b) != want {
			t.Fatalf("rollback changed %s: %v", name, err)
		}
	}
	f.assertNoTransfer(t)
}

// A cross-directory rollback renames the object back to its source name inside
// the destination directory before moving it home. A source name recreated in
// the meantime must survive that first rename and still stop the second step.
func TestCrossDirectoryMoveRollbackPreservesRecreatedSource(t *testing.T) {
	f := setupRenameFixture(t)
	srcDir, dstDir := path.Join(f.mount, "a"), path.Join(f.mount, "b")
	src, dst := path.Join(srcDir, "original.mp4"), path.Join(dstDir, "renamed.mp4")
	for _, dir := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(f.root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(f.root, "a", "original.mp4"), []byte("original content"), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err := moveWithRollback(f.ctx, src, dst, dstDir, srcDir, "original.mp4", "renamed.mp4", false, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, "a", "original.mp4"), []byte("new content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := m.rollback(); err == nil {
		t.Fatal("rollback should refuse to overwrite a recreated source")
	}
	for name, want := range map[string]string{
		path.Join("a", "original.mp4"): "new content",
		path.Join("b", "original.mp4"): "original content",
	} {
		if b, err := os.ReadFile(filepath.Join(f.root, name)); err != nil || string(b) != want {
			t.Fatalf("rollback changed %s: %v", name, err)
		}
	}
	f.assertNoTransfer(t)
}
