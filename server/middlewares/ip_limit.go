package middlewares

import (
	"net/http"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/gin-gonic/gin"
)

var (
	proxyIPCounts   = make(map[string]int)
	proxyIPCountsMu sync.Mutex
)

// ProxyIPConcurrencyLimit limits the maximum concurrent server proxy requests per IP Address
func ProxyIPConcurrencyLimit() gin.HandlerFunc {
	return func(c *gin.Context) {
		limit := setting.GetInt(conf.ProxyMaxConcurrentRequestsPerIP, -1)

		if limit < 0 {
			// -1 or less means unlimited
			c.Next()
			return
		}

		if limit == 0 {
			// 0 means completely disabled
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{
				"code":    http.StatusForbidden,
				"message": "Proxy is disabled",
				"data":    nil,
			})
			return
		}

		ip := c.ClientIP()

		proxyIPCountsMu.Lock()
		count := proxyIPCounts[ip]
		if count >= limit {
			proxyIPCountsMu.Unlock()
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    http.StatusTooManyRequests,
				"message": "Too Many Proxy Requests from this IP",
				"data":    nil,
			})
			return
		}
		proxyIPCounts[ip] = count + 1
		proxyIPCountsMu.Unlock()

		defer func() {
			proxyIPCountsMu.Lock()
			proxyIPCounts[ip]--
			if proxyIPCounts[ip] <= 0 {
				delete(proxyIPCounts, ip)
			}
			proxyIPCountsMu.Unlock()
		}()

		c.Next()
	}
}
