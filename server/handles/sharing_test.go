package handles

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSharingLastVisitCanBrowseAndDownload(t *testing.T) {
	root := t.TempDir()
	database, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "sharing.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { connection.Close() })
	previousConfig := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = previousConfig })
	db.Init(database)

	const content = "shared file content"
	if err := os.WriteFile(filepath.Join(root, "file.txt"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	addition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatal(err)
	}
	mountPath := "/" + t.Name()
	storageID, err := op.CreateStorage(context.Background(), model.Storage{
		Driver: "Local", MountPath: mountPath, Addition: addition,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(context.Background(), storageID); err != nil {
			t.Error(err)
		}
	})
	user := &model.User{Username: t.Name(), BasePath: mountPath, Permission: 1 << 14}
	if err := op.CreateUser(user); err != nil {
		t.Fatal(err)
	}

	for _, limit := range []int{1, 3} {
		t.Run(fmt.Sprintf("limit=%d", limit), func(t *testing.T) {
			request := func(ip, target string, handler gin.HandlerFunc) *httptest.ResponseRecorder {
				t.Helper()
				recorder := httptest.NewRecorder()
				c, _ := gin.CreateTestContext(recorder)
				c.Request = httptest.NewRequest(http.MethodGet, target, nil)
				c.Request.RemoteAddr = ip + ":12345"
				handler(c)
				return recorder
			}
			checkCode := func(resp *httptest.ResponseRecorder, code int) {
				t.Helper()
				var result common.Resp[any]
				if err := utils.Json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
					t.Fatalf("invalid response: %v; body=%s", err, resp.Body.String())
				}
				if result.Code != code {
					t.Fatalf("code = %d, want %d; body=%s", result.Code, code, resp.Body.String())
				}
			}

			s := &model.Sharing{
				SharingDB: &model.SharingDB{MaxAccessed: limit, Pwd: "secret"},
				Files:     []string{mountPath},
				Creator:   user,
			}
			sid, err := op.CreateSharing(s)
			if err != nil {
				t.Fatal(err)
			}
			get := func(c *gin.Context) {
				SharingGet(c, &FsGetReq{Path: "/" + sid, Password: "secret"})
			}
			for visit := 1; visit <= limit; visit++ {
				checkCode(request(fmt.Sprintf("192.0.2.%d", visit), "/api/fs/get", get), 200)
			}
			ip := fmt.Sprintf("192.0.2.%d", limit)
			checkCode(request(ip, "/api/fs/get", get), 200)
			checkCode(request(ip, "/api/fs/list", func(c *gin.Context) {
				SharingList(c, &ListReq{
					Path: "/" + sid, Password: "secret", PageReq: model.PageReq{Page: 1, PerPage: 10},
				})
			}), 200)
			checkCode(request(ip, "/api/fs/get", func(c *gin.Context) {
				SharingGet(c, &FsGetReq{Path: "/" + sid, Password: "wrong"})
			}), 403)

			down := func(c *gin.Context) {
				common.GinAppendValues(c, conf.SharingIDKey, sid, conf.PathKey, "/file.txt")
				SharingDown(c)
			}
			url := "/sd/" + sid + "/file.txt?pwd=secret"
			response := request(ip, url, down)
			if response.Code != http.StatusOK || response.Body.String() != content {
				t.Fatalf("download failed: status=%d body=%s", response.Code, response.Body.String())
			}
			checkCode(request("192.0.2.100", "/api/fs/get", get), 500)
			response = request("192.0.2.100", url, down)
			if !strings.Contains(response.Body.String(), "the share has expired or is no longer valid") {
				t.Fatalf("new visitor was not rejected: %s", response.Body.String())
			}
			stored, err := db.GetSharingById(sid)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Accessed != limit {
				t.Fatalf("accessed = %d, want %d", stored.Accessed, limit)
			}
		})
	}
}
