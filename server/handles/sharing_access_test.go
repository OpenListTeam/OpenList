package handles

import (
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

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/access"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// sharingAccessSetup wires a real single-file share over a real Local storage.
func sharingAccessSetup(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"real.mkv": "0123456789", "head.mkv": "abcdef"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	database, err := gorm.Open(sqlite.Open("file:sharing_access_record?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatalf("database handle: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	t.Cleanup(func() { _ = sqlDB.Close() })
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
	for id, files := range map[string]string{
		"shr-1": `["/local/real.mkv"]`,
		"shr-2": `["/local/head.mkv"]`,
	} {
		if err := db.GetDb().Create(&model.SharingDB{
			ID:        id,
			CreatorId: creator.ID,
			FilesRaw:  files,
		}).Error; err != nil {
			t.Fatalf("create sharing %s: %v", id, err)
		}
	}
}

// sharingDown drives the public, unauthenticated share download handler.
func sharingDown(t *testing.T, method, sid, sharePath string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, "/sd/"+sid+sharePath, nil)
	ctx := context.WithValue(c.Request.Context(), conf.SharingIDKey, sid)
	ctx = context.WithValue(ctx, conf.PathKey, sharePath)
	c.Request = c.Request.WithContext(ctx)
	SharingDown(c)
	return rec
}

func sharingAccessRows(t *testing.T) map[string]model.FileAccess {
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

// TestSharingDownRecordsOnlyServedReads pins two rules for the public share
// download path: a row means the content was actually served, and a caller
// cannot mint rows for paths that are not objects. SharingDown is reachable
// without authentication, so a record placed before op.Link turns every 500 into
// a permanent table row chosen by the caller.
func TestSharingDownRecordsOnlyServedReads(t *testing.T) {
	sharingAccessSetup(t)

	// Positive control: the shared file served end to end must be recorded once.
	rec := sharingDown(t, http.MethodGet, "shr-1", "/")
	if rec.Code != http.StatusOK || rec.Body.String() != "0123456789" {
		t.Fatalf("download of the shared file returned %d %q", rec.Code, rec.Body.String())
	}
	rows := sharingAccessRows(t)
	if row, ok := rows["/local/real.mkv"]; !ok || row.Hits != 1 {
		t.Fatalf("a served share download was not recorded exactly once (rows: %v)", rows)
	}

	// Nothing is served for a path that does not exist: op.Link fails, the client
	// gets a 500, and no row may appear.
	for _, suffix := range []string{"/ghost-a.mkv", "/ghost-b.mkv", "/ghost-c.mkv"} {
		rec := sharingDown(t, http.MethodGet, "shr-1", suffix)
		if rec.Code == http.StatusOK {
			t.Fatalf("GET %s unexpectedly succeeded", suffix)
		}
	}
	rows = sharingAccessRows(t)
	if len(rows) != 1 {
		t.Fatalf("failed share downloads minted %d file_access row(s) from 3 requests that served nothing: %v",
			len(rows)-1, rows)
	}
	for suffix := range map[string]struct{}{"a": {}, "b": {}, "c": {}} {
		phantom := "/local/real.mkv/ghost-" + suffix + ".mkv"
		if row, ok := rows[phantom]; ok {
			t.Fatalf("a 500 response recorded the non-object path %q as read (hits=%d)", row.Path, row.Hits)
		}
	}

	// HEAD is routed to the same public handler (see server/router.go). It serves
	// no bytes, so it must not be recorded - but the real GET right after it must
	// be, otherwise the fix would have broken a working path.
	rec = sharingDown(t, http.MethodHead, "shr-2", "/")
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD /sd/shr-2/ returned a body of %d bytes", rec.Body.Len())
	}
	if row, ok := sharingAccessRows(t)["/local/head.mkv"]; ok {
		t.Fatalf("HEAD /sd/shr-2/ recorded %q as read (hits=%d); a probe is not a read", row.Path, row.Hits)
	}
	rec = sharingDown(t, http.MethodGet, "shr-2", "/")
	if rec.Code != http.StatusOK || rec.Body.String() != "abcdef" {
		t.Fatalf("GET /sd/shr-2/ returned %d %q", rec.Code, rec.Body.String())
	}
	if row, ok := sharingAccessRows(t)["/local/head.mkv"]; !ok || row.Hits != 1 {
		t.Fatalf("a served share GET after a HEAD was not recorded exactly once (rows: %v)", sharingAccessRows(t))
	}
}
