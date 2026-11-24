package handles

import (
	"bytes"
	"context"
	"encoding/json"
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
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func setupBackslashTraversalTest(t *testing.T, root string, permission int32) *model.User {
	t.Helper()
	database, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	conf.Conf = conf.DefaultConfig(t.TempDir())
	db.Init(database)
	addition, err := utils.Json.MarshalToString(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = op.CreateStorage(context.Background(), model.Storage{
		Driver: "Local", MountPath: "/", Addition: addition,
	}); err != nil {
		t.Fatal(err)
	}
	return &model.User{
		Username: "restricted-user", BasePath: "/team/a", Role: model.GENERAL,
		Permission: permission,
	}
}

func prepareBackslashTraversalFs(t *testing.T) (root string, secretPath string) {
	t.Helper()
	root = t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "team", "a", "writable"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "team", "ab"), 0o700); err != nil {
		t.Fatal(err)
	}
	secretPath = filepath.Join(root, "team", "ab", "secret.txt")
	if err := os.WriteFile(secretPath, []byte("synthetic-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return root, secretPath
}

func invokeHandler(t *testing.T, user *model.User, payload any, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	req := httptest.NewRequest(http.MethodPost, "/api/fs/remove", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req = req.WithContext(context.WithValue(req.Context(), conf.UserKey, user))
	ctx.Request = req
	handler(ctx)
	return recorder
}

func TestFsRemoveRejectsBackslashTraversal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root, secretPath := prepareBackslashTraversalFs(t)
	user := setupBackslashTraversalTest(t, root, 1<<3|1<<7)

	for _, name := range []string{"../../ab/secret.txt", `..\..\ab\secret.txt`} {
		recorder := invokeHandler(t, user, map[string]any{"dir": "/writable", "names": []string{name}}, FsRemove)
		if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"code":403`) {
			t.Fatalf("payload %q: got status=%d body=%s, want 403", name, recorder.Code, recorder.Body.String())
		}
		if _, err := os.Stat(secretPath); err != nil {
			t.Fatalf("payload %q deleted sibling file: %v", name, err)
		}
	}
}

func TestFsMoveRejectsBackslashTraversal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root, secretPath := prepareBackslashTraversalFs(t)
	user := setupBackslashTraversalTest(t, root, 1<<3|1<<5)

	recorder := invokeHandler(t, user, map[string]any{
		"src_dir": "/writable", "dst_dir": "/", "names": []string{`..\..\ab\secret.txt`},
	}, FsMove)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"code":403`) {
		t.Fatalf("got status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatalf("backslash traversal moved sibling file: %v", err)
	}
}

func TestFsCopyRejectsBackslashTraversal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	root, secretPath := prepareBackslashTraversalFs(t)
	user := setupBackslashTraversalTest(t, root, 1<<3|1<<6)

	recorder := invokeHandler(t, user, map[string]any{
		"src_dir": "/writable", "dst_dir": "/", "names": []string{`..\..\ab\secret.txt`},
	}, FsCopy)
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"code":403`) {
		t.Fatalf("got status=%d body=%s, want 403", recorder.Code, recorder.Body.String())
	}
	if _, err := os.Stat(secretPath); err != nil {
		t.Fatalf("backslash traversal affected sibling file: %v", err)
	}
}
