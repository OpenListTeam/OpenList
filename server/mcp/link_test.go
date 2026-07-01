package mcp

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/gin-gonic/gin"
)

var settingCacheMu sync.Mutex

func TestParseFSLinkArgs(t *testing.T) {
	args, err := parseFSLinkArgs(json.RawMessage(`{"path":"/file.txt","password":"pw","type":"thumb"}`))
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if args.Path != "/file.txt" || args.Password != "pw" || args.Type != "thumb" {
		t.Fatalf("unexpected args: %+v", args)
	}
}

func TestParseFSLinkArgsRequiresPath(t *testing.T) {
	_, err := parseFSLinkArgs(json.RawMessage(`{"type":"thumb"}`))
	if err == nil || err.Code != -32602 {
		t.Fatalf("unexpected error: %+v", err)
	}
}

func TestCanProxyFile(t *testing.T) {
	storage := &fsLinkTestDriver{
		config: driver.Config{Name: "Test"},
		storage: model.Storage{
			MountPath: "/",
		},
	}
	if canProxyFile(storage, "file.bin") {
		t.Fatal("unexpected proxy support")
	}
	storage.config.OnlyProxy = true
	if !canProxyFile(storage, "file.bin") {
		t.Fatal("expected proxy support")
	}
}

func TestCanProxyFileIgnoresWebdavProxyURLPolicy(t *testing.T) {
	storage := &fsLinkTestDriver{
		config: driver.Config{Name: "Test"},
		storage: model.Storage{
			MountPath: "/",
			Proxy: model.Proxy{
				WebdavPolicy: "use_proxy_url",
			},
		},
	}

	if canProxyFile(storage, "file.bin") {
		t.Fatal("webdav proxy url policy should not enable MCP proxy support")
	}
}

func TestBuildFSLinkInfoUsesProxyWhenStorageRequiresProxy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	settingCacheMu.Lock()
	t.Cleanup(func() {
		op.Cache.ClearAll()
		settingCacheMu.Unlock()
	})
	op.Cache.SetSetting(conf.LinkExpiration, &model.SettingItem{Key: conf.LinkExpiration, Value: "0"})
	op.Cache.SetSetting(conf.Token, &model.SettingItem{Key: conf.Token, Value: "test-token"})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("POST", "http://example.com/mcp", nil)
	ctx := context.WithValue(c.Request.Context(), conf.ApiUrlKey, "http://openlist.test")
	storage := &fsLinkTestDriver{
		config: driver.Config{Name: "Test", OnlyProxy: true},
		storage: model.Storage{
			MountPath: "/",
			Proxy: model.Proxy{
				DownProxyURL:     "http://proxy.test",
				DisableProxySign: true,
			},
		},
	}
	obj := &model.ObjectURL{
		Object: model.Object{Name: "file.txt", Size: 12},
		Url:    model.Url{Url: "http://direct.test/file.txt"},
	}

	meta := &model.Meta{Path: "/file.txt", Password: "secret"}
	resp, err := buildFSLinkInfo(ctx, c, "/file.txt", &fsLinkArgs{Path: "/file.txt"}, obj, meta, storage)
	if err != nil {
		t.Fatalf("unexpected error: %+v", err)
	}
	if resp.URL != "http://proxy.test/file.txt" || resp.URLType != "proxy" {
		t.Fatalf("unexpected selected link: %+v", resp)
	}
	if resp.DirectURL != "" {
		t.Fatalf("direct link should not be resolved for proxy storage: %+v", resp)
	}
}

type fsLinkTestDriver struct {
	config  driver.Config
	storage model.Storage
}

func (d *fsLinkTestDriver) Config() driver.Config {
	return d.config
}

func (d *fsLinkTestDriver) GetStorage() *model.Storage {
	return &d.storage
}

func (d *fsLinkTestDriver) SetStorage(storage model.Storage) {
	d.storage = storage
}

func (d *fsLinkTestDriver) GetAddition() driver.Additional {
	return nil
}

func (d *fsLinkTestDriver) Init(context.Context) error {
	return nil
}

func (d *fsLinkTestDriver) Drop(context.Context) error {
	return nil
}

func (d *fsLinkTestDriver) List(context.Context, model.Obj, model.ListArgs) ([]model.Obj, error) {
	return nil, nil
}

func (d *fsLinkTestDriver) Link(context.Context, model.Obj, model.LinkArgs) (*model.Link, error) {
	return nil, nil
}
