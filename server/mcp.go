package server

import (
	"github.com/OpenListTeam/OpenList/v4/server/mcp"
	"github.com/gin-gonic/gin"
)

func MCP(g *gin.RouterGroup) {
	mcp.Register(g)
}
