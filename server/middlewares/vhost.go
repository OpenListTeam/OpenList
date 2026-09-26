package middlewares

import (
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// vhostBlockedPrefixes 是虚拟主机域名下被阻止的路由前缀列表。
// 这些路由属于管理/内部功能，不应通过 vhost 域名暴露。
var vhostBlockedPrefixes = []string{
	"/api/admin/",
	"/dav/",
	"/s3/",
}

// VhostRouteGuard 虚拟主机路由守卫中间件。
// 当请求的 Host 头匹配到一个有效的虚拟主机 sharing 时，
// 阻止对管理类路由（/api/admin/、/dav/、/s3/）的访问，返回 404。
// 对于 WebHosting=true 的域名，额外阻止 /api/ 下除 /api/public/ 和 /api/fs/ 之外的路由。
func VhostRouteGuard(c *gin.Context) {
	rawHost := c.Request.Host
	domain := common.StripHostPort(rawHost)
	if domain == "" {
		c.Next()
		return
	}

	sharing, err := op.GetSharingByDomain(domain)
	if err != nil || sharing == nil {
		// 非 vhost 域名，放行
		c.Next()
		return
	}

	path := c.Request.URL.Path

	// 通用阻止：管理类路由
	for _, prefix := range vhostBlockedPrefixes {
		if strings.HasPrefix(path, prefix) {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
	}

	// WebHosting 模式下额外限制：仅允许 /api/public/、/api/fs/ 和非 /api/ 路由
	if sharing.WebHosting && strings.HasPrefix(path, "/api/") {
		if !strings.HasPrefix(path, "/api/public/") && !strings.HasPrefix(path, "/api/fs/") {
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
	}

	c.Next()
}
