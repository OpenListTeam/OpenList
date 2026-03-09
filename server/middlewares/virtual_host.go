package middlewares

import (
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// VirtualHost 虚拟主机中间件，根据请求的 Host 头匹配虚拟主机配置
func VirtualHost(c *gin.Context) {
	host := c.Request.Host
	// 去掉端口号
	domain := stripHostPort(host)
	if domain == "" {
		c.Next()
		return
	}

	vhost, err := op.GetVirtualHostByDomain(domain)
	if err != nil || !vhost.Enabled {
		// 未找到匹配的虚拟主机或未启用，继续正常处理
		c.Next()
		return
	}

	// 将虚拟主机信息存入请求上下文
	common.GinWithValue(c, conf.VirtualHostKey, vhost)
	c.Next()
}
