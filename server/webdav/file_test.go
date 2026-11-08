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
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// moveDriver stands in for a cloud driver. With flagged set it declares
// NoOverwriteRename and keeps that promise by refusing a rename onto an
// existing name. It counts Copy calls, which a rename needs no data transfer for.
type moveDriver struct {
	local.Local
	root             string
	name             string
	flagged          bool
	copies           atomic.Int32
	claimDestination atomic.Bool
	claimed          atomic.Bool
}

func (d *moveDriver) Config() driver.Config {
	c := d.Local.Config()
	c.Name = "WebDAVMoveRegression"
	if d.name != "" {
		c.Name = d.name
	}
	c.NoOverwriteRename = d.flagged
	return c
}

func (d *moveDriver) Copy(ctx context.Context, src, dst model.Obj) error {
	d.copies.Add(1)
	return d.Local.Copy(ctx, src, dst)
}

// With claimDestination set, the first rename simulates another writer taking
// the destination after the handler's existence check, then refuses it.
func (d *moveDriver) Rename(ctx context.Context, obj model.Obj, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	target := filepath.Join(filepath.Dir(obj.GetPath()), name)
	if d.claimDestination.Load() && d.claimed.CompareAndSwap(false, true) {
		if err := os.WriteFile(target, []byte("other writer"), 0o600); err != nil {
			return err
		}
	}
	if _, err := os.Lstat(target); err == nil && d.flagged {
		return errors.New("destination already exists")
	}
	return d.Local.Rename(ctx, obj, name)
}

type moveFixture struct {
	root  string
	mount string
	src   string
	dst   string
	d     *moveDriver
	ctx   context.Context
	h     *Handler
}

func setupMoveFixture(t *testing.T, d *moveDriver) *moveFixture {
	t.Helper()
	root := t.TempDir()
	d.root = root
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	op.RegisterDriver(func() driver.Driver { return d })
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
	user := &model.User{Username: "mover", BasePath: "/", Role: model.GENERAL, Permission: 1<<4 | 1<<5}
	return &moveFixture{
		root: root, mount: mount,
		src: path.Join(mount, "original.mp4"),
		dst: path.Join(mount, "renamed.mp4"),
		d:   d,
		ctx: context.WithValue(context.Background(), conf.UserKey, user),
		h:   &Handler{Prefix: "/dav", LockSystem: NewMemLS()},
	}
}

func (f *moveFixture) write(t *testing.T, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(f.root, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.root, name), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func (f *moveFixture) assertContent(t *testing.T, name, want string) {
	t.Helper()
	if b, err := os.ReadFile(filepath.Join(f.root, name)); err != nil || string(b) != want {
		t.Fatalf("%s = %q, %v; want %q", name, b, err, want)
	}
}

func (f *moveFixture) move(src, dst string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("MOVE", "http://example.test/dav"+src, nil).WithContext(f.ctx)
	r.Header.Set("Destination", "http://example.test/dav"+dst)
	r.Header.Set("Overwrite", "F")
	w := httptest.NewRecorder()
	f.h.ServeHTTP(w, r)
	return w
}

// Only drivers that declare NoOverwriteRename may skip the source backup, and
// only for a same-directory rename of a file.
func TestMoveSourceBackupDependsOnDriverGuarantee(t *testing.T) {
	cases := []struct {
		name        string
		flagged     bool
		cross       bool
		wantBackups bool
	}{
		{"flagged driver renames in place", true, false, false},
		{"unflagged driver keeps the backup", false, false, true},
		{"flagged driver keeps the backup across directories", true, true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := setupMoveFixture(t, &moveDriver{flagged: tc.flagged})
			f.write(t, "original.mp4", "source")
			f.write(t, "sub/keep", "")
			src, dst, want := f.src, f.dst, "renamed.mp4"
			if tc.cross {
				dst, want = path.Join(f.mount, "sub", "renamed.mp4"), "sub/renamed.mp4"
			}
			if w := f.move(src, dst); w.Code != http.StatusCreated {
				t.Fatalf("MOVE status=%d body=%s", w.Code, w.Body.String())
			}
			f.assertContent(t, want, "source")
			if _, err := os.Stat(filepath.Join(f.root, "original.mp4")); !os.IsNotExist(err) {
				t.Fatalf("source still exists: %v", err)
			}
			if got := f.d.copies.Load() > 0; got != tc.wantBackups {
				t.Fatalf("copies=%d, want backups=%v", f.d.copies.Load(), tc.wantBackups)
			}
		})
	}
}

