package handles

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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

// mediaAuthFailed 记录一次认证失败
func mediaAuthFailed(c *gin.Context) {
	key := mediaAuthKey(c)
	count, _ := model.LoginCache.Get(key)
	count++
	model.LoginCache.Set(key, count)
	if count >= model.DefaultMaxAuthRetries {
		// 达到上限时设定锁定窗口；锁定期间请求会被 mediaAuthLocked 直接拦截，
		// 不会再走到这里，因此过期时间不会被续期
		model.LoginCache.Expire(key, model.DefaultLockDuration)
	}
}

// mediaAuthSucceeded 认证成功后清除失败计数
func mediaAuthSucceeded(c *gin.Context) {
	model.LoginCache.Del(mediaAuthKey(c))
}
