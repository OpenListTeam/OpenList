package handles

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/go-cache"
	"github.com/gin-gonic/gin"
)

// 媒体协议（Emby / Subsonic）的认证入口不经过 /api/auth/login，
// 因此必须自行复用 LoginCache 的失败计数与锁定策略，否则等于附送一个无限次数的爆破接口。
// 使用独立的 key 前缀，避免 Subsonic 客户端的失败重试把 Web 登录一并锁死。

func mediaAuthKey(c *gin.Context) string {
	return "media:" + c.ClientIP()
}

// mediaAuthLocked 当前 IP 是否已达到失败上限（需要拒绝认证尝试）
// 注意：这里绝不能刷新过期时间。客户端（尤其是 Subsonic）会持续轮询重试，
// 每次被拒都续期会把 IP 永久锁死，连之后正确的密钥也无法登录。
func mediaAuthLocked(c *gin.Context) bool {
	count, ok := model.LoginCache.Get(mediaAuthKey(c))
	return ok && count >= model.DefaultMaxAuthRetries
}

// bumpMediaAuthCount 增加一次失败计数。
// 计数必须带 TTL：key 里包含 IP / 用户名（后者来自请求参数，可被随意伪造），
// 不带过期时间会让缓存随请求无限增长；固定窗口也让锁定到期后计数自动归零。
// 达到上限后请求会被 mediaAuthLocked / mediaAuthLockedFor 拦截，不会再走到这里，
// 因此锁定窗口不会被持续刷新。
func bumpMediaAuthCount(key string) {
	count, _ := model.LoginCache.Get(key)
	count++
	model.LoginCache.Set(key, count, cache.WithEx[int](model.DefaultLockDuration))
}

// mediaAuthFailed 记录一次认证失败（IP 维度）
func mediaAuthFailed(c *gin.Context) {
	bumpMediaAuthCount(mediaAuthKey(c))
}

// mediaAuthSucceeded 认证成功后清除失败计数
func mediaAuthSucceeded(c *gin.Context) {
	model.LoginCache.Del(mediaAuthKey(c))
}

// ==================== 用户名维度 ====================
// 只按 IP 计数是可以被绕过的：gin 默认信任所有代理（仓库里没有设置
// SetTrustedProxies），客户端自带 X-Forwarded-For 就能改写 c.ClientIP()，
// 轮换这个头即可无限次尝试。因此再按用户名记一份失败次数：它与 IP 无关，
// 即使 IP 被伪造，对同一账号的爆破依然会被锁住。
// （代理信任范围属于项目层面的问题，主干 /api/auth/login 现状相同。）

func mediaAuthUserKey(username string) string {
	return "media:user:" + strings.ToLower(strings.TrimSpace(username))
}

// mediaAuthLockedFor 本次请求是否应被限流：IP 或用户名任一维度达到上限即拒绝。
func mediaAuthLockedFor(c *gin.Context, username string) bool {
	if mediaAuthLocked(c) {
		return true
	}
	if strings.TrimSpace(username) == "" {
		return false
	}
	count, ok := model.LoginCache.Get(mediaAuthUserKey(username))
	return ok && count >= model.DefaultMaxAuthRetries
}

// bumpMediaAuthUser 给用户名维度记一次失败
func bumpMediaAuthUser(username string) {
	name := strings.TrimSpace(username)
	if name == "" {
		return
	}
	bumpMediaAuthCount(mediaAuthUserKey(name))
}

// mediaAuthFailedFor 记录一次认证失败：同时计入 IP 与用户名两个维度。
func mediaAuthFailedFor(c *gin.Context, username string) {
	mediaAuthFailed(c)
	bumpMediaAuthUser(username)
}

// mediaAuthSucceededFor 认证成功后清除 IP 与用户名两个维度的计数。
func mediaAuthSucceededFor(c *gin.Context, username string) {
	mediaAuthSucceeded(c)
	if name := strings.TrimSpace(username); name != "" {
		model.LoginCache.Del(mediaAuthUserKey(name))
	}
}
