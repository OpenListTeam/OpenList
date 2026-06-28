package server

import (
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/handles"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/gin-gonic/gin"
)

func Wopi(wopi *gin.RouterGroup) {
	// Routes that require user authentication
	auth := wopi.Group("", middlewares.Auth(false))
	{
		auth.POST("/create-session", handles.WopiCreateSession)
		auth.GET("/settings", handles.WopiGetSettings)
	}

	// WOPI file operations (uses access_token auth, no JWT needed)
	wopiFiles := wopi.Group("/files", middlewares.WopiSessionValidation())
	{
		wopiFiles.GET("/*path", func(c *gin.Context) {
			rawPath := utils.FixAndCleanPath(c.Param("path"))
			if rawPath == "/" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing path"})
				return
			}
			c.Set("wopi_path", rawPath)

			if strings.HasSuffix(rawPath, "/contents") {
				actualPath := strings.TrimSuffix(rawPath, "/contents")
				if actualPath == "" || actualPath == "/" {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
					return
				}
				c.Set("wopi_path", actualPath)
				handles.WopiGetFile(c)
			} else {
				handles.WopiCheckFileInfo(c)
			}
		})

		wopiFiles.POST("/*path", func(c *gin.Context) {
			rawPath := utils.FixAndCleanPath(c.Param("path"))
			if rawPath == "/" {
				c.JSON(http.StatusBadRequest, gin.H{"error": "missing path"})
				return
			}
			c.Set("wopi_path", rawPath)

			if strings.HasSuffix(rawPath, "/contents") {
				actualPath := strings.TrimSuffix(rawPath, "/contents")
				if actualPath == "" || actualPath == "/" {
					c.JSON(http.StatusBadRequest, gin.H{"error": "invalid path"})
					return
				}
				c.Set("wopi_path", actualPath)
				handles.WopiPutFile(c)
			} else {
				handles.WopiModifyFile(c)
			}
		})
	}
}
