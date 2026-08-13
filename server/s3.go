package server

import (
	"context"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/s3"
	"github.com/gin-gonic/gin"
)

func S3(g *gin.RouterGroup) {
	h, _ := s3.NewServer(context.Background())
	g.Any("/*path", s3EnabledGuard, func(c *gin.Context) {
		adjustedPath := strings.TrimPrefix(c.Request.URL.Path, path.Join(conf.URL.Path, "/s3"))
		c.Request.URL.Path = adjustedPath
		gin.WrapH(h)(c)
	})
}

func s3EnabledGuard(c *gin.Context) {
	if !conf.Conf.S3.Enable {
		common.ErrorStrResp(c, "S3 server is not enabled", 403)
		c.Abort()
		return
	}
	if conf.Conf.S3.Port != -1 {
		common.ErrorStrResp(c, "S3 server bound to single port", 403)
		c.Abort()
		return
	}
	c.Next()
}

func S3Server(g *gin.RouterGroup) {
	h, _ := s3.NewServer(context.Background())
	g.Any("/*path", gin.WrapH(h))
}
