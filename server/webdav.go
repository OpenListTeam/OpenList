package server

import (
	"crypto/subtle"
	"net/http"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/OpenListTeam/OpenList/v4/server/webdav"
	"github.com/gin-gonic/gin"
	log "github.com/sirupsen/logrus"
)

var handler *webdav.Handler

func WebDav(dav *gin.RouterGroup) {
	handler = &webdav.Handler{
		Prefix:     path.Join(conf.URL.Path, "/dav"),
		LockSystem: webdav.NewMemLS(),
		Logger: func(request *http.Request, err error) {
			log.Errorf("%s %s %+v", request.Method, request.URL.Path, err)
		},
	}
	dav.Use(WebDAVAuth)
	uploadLimiter := middlewares.UploadRateLimiter(stream.ClientUploadLimit)
	downloadLimiter := middlewares.DownloadRateLimiter(stream.ClientDownloadLimit)
	dav.Any("/*path", uploadLimiter, downloadLimiter, ServeWebDAV)
	dav.Any("", uploadLimiter, downloadLimiter, ServeWebDAV)
	dav.Handle("PROPFIND", "/*path", ServeWebDAV)
	dav.Handle("PROPFIND", "", ServeWebDAV)
	dav.Handle("MKCOL", "/*path", ServeWebDAV)
	dav.Handle("LOCK", "/*path", ServeWebDAV)
	dav.Handle("UNLOCK", "/*path", ServeWebDAV)
	dav.Handle("PROPPATCH", "/*path", ServeWebDAV)
	dav.Handle("COPY", "/*path", ServeWebDAV)
	dav.Handle("MOVE", "/*path", ServeWebDAV)
}

func ServeWebDAV(c *gin.Context) {
	handler.ServeHTTP(c.Writer, c.Request)
}

func WebDAVAuth(c *gin.Context) {
	// check count of login
	ip := c.ClientIP()
	guest, _ := op.GetGuest()
	count, cok := model.LoginCache.Get(ip)
	if cok && count >= model.DefaultMaxAuthRetries {
		if c.Request.Method == "OPTIONS" {
			common.GinWithValue(c, conf.UserKey, guest)
			c.Next()
			return
		}
		c.Status(http.StatusTooManyRequests)
		c.Abort()
		model.LoginCache.Expire(ip, model.DefaultLockDuration)
		return
	}
	username, password, ok := c.Request.BasicAuth()
	if !ok {
		bt := c.GetHeader("Authorization")
		log.Debugf("[webdav auth] token: %s", bt)
		if strings.HasPrefix(bt, "Bearer") {
			bt = strings.TrimPrefix(bt, "Bearer ")
			token := setting.GetStr(conf.Token)
			if token != "" && subtle.ConstantTimeCompare([]byte(bt), []byte(token)) == 1 {
				admin, err := op.GetAdmin()
				if err != nil {
					log.Errorf("[webdav auth] failed get admin user: %+v", err)
					c.Status(http.StatusInternalServerError)
					c.Abort()
					return
				}
				common.GinWithValue(c, conf.UserKey, admin)
				c.Next()
				return
			}
		}
		if c.Request.Method == "OPTIONS" {
			common.GinWithValue(c, conf.UserKey, guest)
			c.Next()
			return
		}
		c.Writer.Header()["WWW-Authenticate"] = []string{`Basic realm="openlist"`}
		c.Status(http.StatusUnauthorized)
		c.Abort()
		return
	}
	user, ok := tryLogin(username, password)
	if !ok {
		if c.Request.Method == "OPTIONS" {
			common.GinWithValue(c, conf.UserKey, guest)
			c.Next()
			return
		}
		model.LoginCache.Set(ip, count+1)
		c.Status(http.StatusUnauthorized)
		c.Abort()
		return
	}
	// at least auth is successful till here
	model.LoginCache.Del(ip)
	if user.Disabled || !user.CanWebdavRead() {
		if c.Request.Method == "OPTIONS" {
			common.GinWithValue(c, conf.UserKey, guest)
			c.Next()
			return
		}
		c.Status(http.StatusForbidden)
		c.Abort()
		return
	}
	if (c.Request.Method == "PUT" || c.Request.Method == "MKCOL") && (!user.CanWebdavManage() || !user.CanWrite()) {
		c.Status(http.StatusForbidden)
		c.Abort()
		return
	}
	if c.Request.Method == "MOVE" && (!user.CanWebdavManage() || (!user.CanMove() && !user.CanRename())) {
		c.Status(http.StatusForbidden)
		c.Abort()
		return
	}
	if c.Request.Method == "COPY" && (!user.CanWebdavManage() || !user.CanCopy()) {
		c.Status(http.StatusForbidden)
		c.Abort()
		return
	}
	if c.Request.Method == "DELETE" && (!user.CanWebdavManage() || !user.CanRemove()) {
		c.Status(http.StatusForbidden)
		c.Abort()
		return
	}
	if c.Request.Method == "PROPPATCH" && !user.CanWebdavManage() {
		c.Status(http.StatusForbidden)
		c.Abort()
		return
	}
	common.GinWithValue(c, conf.UserKey, user)
	c.Next()
}

func tryLogin(username, password string) (*model.User, bool) {
	user, err := op.GetUserByName(username)
	if err == nil {
		err = user.ValidateRawPassword(password)
		if err != nil && setting.GetBool(conf.LdapLoginEnabled) && user.AllowLdap {
			err = common.HandleLdapLogin(username, password)
		}
	} else if setting.GetBool(conf.LdapLoginEnabled) && model.CanWebdavRead(int32(setting.GetInt(conf.LdapDefaultPermission, 0))) {
		user, err = tryLdapLoginAndRegister(username, password)
	}
	return user, err == nil
}
