package middlewares

import (
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

func SharingIdParse(c *gin.Context) {
	sid := c.Param("sid")
	common.GinAppendValues(c, conf.SharingIDKey, sid)
	c.Next()
}

func EmptyPathParse(c *gin.Context) {
	common.GinAppendValues(c, conf.PathKey, "/")
	c.Next()
}
