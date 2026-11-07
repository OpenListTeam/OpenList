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

func downAccessSetup(t *testing.T) {
	t.Helper()
	root := t.TempDir()
	for _, name := range []string{"get.txt", "head.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("hello world"), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	database, err := gorm.Open(sqlite.Open("file:down_access_record?mode=memory&cache=shared"), &gorm.Config{
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
}

// downCall invokes the /d handler exactly as the router does, only with the
// context values PathParse would have stored.
func downCall(t *testing.T, handler func(*gin.Context), method, filePath string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, "/d"+filePath, nil)
	ctx := context.WithValue(c.Request.Context(), conf.PathKey, filePath)
	c.Request = c.Request.WithContext(ctx)
	handler(c)
	return rec
}

func downAccessRows(t *testing.T) map[string]model.FileAccess {
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

// TestDownDoesNotRecordProbes pins the rule for the two download endpoints that
// the fork wired into the recorder. GET /d/*path and HEAD /d/*path share one
// handler (see server/router.go), and players, browsers and link checkers send
// HEAD first to ask for the size before deciding to download; recording that
// makes a file that was never transferred look hot, which is exactly the input
// the hot/cold tiering job acts on.
func TestDownDoesNotRecordProbes(t *testing.T) {
	downAccessSetup(t)

	// Positive control: a real GET must still be recorded.
	rec := downCall(t, Down, http.MethodGet, "/local/get.txt")
	if rec.Code != http.StatusOK || rec.Body.String() != "hello world" {
		t.Fatalf("GET /d/local/get.txt returned %d %q", rec.Code, rec.Body.String())
	}
	if row, ok := downAccessRows(t)["/local/get.txt"]; !ok || row.Hits != 1 {
		t.Fatalf("a real GET /d download was not recorded (rows: %v)", downAccessRows(t))
	}

	// HEAD serves no bytes: nothing was read.
	rec = downCall(t, Down, http.MethodHead, "/local/head.txt")
	if rec.Body.Len() != 0 {
		t.Fatalf("HEAD /d/local/head.txt returned a body of %d bytes", rec.Body.Len())
	}
	if row, ok := downAccessRows(t)["/local/head.txt"]; ok {
		t.Fatalf("HEAD /d/local/head.txt recorded %q as read (hits=%d); a probe is not a read",
			row.Path, row.Hits)
	}
}
