package middlewares

import (
	"crypto/subtle"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/wopi"
	"github.com/gin-gonic/gin"
)

// WopiSessionValidation validates the WOPI access_token from query parameter.
// It sets the WOPI session and user info in the context.
func WopiSessionValidation() gin.HandlerFunc {
	return func(c *gin.Context) {
		accessToken := c.Query(wopi.AccessTokenQuery)
		if accessToken == "" {
			c.Status(http.StatusForbidden)
			c.Header(wopi.ServerErrorHeader, "missing access_token")
			c.Abort()
			return
		}

		session, exists := wopi.GlobalSessionStore.GetSession(accessToken)
		if !exists {
			c.Status(http.StatusForbidden)
			c.Header(wopi.ServerErrorHeader, "invalid access token")
			c.Abort()
			return
		}

		// Constant-time comparison to prevent timing attacks
		if subtle.ConstantTimeCompare([]byte(accessToken), []byte(session.AccessToken)) != 1 {
			c.Status(http.StatusForbidden)
			c.Header(wopi.ServerErrorHeader, "invalid access token")
			c.Abort()
			return
		}

		// Set user in context for downstream handlers
		user, err := op.GetUserById(session.UserID)
		if err != nil || user == nil {
			c.Status(http.StatusInternalServerError)
			c.Header(wopi.ServerErrorHeader, "user not found")
			c.Abort()
			return
		}
		common.GinAppendValues(c, conf.UserKey, user)

		// Set WOPI session in context
		c.Set("wopi_session", session)

		c.Next()
	}
}

// WopiWriteAccess checks if the WOPI session has write access
func WopiWriteAccess() gin.HandlerFunc {
	return func(c *gin.Context) {
		sessionRaw, exists := c.Get("wopi_session")
		if !exists {
			c.Status(http.StatusForbidden)
			c.Header(wopi.ServerErrorHeader, "no wopi session")
			c.Abort()
			return
		}

		session := sessionRaw.(*wopi.SessionCache)
		if !session.CanEdit {
			c.Status(http.StatusNotFound)
			c.Header(wopi.ServerErrorHeader, "read-only access")
			c.Abort()
			return
		}

		c.Next()
	}
}
