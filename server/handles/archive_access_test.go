package handles

import (
	"archive/zip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	// the real server registers every archive tool through internal/archive/all.
	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/access"
	_ "github.com/OpenListTeam/OpenList/v4/internal/archive/zip"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// archiveAccessSetup writes a real single-entry zip and mounts it as /local.
func archiveAccessSetup(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	// Two archives: the tracker throttles repeated writes of one path, so a test
	// that reused a path would not see its own read land.
	for _, name := range []string{"bundle.zip", "shared.zip"} {
		zf, err := os.Create(filepath.Join(root, name))
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		zw := zip.NewWriter(zf)
		w, err := zw.Create("inner.txt")
		if err != nil {
			t.Fatalf("zip entry: %v", err)
		}
		if _, err := w.Write([]byte("zipped content")); err != nil {
			t.Fatalf("zip write: %v", err)
		}
		if err := zw.Close(); err != nil {
			t.Fatalf("zip close: %v", err)
		}
		if err := zf.Close(); err != nil {
			t.Fatalf("file close: %v", err)
		}
	}
	database, err := gorm.Open(sqlite.Open("file:archive_access_record?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	useTestDatabase(t, database)
	if _, err := op.CreateStorage(context.Background(), model.Storage{
		Driver:    "Local",
		MountPath: "/local",
		Addition:  `{"root_folder_path":"` + root + `"}`,
	}); err != nil {
		t.Fatalf("create storage: %v", err)
	}
	creator := &model.User{Username: "creator", BasePath: "/", Role: model.ADMIN, Permission: 0x7FFFFFFF}
	if err := db.GetDb().Create(creator).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.GetDb().Create(&model.SharingDB{
		ID:        "shr-zip",
		CreatorId: creator.ID,
		FilesRaw:  `["/local/shared.zip"]`,
	}).Error; err != nil {
		t.Fatalf("create sharing: %v", err)
	}
	// /sad refuses to run unless archive previewing inside a share is enabled.
	if err := db.SaveSettingItem(&model.SettingItem{
		Key:   conf.ShareArchivePreview,
		Value: "true",
		Type:  conf.TypeBool,
		Group: model.GLOBAL,
	}); err != nil {
		t.Fatalf("enable %s: %v", conf.ShareArchivePreview, err)
	}
	op.SettingCacheUpdate()
	t.Cleanup(func() { op.SettingCacheUpdate() })
}

func archiveAccessRows(t *testing.T) map[string]model.FileAccess {
	t.Helper()
	access.Flush()
	var rows []model.FileAccess
	if err := db.GetDb().Model(&model.FileAccess{}).Find(&rows).Error; err != nil {
		t.Fatalf("read file_access: %v", err)
	}
	out := make(map[string]model.FileAccess, len(rows))
	for _, row := range rows {
		out[row.Path] = row
	}
	return out
}

// TestArchiveInternalExtractRecordsRealReads covers the third archive download
// endpoint, GET /ae/*path (ArchiveInternalExtract, registered next to /ad and /ap
// in server/router.go). It streams an entry straight out of the archive, but
// unlike ArchiveDown and ArchiveProxy it never told the recorder, so on every
// storage without a native archive reader - local, most object stores - the one
// endpoint that can actually serve archive entries leaves no trace: the archive
// looks cold and the tiering job archives a file that is being read every day.
func TestArchiveInternalExtractRecordsRealReads(t *testing.T) {
	archiveAccessSetup(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/ae/local/bundle.zip?inner=/inner.txt", nil)
	ctx := context.WithValue(c.Request.Context(), conf.PathKey, "/local/bundle.zip")
	c.Request = c.Request.WithContext(ctx)
	ArchiveInternalExtract(c)

	if rec.Code != http.StatusOK || rec.Body.String() != "zipped content" {
		t.Fatalf("GET /ae/local/bundle.zip?inner=/inner.txt returned %d %q", rec.Code, rec.Body.String())
	}
	if row, ok := archiveAccessRows(t)["/local/bundle.zip"]; !ok || row.Hits != 1 {
		t.Fatalf("an archive entry was served out of %q but the read was not recorded (rows: %v)",
			"/local/bundle.zip", archiveAccessRows(t))
	}
}

// TestSharingArchiveExtractRecordsRealReads covers GET /sad/:sid/*path, the share
// counterpart of /ae: it streams archive entries to an unauthenticated visitor and,
// like /ae, it never told the recorder. A shared archive that is being read every
// day therefore looks cold, and the tiering job archives a file whose reads are
// arriving from the share.
func TestSharingArchiveExtractRecordsRealReads(t *testing.T) {
	archiveAccessSetup(t)
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/sad/shr-zip/?inner=/inner.txt", nil)
	ctx := context.WithValue(c.Request.Context(), conf.SharingIDKey, "shr-zip")
	ctx = context.WithValue(ctx, conf.PathKey, "/")
	c.Request = c.Request.WithContext(ctx)
	SharingArchiveExtract(c)

	if rec.Code != http.StatusOK || rec.Body.String() != "zipped content" {
		t.Fatalf("GET /sad/shr-zip/?inner=/inner.txt returned %d %q", rec.Code, rec.Body.String())
	}
	if row, ok := archiveAccessRows(t)["/local/shared.zip"]; !ok || row.Hits != 1 {
		t.Fatalf("a shared archive entry was served out of %q but the read was not recorded (rows: %v)",
			"/local/shared.zip", archiveAccessRows(t))
	}
}
