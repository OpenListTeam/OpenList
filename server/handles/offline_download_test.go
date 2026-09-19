package handles

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func init() {
	dataDir, err := os.MkdirTemp("", "openlist-handles-*")
	if err != nil {
		panic(err)
	}
	conf.Conf = conf.DefaultConfig(dataDir)
	database, err := gorm.Open(sqlite.Open("file:handles?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		panic(err)
	}
	db.Init(database)
}

type settingsTool struct {
	name      string
	version   string
	initCalls int
}

func (t *settingsTool) Name() string                          { return t.name }
func (*settingsTool) Items() []model.SettingItem              { return nil }
func (t *settingsTool) Init() (string, error)                 { t.initCalls++; return t.version, nil }
func (*settingsTool) IsReady() bool                           { return true }
func (*settingsTool) AddURL(*tool.AddUrlArgs) (string, error) { return "", nil }
func (*settingsTool) Remove(*tool.DownloadTask) error         { return nil }
func (*settingsTool) Status(*tool.DownloadTask) (*tool.Status, error) {
	return &tool.Status{}, nil
}
func (*settingsTool) Run(*tool.DownloadTask) error { return nil }

func TestOfflineDownloadSettingsPreserveSuccessPayloads(t *testing.T) {
	tests := []struct {
		name       string
		toolName   string
		version    string
		body       string
		handler    gin.HandlerFunc
		wantData   string
		settingKey string
	}{
		{
			name:       "aria2 returns version",
			toolName:   "aria2",
			version:    "v-test",
			body:       `{"uri":"http://aria2","secret":"secret"}`,
			handler:    SetAria2,
			wantData:   "v-test",
			settingKey: conf.Aria2Uri,
		},
		{
			name:       "qBittorrent returns ok",
			toolName:   "qBittorrent",
			version:    "ignored",
			body:       `{"url":"http://qbit","seedtime":"1"}`,
			handler:    SetQbittorrent,
			wantData:   "ok",
			settingKey: conf.QbittorrentUrl,
		},
	}
	gin.SetMode(gin.TestMode)
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			fake := &settingsTool{name: test.toolName, version: test.version}
			previous, existed := tool.Tools[test.toolName]
			tool.Tools[test.toolName] = fake
			t.Cleanup(func() {
				if existed {
					tool.Tools[test.toolName] = previous
				} else {
					delete(tool.Tools, test.toolName)
				}
				_ = db.DeleteSettingItemByKey(test.settingKey)
				op.SettingCacheUpdate()
			})

			response := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(response)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(test.body))
			ctx.Request.Header.Set("Content-Type", "application/json")
			test.handler(ctx)

			var result common.Resp[string]
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Code != 200 || result.Data != test.wantData {
				t.Fatalf("response = %#v, want code 200 and data %q", result, test.wantData)
			}
			if fake.initCalls != 1 {
				t.Fatalf("Init calls = %d, want 1", fake.initCalls)
			}
		})
	}
}

func TestValidateOfflineDownloadStorageRejectsWrongNativeTool(t *testing.T) {
	root := t.TempDir()
	addition, err := json.Marshal(struct {
		RootFolderPath string `json:"root_folder_path"`
	}{RootFolderPath: root})
	if err != nil {
		t.Fatal(err)
	}
	mount := "/" + strings.ReplaceAll(t.Name(), "/", "_")
	storageID, err := op.CreateStorage(context.Background(), model.Storage{
		Driver:    "Local",
		MountPath: mount,
		Addition:  string(addition),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := op.DeleteStorageById(context.Background(), storageID); err != nil {
			t.Errorf("delete fixture storage: %v", err)
		}
	})

	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	if validateOfflineDownloadStorage(ctx, mount, "Thunder") {
		t.Fatal("Local storage unexpectedly accepted as Thunder")
	}
	var result common.Resp[any]
	if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	want := "unsupported storage driver for offline download, only Thunder is supported"
	if result.Code != 400 || result.Message != want {
		t.Fatalf("response = %#v, want code 400 and message %q", result, want)
	}
}
