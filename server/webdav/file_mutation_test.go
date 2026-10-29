package webdav

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestWebDAVMutationIntegration(t *testing.T) {
	previousConf, previousCache, previousDB := conf.Conf, op.Cache, db.GetDb()
	conf.Conf = conf.DefaultConfig(t.TempDir())
	op.Cache = op.NewCacheManager()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "webdav.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Init(database)
	root := t.TempDir()
	for _, directory := range []string{"source-dir/child", "destination"} {
		if err := os.MkdirAll(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range map[string]string{"source": "new", "destination/target": "old", "destination/rollback-target": "before", "destination/cancel-target": "before-cancel", "source-dir/child/file": "nested"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	addition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatal(err)
	}
	storageID, err := op.CreateStorage(context.Background(), model.Storage{Driver: "Local", MountPath: "/dav-mutation", Addition: addition})
	if err != nil {
		t.Fatal(err)
	}
	otherRoot := t.TempDir()
	otherAddition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": otherRoot})
	if err != nil {
		t.Fatal(err)
	}
	otherID, err := op.CreateStorage(context.Background(), model.Storage{Driver: "Local", MountPath: "/dav-other", Addition: otherAddition})
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var snapshots [][]string
	op.Cache.SetSetting(conf.HandleHookAfterWriting, &model.SettingItem{Value: "true"})
	op.Cache.SetSetting(conf.HandleHookRateLimit, &model.SettingItem{Value: "0"})
	op.SetSnapshotProjector(func(_ context.Context, _ string, objs []model.Obj) {
		var names []string
		for _, obj := range objs {
			names = append(names, obj.GetName())
		}
		mu.Lock()
		snapshots = append(snapshots, names)
		mu.Unlock()
	})
	op.StartSnapshotReconciliation()
	stop := func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()
		if err := op.StopSnapshotReconciliation(ctx); err != nil {
			t.Error(err)
		}
	}
	t.Cleanup(func() {
		stop()
		op.SetSnapshotProjector(nil)
		if err := op.DeleteStorageById(context.Background(), storageID); err != nil {
			t.Error(err)
		}
		if err := op.DeleteStorageById(context.Background(), otherID); err != nil {
			t.Error(err)
		}
		conf.Conf = previousConf
		if previousDB != nil {
			if previousSQL, err := previousDB.DB(); err == nil && previousSQL.Ping() == nil {
				db.Init(previousDB)
			}
		}
		if err := sqlDB.Close(); err != nil {
			t.Error(err)
		}
		op.Cache = previousCache
	})
	user := &model.User{ID: 1, BasePath: "/", Permission: 1<<3 | 1<<4 | 1<<5 | 1<<6}
	ctx := context.WithValue(context.Background(), conf.UserKey, user)
	assertContent := func(name, want string) {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(root, name))
		if err != nil || string(data) != want {
			t.Fatalf("%s = %q, %v; want %q", name, data, err, want)
		}
	}
	assertNoStaging := func() {
		t.Helper()
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				t.Error(err)
				return err
			}
			if strings.HasPrefix(entry.Name(), ".openlist-") {
				t.Errorf("staging remains: %s", path)
			}
			return nil
		})
	}

	t.Run("overwrite and named copy", func(t *testing.T) {
		if status, err := copyFiles(ctx, "/dav-mutation/source", "/dav-mutation/destination/target", false, -1); err != nil || status != http.StatusPreconditionFailed {
			t.Fatalf("overwrite=false: %d %v", status, err)
		}
		assertContent("destination/target", "old")
		property := model.WebDAVProperty{Path: "/dav-mutation/source", Namespace: "urn:test", Name: "color", InnerXML: []byte("blue")}
		if err := database.Create(&property).Error; err != nil {
			t.Fatal(err)
		}
		if status, err := copyFiles(ctx, "/dav-mutation/source", "/dav-mutation/destination/target", true, -1); err != nil || status != http.StatusNoContent {
			t.Fatalf("overwrite=true: %d %v", status, err)
		}
		assertContent("destination/target", "new")
		var copied model.WebDAVProperty
		if err := database.Where("path = ?", "/dav-mutation/destination/target").First(&copied).Error; err != nil || string(copied.InnerXML) != "blue" {
			t.Fatalf("dead property: %+v %v", copied, err)
		}
		assertNoStaging()
	})
	t.Run("depth zero and recursive directory", func(t *testing.T) {
		for _, tc := range []struct {
			name  string
			depth int
		}{{"shallow", 0}, {"deep", -1}} {
			if status, err := copyFiles(ctx, "/dav-mutation/source-dir", "/dav-mutation/"+tc.name, false, tc.depth); err != nil || status != http.StatusCreated {
				t.Fatalf("%s: %d %v", tc.name, status, err)
			}
			entries, err := os.ReadDir(filepath.Join(root, tc.name))
			if err != nil {
				t.Fatal(err)
			}
			if (tc.depth == 0 && len(entries) != 0) || (tc.depth != 0 && len(entries) != 1) {
				t.Fatalf("%s entries = %v", tc.name, entries)
			}
		}
		assertContent("deep/child/file", "nested")
		assertNoStaging()
	})
	t.Run("cross storage move completes before returning", func(t *testing.T) {
		if status, err := moveFiles(ctx, "/dav-mutation/deep", "/dav-other/renamed", false); err != nil || status != http.StatusCreated {
			t.Fatalf("cross-storage MOVE: %d %v", status, err)
		}
		data, err := os.ReadFile(filepath.Join(otherRoot, "renamed", "child", "file"))
		if err != nil || string(data) != "nested" {
			t.Fatalf("moved content = %q %v", data, err)
		}
		if _, err := os.Stat(filepath.Join(root, "deep")); !os.IsNotExist(err) {
			t.Fatalf("MOVE retained its source: %v", err)
		}
		assertNoStaging()
	})
	t.Run("database commit failure compensates files", func(t *testing.T) {
		for _, sql := range []string{"PRAGMA foreign_keys = ON", "CREATE TABLE parent_receipt(id INTEGER PRIMARY KEY)", "CREATE TABLE deferred_receipt(parent_id INTEGER REFERENCES parent_receipt(id) DEFERRABLE INITIALLY DEFERRED)"} {
			if err := database.Exec(sql).Error; err != nil {
				t.Fatal(err)
			}
		}
		err := runDeadPropsFileMutation(ctx, func(tx *gorm.DB) error { return tx.Exec("INSERT INTO deferred_receipt(parent_id) VALUES(999)").Error }, func(ctx context.Context) (*fileMutation, error) {
			return copyWithRollback(ctx, "/dav-mutation/source", "/dav-mutation/destination", "rollback-target", true, false)
		})
		if err == nil {
			t.Fatal("invalid deferred foreign key unexpectedly committed")
		}
		assertContent("destination/rollback-target", "before")
		var count int64
		if err := database.Table("deferred_receipt").Count(&count).Error; err != nil || count != 0 {
			t.Fatalf("failed transaction persisted: %d %v", count, err)
		}
		assertNoStaging()
	})
	t.Run("cancellation still compensates", func(t *testing.T) {
		canceled, cancel := context.WithCancel(ctx)
		defer cancel()
		err := runDeadPropsFileMutation(canceled, func(*gorm.DB) error { return nil }, func(ctx context.Context) (*fileMutation, error) {
			mutation, err := copyWithRollback(ctx, "/dav-mutation/source", "/dav-mutation/destination", "cancel-target", true, false)
			if err != nil {
				return mutation, err
			}
			cancel()
			return mutation, context.Canceled
		})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation = %v", err)
		}
		assertContent("destination/cancel-target", "before-cancel")
		assertNoStaging()
	})
	t.Run("compensation failure preserves both causes", func(t *testing.T) {
		primary, compensation := errors.New("installation failed"), errors.New("restore failed")
		err := runDeadPropsFileMutation(ctx, func(*gorm.DB) error { return nil }, func(context.Context) (*fileMutation, error) {
			return &fileMutation{rollback: func() error { return compensation }}, primary
		})
		if !errors.Is(err, primary) || !errors.Is(err, compensation) {
			t.Fatalf("lost error: %v", err)
		}
	})
	stop()
	mu.Lock()
	defer mu.Unlock()
	if len(snapshots) == 0 {
		t.Fatal("no final snapshots were reconciled")
	}
	for _, names := range snapshots {
		for _, name := range names {
			if strings.HasPrefix(name, ".openlist-") {
				t.Errorf("published staging name: %s", name)
			}
		}
	}
}
