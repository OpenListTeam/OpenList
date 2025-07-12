package server

import (
	"net/http"
	_ "net/http/pprof"
	"runtime"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/gin-gonic/gin"
)

func _pprof(g *gin.RouterGroup) {
	g.Any("/*name", gin.WrapH(http.DefaultServeMux))
}

func debug(g *gin.RouterGroup) {
	g.GET("/path/*path", middlewares.Down(sign.Verify), func(c *gin.Context) {
		rawPath := c.Request.Context().Value(conf.PathKey).(string)
		c.JSON(200, gin.H{
			"path": rawPath,
		})
	})
	g.GET("/hide_privacy", func(c *gin.Context) {
		common.ErrorStrResp(c, "This is ip: 1.1.1.1", 400)
	})
	g.GET("/gc", func(c *gin.Context) {
		runtime.GC()
		c.String(http.StatusOK, "ok")
	})
	_pprof(g.Group("/pprof"))
}
