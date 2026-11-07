package handles

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
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

// A storage can be configured with a down_proxy_url (and a WebDAV policy of
// "use_proxy_url"): the instance then answers a read with a redirect to that
// external proxy and never touches the storage itself. The content is served all
// the same, so that redirect is a read like any other - without a row, an
// instance that offloads downloads to a proxy reports its library as never read
// and the tiering job archives files that are played daily.

const proxyURLBase = "https://proxy.example.com/files"

func proxyURLSetup(t *testing.T, mount string, files map[string]string) {
	t.Helper()
	root := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	database, err := gorm.Open(sqlite.Open("file:proxy_url_access?mode=memory&cache=shared"), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	useTestDatabase(t, database)
	if _, err := op.CreateStorage(context.Background(), model.Storage{
		Driver:    "Local",
		MountPath: mount,
		Proxy: model.Proxy{
			WebProxy:     true,
			DownProxyURL: proxyURLBase,
		},
		Addition: `{"root_folder_path":"` + root + `"}`,
	}); err != nil {
		t.Fatalf("create storage: %v", err)
	}
}

func proxyURLRows(t *testing.T) map[string]model.FileAccess {
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

// TestProxyDoesNotRecordProbesAndDoesRecordRedirects pins both halves of the
// redirect branch of Proxy: the offload to the configured proxy is a read of the
// file (a row), and it is only a read when an object was behind the path, since
// /p/*path is client-chosen and reachable without a sign by default.
func TestProxyURLRedirectIsRecorded(t *testing.T) {
	proxyURLSetup(t, "/purl", map[string]string{"get.txt": "hello world", "head.txt": "probe"})

	rec := downCall(t, Proxy, http.MethodGet, "/purl/get.txt")
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /p/purl/get.txt returned %d, want the 302 the proxy offload sends", rec.Code)
	}
	if loc := rec.Header().Get("Location"); !strings.HasPrefix(loc, proxyURLBase) {
		t.Fatalf("GET /p/purl/get.txt redirected to %q, want the configured proxy", loc)
	}
	if row, ok := proxyURLRows(t)["/purl/get.txt"]; !ok || row.Hits != 1 {
		t.Fatalf("a read offloaded to the configured proxy was not recorded (rows: %v)", proxyURLRows(t))
	}

	// A path that is not an object gets no row: the redirect is minted from the
	// request path alone, and anyone can ask for any string.
	for _, ghost := range []string{"/purl/ghost-a.txt", "/purl/ghost-b.txt"} {
		downCall(t, Proxy, http.MethodGet, ghost)
	}
	for ghost, row := range proxyURLRows(t) {
		if strings.Contains(ghost, "ghost") {
			t.Fatalf("a proxy redirect for the non-object path %q recorded it as read (hits=%d)", row.Path, row.Hits)
		}
	}

	// HEAD asks for the size only: the proxy is still handed a redirect, but no
	// content was read by anybody.
	downCall(t, Proxy, http.MethodHead, "/purl/head.txt")
	if row, ok := proxyURLRows(t)["/purl/head.txt"]; ok {
		t.Fatalf("HEAD /p/purl/head.txt recorded %q as read (hits=%d); a probe is not a read", row.Path, row.Hits)
	}
}

// TestSharingDownProxyURLRedirectIsRecorded is the same rule on the public share
// path: the share is served by the offload proxy, so the shared file was read.
func TestSharingDownProxyURLRedirectIsRecorded(t *testing.T) {
	proxyURLSetup(t, "/pshare", map[string]string{"real.mkv": "0123456789"})
	creator := &model.User{Username: "creator", BasePath: "/", Role: model.ADMIN, Permission: 0x7FFFFFFF}
	if err := db.GetDb().Create(creator).Error; err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := db.GetDb().Create(&model.SharingDB{
		ID:        "shr-p",
		CreatorId: creator.ID,
		FilesRaw:  `["/pshare/real.mkv"]`,
	}).Error; err != nil {
		t.Fatalf("create sharing: %v", err)
	}

	gin.SetMode(gin.TestMode)
	share := func(method, sharePath string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(method, "/sd/shr-p"+sharePath, nil)
		ctx := context.WithValue(c.Request.Context(), conf.SharingIDKey, "shr-p")
		ctx = context.WithValue(ctx, conf.PathKey, sharePath)
		c.Request = c.Request.WithContext(ctx)
		SharingDown(c)
		return rec
	}

	rec := share(http.MethodGet, "/")
	if rec.Code != http.StatusFound {
		t.Fatalf("GET /sd/shr-p/ returned %d, want the 302 the proxy offload sends", rec.Code)
	}
	if row, ok := proxyURLRows(t)["/pshare/real.mkv"]; !ok || row.Hits != 1 {
		t.Fatalf("a share served through the configured proxy was not recorded (rows: %v)", proxyURLRows(t))
	}

	// A share path that is not an object must not become a row: the handler is
	// public, and the unwrapped path is derived from what the caller asked for.
	for _, ghost := range []string{"/ghost-a.mkv", "/ghost-b.mkv"} {
		share(http.MethodGet, ghost)
	}
	for p, row := range proxyURLRows(t) {
		if strings.Contains(p, "ghost") {
			t.Fatalf("a failed share download of the non-object path %q recorded it as read (hits=%d)", row.Path, row.Hits)
		}
	}
}
