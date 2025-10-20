package middlewares

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func StoragesLoaded(c *gin.Context) {
	if !conf.StoragesLoaded {
		if utils.SliceContains([]string{"", "/", "/favicon.ico"}, c.Request.URL.Path) {
			c.Next()
			return
		}
		paths := []string{"/assets", "/images", "/streamer", "/static"}
		for _, path := range paths {
			if strings.HasPrefix(c.Request.URL.Path, path) {
				c.Next()
				return
			}
		}
		select {
		case <-conf.StoragesLoadSignal():
		case <-c.Request.Context().Done():
			c.Abort()
			return
		}
	}
	common.GinWithValue(c,
		conf.ApiUrlKey, common.GetApiUrlFromRequest(c.Request),
	)
	c.Next()
}
