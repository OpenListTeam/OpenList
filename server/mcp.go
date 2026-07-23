package server

import (
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/mcp"
	"github.com/gin-gonic/gin"
)

func MCP(g *gin.RouterGroup) {
	if !conf.Conf.MCP.Enable {
		g.Any("/mcp", func(c *gin.Context) {
			common.ErrorStrResp(c, "MCP server is not enabled", 403)
		})
		return
	}
	mcp.Register(g)
}
