package handles

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"

	// Local 驱动通过包初始化注册，管理员创建 sync 任务时需要真实存储。
	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/cronjob"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// adminTestStorageSeq 为每个测试生成不同的 Local 存储挂载路径。
var adminTestStorageSeq atomic.Uint32

// cronjobAdminTestMain 初始化 handler 层测试需要的数据库、驱动和 sync Handler。
// 只初始化必要部分，不启动真实 CronJob 调度器，避免测试期间出现定时触发。
func TestMain(m *testing.M) {
	conf.Conf = conf.DefaultConfig("data")
	database, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		panic(fmt.Sprintf("failed open cronjob handler test database: %+v", err))
	}
	db.Init(database)

	// 预先创建空任务数据，避免 tache 恢复函数输出“任务数据不存在”的错误日志。
	if err := db.CreateTaskData(&model.TaskItem{Key: "cron_sync", PersistData: "[]"}); err != nil {
		panic(fmt.Sprintf("failed create cron_sync task data: %+v", err))
	}

	// sync Handler 的创建校验依赖注册表；这里与启动流程保持一致。
	cronjob.RegisterSyncHandler()

	os.Exit(m.Run())
}

// registerAdminTestLocalStorage 注册一个真实 Local 存储。
// 创建 sync 任务时，CreateCronJob 会调用 handler.ValidateArgs，进而校验源/目标路径。
func registerAdminTestLocalStorage(t *testing.T, root string) string {
	t.Helper()

	seq := adminTestStorageSeq.Add(1)
	mountPath := fmt.Sprintf("/admin-cronjob-test-%d-%d", os.Getpid(), seq)
	addition, err := json.Marshal(map[string]string{"root_folder_path": root})
	if err != nil {
		t.Fatalf("failed marshal local addition: %+v", err)
	}
	storage := model.Storage{
		Driver:    "Local",
		MountPath: mountPath,
		Addition:  string(addition),
	}
	id, err := op.CreateStorage(context.Background(), storage)
	if err != nil {
		t.Fatalf("failed create local storage %s: %+v", mountPath, err)
	}
	t.Cleanup(func() {
		_ = op.DeleteStorageById(context.Background(), id)
	})
	return mountPath
}

// postCronjobRequest 模拟一次带用户身份的创建计划任务请求。
// 这里显式使用 middlewares.AuthAdmin，保证测试覆盖真实路由上的管理员权限拦截。
func postCronjobRequest(t *testing.T, user *model.User, payload map[string]any) (*httptest.ResponseRecorder, *gin.Engine) {
	t.Helper()

	gin.SetMode(gin.TestMode)
	router := gin.New()

	// 模拟 Auth 中间件已经解析出用户；AuthAdmin 是真实 admin 路由组使用的鉴权中间件。
	api := router.Group("/api", func(c *gin.Context) {
		c.Request = c.Request.WithContext(
			context.WithValue(c.Request.Context(), conf.UserKey, user),
		)
		c.Next()
	})
	admin := api.Group("/admin", middlewares.AuthAdmin)
	admin.POST("/cronjobs", CreateCronJob)

	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("failed marshal request: %+v", err)
	}
	request := httptest.NewRequest(http.MethodPost, "/api/admin/cronjobs", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	return recorder, router
}

// decodeCronjobResponse 解析 common.Resp 的统一返回结构。
// OpenList 的大多数业务错误也返回 HTTP 200，因此必须检查响应体里的业务 Code。
func decodeCronjobResponse(t *testing.T, recorder *httptest.ResponseRecorder) (int, string) {
	t.Helper()

	var resp struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
		Data    any    `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed decode response %q: %+v", recorder.Body.String(), err)
	}
	return resp.Code, resp.Message
}

// TestCreateCronJobRequiresAdmin 覆盖“仅管理员可以创建同步计划任务”的路由安全约束。
// 普通用户即使手工构造请求，也必须被 AuthAdmin 拦截，并且数据库不产生任务。
func TestCreateCronJobRequiresAdmin(t *testing.T) {
	normalUser := &model.User{
		Username: "cronjob-normal-user",
		Role:     model.GENERAL,
	}
	payload := map[string]any{
		"name":      "normal-user-sync",
		"type":      "sync",
		"cron_spec": "*/5 * * * *",
		"enabled":   false,
		"args": map[string]any{
			"src": "/non-admin-sync-src",
			"dst": "/non-admin-sync-dst",
		},
	}
	recorder, _ := postCronjobRequest(t, normalUser, payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected HTTP status: %d", recorder.Code)
	}
	code, message := decodeCronjobResponse(t, recorder)
	if code != 403 {
		t.Fatalf("expected business code 403, got %d, message %q", code, message)
	}

	jobs, err := db.GetCronJobs()
	if err != nil {
		t.Fatalf("failed list cronjobs after rejected request: %+v", err)
	}
	for _, job := range jobs {
		if job.Name == "normal-user-sync" {
			t.Fatal("non-admin request must not create a cronjob")
		}
	}
}

// TestCreateSyncCronJobWithAdmin 覆盖管理员实际创建 sync 计划任务。
// 使用两个 Local 存储保证参数校验里的源/目标路径可以被解析，并且不会互套。
func TestCreateSyncCronJobWithAdmin(t *testing.T) {
	// 创建两个独立的临时目录和存储，模拟真实管理员从本地目录 A 同步到本地目录 B。
	srcRoot := t.TempDir()
	dstRoot := t.TempDir()
	srcMount := registerAdminTestLocalStorage(t, srcRoot)
	dstMount := registerAdminTestLocalStorage(t, dstRoot)

	admin := &model.User{
		Username: "cronjob-admin-user",
		Role:     model.ADMIN,
	}
	payload := map[string]any{
		"name":      "admin-sync-cronjob",
		"type":      "sync",
		"cron_spec": "*/10 * * * *",
		"enabled":   false,
		"args": map[string]any{
			"src": srcMount,
			"dst": dstMount,
		},
	}
	recorder, _ := postCronjobRequest(t, admin, payload)
	if recorder.Code != http.StatusOK {
		t.Fatalf("unexpected HTTP status: %d", recorder.Code)
	}
	code, message := decodeCronjobResponse(t, recorder)
	if code != 200 {
		t.Fatalf("expected admin create success, got code %d, message %q", code, message)
	}

	created, err := db.GetCronJobs()
	if err != nil {
		t.Fatalf("failed list cronjobs after admin create: %+v", err)
	}
	found := false
	for _, job := range created {
		if job.Name == "admin-sync-cronjob" {
			found = true
			if job.Type != "sync" {
				t.Fatalf("unexpected job type: %s", job.Type)
			}
			if job.CronSpec != "*/10 * * * *" {
				t.Fatalf("unexpected cron spec: %s", job.CronSpec)
			}
		}
	}
	if !found {
		t.Fatal("admin-created sync cronjob was not persisted")
	}
}
