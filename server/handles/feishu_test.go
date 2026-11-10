package handles

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/feishu"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func TestFeishuAuthorizeRequiresAdministrator(t *testing.T) {
	for _, user := range []*model.User{nil, {Role: model.GENERAL}, {Role: model.ADMIN, Disabled: true}} {
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest("POST", "/", strings.NewReader(`{"id":1}`))
		c.Request.Header.Set("Content-Type", "application/json")
		if user != nil {
			c.Set(conf.UserKey, user)
		}
		FeishuAuthorize(c)
		var response common.Resp[any]
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 403 {
			t.Fatalf("non-admin accepted: %s", w.Body.String())
		}
	}
}

func TestFeishuAuthorizationStartAndDeclinedCallback(t *testing.T) {
	previousURL := conf.URL
	conf.URL = &url.URL{Path: "/base"}
	t.Cleanup(func() { conf.URL = previousURL })
	addition := feishu.Addition{RootID: driver.RootID{RootFolderID: "folderToken"}, AuthMode: "user", AppID: "test-app", AppSecret: "test-secret", RedirectURI: "https://openlist.example/base/api/public/feishu/callback", DocumentFormat: "docx", ReadOnly: true}
	raw, _ := json.Marshal(addition)
	id, err := op.CreateStorage(context.Background(), model.Storage{Driver: "Feishu", MountPath: "/test-feishu-oauth", Addition: string(raw)})
	if err != nil || id == 0 {
		t.Fatalf("could not save storage pending authorization: id=%d err=%v", id, err)
	}
	t.Cleanup(func() { _ = op.DeleteStorageById(context.Background(), id) })
	before, err := db.GetStorageById(id)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]uint{"id": id})
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("POST", "https://openlist.example/api/admin/storage/feishu/authorize", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set(conf.UserKey, &model.User{ID: 42, Role: model.ADMIN})
	FeishuAuthorize(c)
	var response common.Resp[struct {
		URL string `json:"url"`
	}]
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil || response.Code != 200 {
		t.Fatalf("start failed: %s", w.Body.String())
	}
	authorize, err := url.Parse(response.Data.URL)
	if err != nil {
		t.Fatal(err)
	}
	query := authorize.Query()
	state := query.Get("state")
	if len(state) != 64 || query.Get("client_id") != "test-app" || !strings.Contains(query.Get("scope"), "offline_access") || query.Get("redirect_uri") != addition.RedirectURI {
		t.Fatalf("invalid authorization URL: %s", authorize)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].Secure || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode || cookies[0].Path != "/base/api/public/feishu/callback" {
		t.Fatalf("unsafe binding cookie: %v", cookies)
	}
	call := func(cookie *http.Cookie) *httptest.ResponseRecorder {
		result := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(result)
		ctx.Request = httptest.NewRequest("GET", addition.RedirectURI+"?state="+state+"&error=access_denied", nil)
		if cookie != nil {
			ctx.Request.AddCookie(cookie)
		}
		FeishuCallback(ctx)
		return result
	}
	if got := call(nil); got.Code != 400 || !strings.Contains(got.Body.String(), "binding") {
		t.Fatalf("missing cookie accepted: %s", got.Body.String())
	}
	if got := call(cookies[0]); got.Code != 400 || !strings.Contains(got.Body.String(), "declined") {
		t.Fatalf("declined callback failed: %s", got.Body.String())
	}
	if got := call(cookies[0]); got.Code != 400 || !strings.Contains(got.Body.String(), "expired") {
		t.Fatalf("callback state replay accepted: %s", got.Body.String())
	}
	stored, err := db.GetStorageById(id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Addition != before.Addition {
		t.Fatal("declined callback altered credentials")
	}
}

func TestFeishuCallbackRejectsExpiredState(t *testing.T) {
	state := strings.Repeat("e", 64)
	feishuAuthorizations.Lock()
	feishuAuthorizations.pending[state] = feishuAuthorization{binding: sha256.Sum256([]byte("binding")), expires: time.Now().Add(-time.Second)}
	feishuAuthorizations.Unlock()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest("GET", "/api/public/feishu/callback?state="+state+"&code=secret-code", nil)
	FeishuCallback(c)
	if w.Code != 400 || strings.Contains(w.Body.String(), "secret-code") {
		t.Fatalf("unsafe expired callback response: %s", w.Body.String())
	}
}
