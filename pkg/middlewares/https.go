package middlewares

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
)

func ForceHttps(httpPort, httpsPort int) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.TLS == nil {
			host := c.Request.Host
			host = strings.Replace(host, fmt.Sprintf(":%d", httpPort), fmt.Sprintf(":%d", httpsPort), 1)
			c.Redirect(302, "https://"+host+c.Request.RequestURI)
			c.Abort()
			return
		}
		c.Next()
	}
}
