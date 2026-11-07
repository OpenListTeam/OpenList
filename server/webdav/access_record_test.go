package webdav

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

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

// webdavSetup wires a real WebDAV handler on top of a real Local storage: the
// point of these tests is to drive handleGetHeadPost itself, so nothing between
// fs.Get and the access record may be faked.
func webdavSetup(t *testing.T) *Handler {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{
		"get.txt":  "hello world",
		"head.txt": "probe target",
		"post.txt": "post target",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	database, err := gorm.Open(sqlite.Open("file:webdav_access_record?mode=memory&cache=shared"), &gorm.Config{
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
	return &Handler{Prefix: "/dav", LockSystem: NewMemLS()}
}

// webdavDo sends one request through the handler with the values the auth
// middleware would have put in the context.
func webdavDo(t *testing.T, h *Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, nil)
	user := &model.User{ID: 1, Username: "admin", BasePath: "/", Role: model.ADMIN, Permission: 0x7FFFFFFF}
	ctx := context.WithValue(req.Context(), conf.UserKey, user)
	ctx = context.WithValue(ctx, conf.MetaPassKey, "")
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// webdavAccessRows flushes the tracker and returns every recorded path.
func webdavAccessRows(t *testing.T) map[string]model.FileAccess {
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

// TestWebdavAccessRecord_OnlyContentReads pins the product rule the whole table
// exists for: a row means "this file's content was served". A WebDAV HEAD or
// POST is a probe - it is what davfs2, Windows Explorer, rclone and curl -I send
// while *browsing*, before anything is opened - so recording one turns any
// listing of a directory into evidence that its files are hot, and a tiering job
// fed with that table never archives anything.
func TestWebdavAccessRecord_OnlyContentReads(t *testing.T) {
	h := webdavSetup(t)

	// Positive control: a GET that really serves the bytes must be recorded.
	rec := webdavDo(t, h, http.MethodGet, "/dav/local/get.txt")
	if rec.Code != http.StatusOK || rec.Body.String() != "hello world" {
		t.Fatalf("GET returned %d %q, want the file content", rec.Code, rec.Body.String())
	}
	if row, ok := webdavAccessRows(t)["/local/get.txt"]; !ok || row.Hits != 1 {
		t.Fatalf("a real GET was not recorded (rows: %v)", webdavAccessRows(t))
	}

	// A HEAD serves no content: no bytes leave the instance, so nothing was read.
	rec = webdavDo(t, h, http.MethodHead, "/dav/local/head.txt")
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned a body of %d bytes", rec.Body.Len())
	}
	if row, ok := webdavAccessRows(t)["/local/head.txt"]; ok {
		t.Fatalf("a WebDAV HEAD recorded %q as read (hits=%d, last=%v); a probe is not a read",
			row.Path, row.Hits, row.LastAccess)
	}

	// POST on a file body-serves nothing either; x/net/webdav routes it through
	// the same handler, and it used to record too.
	webdavDo(t, h, http.MethodPost, "/dav/local/post.txt")
	if row, ok := webdavAccessRows(t)["/local/post.txt"]; ok {
		t.Fatalf("a WebDAV POST recorded %q as read (hits=%d)", row.Path, row.Hits)
	}
}
