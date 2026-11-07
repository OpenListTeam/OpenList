package webdav

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// A storage with the WebDAV policy "use_proxy_url" answers a GET with a redirect
// to that external proxy instead of streaming the bytes itself. The client still
// receives the file, so this is a content read on the same footing as the
// 302_redirect policy and the local proxy - and it used to be the one branch of
// handleGetHeadPost that recorded nothing, which makes a drive mounted through a
// proxy look permanently unused to the tiering job.
func webdavProxyURLSetup(t *testing.T) *Handler {
	t.Helper()
	root := t.TempDir()
	for name, content := range map[string]string{"get.txt": "hello world", "head.txt": "probe target"} {
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
		MountPath: "/purl",
		Proxy: model.Proxy{
			WebdavPolicy: "use_proxy_url",
			DownProxyURL: "https://proxy.example.com/files",
		},
		Addition: `{"root_folder_path":"` + root + `"}`,
	}); err != nil {
		t.Fatalf("create storage: %v", err)
	}
	return &Handler{Prefix: "/dav", LockSystem: NewMemLS()}
}

func TestWebdavAccessRecord_ProxyURLRedirectIsRecorded(t *testing.T) {
	h := webdavProxyURLSetup(t)

	rec := webdavDo(t, h, http.MethodGet, "/dav/purl/get.txt")
	if rec.Code != http.StatusFound {
		t.Fatalf("GET returned %d, want the 302 the proxy offload sends", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, "https://proxy.example.com/files") {
		t.Fatalf("GET redirected to %q, want the configured proxy", loc)
	}
	if row, ok := webdavAccessRows(t)["/purl/get.txt"]; !ok || row.Hits != 1 {
		t.Fatalf("a WebDAV GET offloaded to the configured proxy was not recorded (rows: %v)", webdavAccessRows(t))
	}

	// HEAD is still only a probe: it is what the driver sends while browsing.
	rec = webdavDo(t, h, http.MethodHead, "/dav/purl/head.txt")
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD returned a body of %d bytes", rec.Body.Len())
	}
	if row, ok := webdavAccessRows(t)["/purl/head.txt"]; ok {
		t.Fatalf("a WebDAV HEAD recorded %q as read (hits=%d); a probe is not a read", row.Path, row.Hits)
	}
}
