package handles

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/feishu"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

type feishuAuthorization struct {
	driver            *feishu.Feishu
	storageID, userID uint
	generation        string
	binding           [32]byte
	expires           time.Time
	callback          *url.URL
}

var feishuAuthorizations = struct {
	sync.Mutex
	pending map[string]feishuAuthorization
}{pending: make(map[string]feishuAuthorization)}

func randomFeishuValue() (string, error) {
	var raw [32]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("cannot create Feishu authorization state")
	}
	return hex.EncodeToString(raw[:]), nil
}
func feishuCookieName(state string) string { return "openlist_feishu_" + state }

// FeishuAuthorize is registered only inside the authenticated administrator group.
func FeishuAuthorize(c *gin.Context) {
	value, exists := c.Get(conf.UserKey)
	user, ok := value.(*model.User)
	if !exists || !ok || !user.IsAdmin() || user.Disabled {
		common.ErrorStrResp(c, "administrator authorization required", 403)
		return
	}
	var req struct {
		ID uint `json:"id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorStrResp(c, "a saved Feishu storage ID is required", 400)
		return
	}
	stored, err := db.GetStorageById(req.ID)
	if err != nil || stored.Driver != "Feishu" || stored.Disabled {
		common.ErrorStrResp(c, "an enabled Feishu storage is required", 400)
		return
	}
	driver, err := op.GetStorageByMountPath(stored.MountPath)
	if err != nil {
		common.ErrorStrResp(c, "Feishu storage is not loaded", 400)
		return
	}
	d, ok := driver.(*feishu.Feishu)
	if !ok {
		common.ErrorStrResp(c, "invalid Feishu storage", 400)
		return
	}
	cfg, generation, err := d.OAuthConfig()
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	callback, err := feishu.ValidateRedirectURI(cfg.RedirectURI)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	basePath := ""
	if conf.URL != nil {
		basePath = strings.TrimSuffix(conf.URL.Path, "/")
	}
	expectedPath := basePath + "/api/public/feishu/callback"
	if callback.Path != expectedPath {
		common.ErrorStrResp(c, "redirect_uri must end with "+expectedPath, 400)
		return
	}
	// The binding cookie must be issued by the same origin that receives the
	// callback. Do not infer an external origin from untrusted forwarded headers.
	if !strings.EqualFold(c.Request.Host, callback.Host) {
		common.ErrorStrResp(c, "start authorization through the same public host configured in redirect_uri", 400)
		return
	}
	state, err := randomFeishuValue()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	binding, err := randomFeishuValue()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	feishuAuthorizations.Lock()
	for key, pending := range feishuAuthorizations.pending {
		if time.Now().After(pending.expires) {
			delete(feishuAuthorizations.pending, key)
		}
	}
	if len(feishuAuthorizations.pending) >= 256 {
		feishuAuthorizations.Unlock()
		common.ErrorStrResp(c, "too many pending Feishu authorizations; retry later", 429)
		return
	}
	feishuAuthorizations.pending[state] = feishuAuthorization{driver: d, storageID: req.ID, userID: user.ID, generation: generation, binding: sha256.Sum256([]byte(binding)), expires: time.Now().Add(10 * time.Minute), callback: callback}
	feishuAuthorizations.Unlock()
	http.SetCookie(c.Writer, &http.Cookie{Name: feishuCookieName(state), Value: binding, Path: callback.Path, MaxAge: 600, HttpOnly: true, Secure: callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
	query := url.Values{"client_id": {cfg.AppID}, "redirect_uri": {callback.String()}, "response_type": {"code"}, "state": {state}, "scope": {"offline_access drive:drive"}}
	c.Header("Cache-Control", "no-store")
	common.SuccessResp(c, gin.H{"url": "https://accounts.feishu.cn/open-apis/authen/v1/authorize?" + query.Encode()})
}

func FeishuCallback(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("X-Content-Type-Options", "nosniff")
	state := c.Query("state")
	if len(state) != 64 {
		c.String(400, "Feishu authorization state is invalid or expired; start again.")
		return
	}
	feishuAuthorizations.Lock()
	pending, ok := feishuAuthorizations.pending[state]
	if ok && time.Now().After(pending.expires) {
		delete(feishuAuthorizations.pending, state)
		ok = false
	}
	if !ok {
		feishuAuthorizations.Unlock()
		c.String(400, "Feishu authorization state is invalid or expired; start again.")
		return
	}
	cookie, err := c.Cookie(feishuCookieName(state))
	hash := sha256.Sum256([]byte(cookie))
	if err != nil || subtle.ConstantTimeCompare(hash[:], pending.binding[:]) != 1 {
		feishuAuthorizations.Unlock()
		c.String(400, "Feishu authorization browser binding failed; start again in the original browser.")
		return
	}
	delete(feishuAuthorizations.pending, state)
	feishuAuthorizations.Unlock()
	http.SetCookie(c.Writer, &http.Cookie{Name: feishuCookieName(state), Path: pending.callback.Path, MaxAge: -1, HttpOnly: true, Secure: pending.callback.Scheme == "https", SameSite: http.SameSiteLaxMode})
	if c.Query("error") != "" || c.Query("code") == "" {
		c.String(400, "Feishu authorization was declined or no authorization code was returned.")
		return
	}
	user, err := op.GetUserById(pending.userID)
	if err != nil || user.Disabled || !user.IsAdmin() {
		c.String(403, "The initiating administrator is no longer authorized.")
		return
	}
	stored, err := db.GetStorageById(pending.storageID)
	if err != nil || stored.Disabled || stored.Driver != "Feishu" {
		c.String(400, "Feishu storage is unavailable; start again.")
		return
	}
	current, err := op.GetStorageByMountPath(stored.MountPath)
	if err != nil || current != pending.driver {
		c.String(400, "Feishu storage changed; start authorization again.")
		return
	}
	if err := pending.driver.CompleteAuthorization(c.Request.Context(), pending.generation, c.Query("code")); err != nil {
		c.String(400, "%s", err.Error())
		return
	}
	// Reload through the normal storage lifecycle to clear object/link caches and
	// validate the selected root with the newly authorized identity.
	stored, err = db.GetStorageById(pending.storageID)
	if err != nil {
		c.String(500, "Authorization saved, but storage reload failed; reload it in administration.")
		return
	}
	if err := op.UpdateStorage(c.Request.Context(), *stored); err != nil {
		c.String(400, "Authorization saved, but root folder access failed; check scopes and folder permissions in administration.")
		return
	}
	c.String(200, "飞书授权成功，存储已连接。请关闭此窗口并刷新存储列表。\nFeishu authorization succeeded. Close this window and refresh the storage list.")
}
