package middlewares

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// NOTE: Counts are per-process; in multi-instance deployments the effective limit is limit * N.
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
			common.ErrorPage(c, errors.New("Proxy is disabled"), http.StatusForbidden)
			return
		}

		ipHeader := setting.GetStr(conf.ProxyClientIPHeader)
		var ip string

		// Extract IP based on user configuration to prevent spoofing
		if ipHeader != "" {
			ip = c.Request.Header.Get(ipHeader)
			if idx := strings.Index(ip, ","); idx != -1 {
				ip = ip[:idx]
			}
			ip = strings.TrimSpace(ip)
		}

		// Fallback to strict remote address if missing or not configured
		if ip == "" {
			ip = c.Request.RemoteAddr
			if host, _, err := net.SplitHostPort(ip); err == nil {
				ip = host
			}
		}

		proxyIPCountsMu.Lock()
		count := proxyIPCounts[ip]
		if count >= limit {
			proxyIPCountsMu.Unlock()
			common.ErrorPage(c, errors.New("Too Many Proxy Requests from this IP"), http.StatusTooManyRequests)
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