// A balanced mount hands each operation to the next storage, so the check and
// the rename can land on different ones. The backup may only be skipped when
// every storage behind the path declares the guarantee.
func TestRenameInPlaceNeedsEveryBalancedStorageToDeclare(t *testing.T) {
	f := setupMoveFixture(t, &moveDriver{flagged: true})
	plain := &moveDriver{name: "WebDAVMoveRegressionPlain"}
	op.RegisterDriver(func() driver.Driver { return plain })
	addition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": f.root})
	if err != nil {
		t.Fatal(err)
	}
	id, err := op.CreateStorage(context.Background(), model.Storage{
		Driver: "WebDAVMoveRegressionPlain", MountPath: f.mount + ".balance", Addition: addition,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(context.Background(), id); err != nil {
			t.Error(err)
		}
	})
	for i := 0; i < 4; i++ {
		if canRenameInPlace(f.src) {
			t.Fatalf("call %d: skipping the backup is unsafe while a storage behind the path does not declare the guarantee", i)
		}
	}
}

// A destination created by another writer after the existence check must
// survive a MOVE that then fails.
func TestSameDirectoryMoveKeepsRecreatedDestination(t *testing.T) {
	d := &moveDriver{flagged: true}
	d.claimDestination.Store(true)
	f := setupMoveFixture(t, d)
	f.write(t, "original.mp4", "source")
	if w := f.move(f.src, f.dst); w.Code != http.StatusInternalServerError {
		t.Fatalf("MOVE status=%d want=%d body=%s", w.Code, http.StatusInternalServerError, w.Body.String())
	}
	f.assertContent(t, "renamed.mp4", "other writer")
	f.assertContent(t, "original.mp4", "source")
}

// A failing dead-property transaction must undo the rename.
func TestSameDirectoryMoveRollsBackOnTransactionFailure(t *testing.T) {
	f := setupMoveFixture(t, &moveDriver{flagged: true})
	f.write(t, "original.mp4", "source")
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
	f.assertContent(t, "original.mp4", "source")
	if _, err := os.Stat(filepath.Join(f.root, "renamed.mp4")); !os.IsNotExist(err) {
		t.Fatalf("rollback left destination: %v", err)
	}
	var props []model.WebDAVProperty
	if err := db.GetDb().Find(&props).Error; err != nil || len(props) != 1 || props[0].Path != f.src {
		t.Fatalf("dead properties did not roll back: %+v, %v", props, err)
	}
	if f.d.copies.Load() != 0 {
		t.Fatalf("rollback copied file data: copies=%d", f.d.copies.Load())
	}
}

// A source name recreated while the transaction was running is not overwritten:
// the driver refuses the reverse rename and both objects stay.
func TestSameDirectoryMoveRollbackPreservesRecreatedSource(t *testing.T) {
	f := setupMoveFixture(t, &moveDriver{flagged: true})
	f.write(t, "original.mp4", "original content")
	m, err := moveWithRollback(f.ctx, f.src, f.dst, f.mount, f.mount, "original.mp4", "renamed.mp4", false, true)
	if err != nil {
		t.Fatal(err)
	}
	f.write(t, "original.mp4", "new content")
	if err := m.rollback(); err == nil {
		t.Fatal("rollback of a taken source name should fail")
	}
	f.assertContent(t, "original.mp4", "new content")
	f.assertContent(t, "renamed.mp4", "original content")
}
