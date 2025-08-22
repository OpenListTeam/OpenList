package middlewares

import (
	"net/url"
	stdpath "path"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

// 文件操作相关中间件
// 把文件路径校验及鉴权统一放在这里处理，并将处理结果放到上下文里，避免后续重复处理
// // Middleware for file operations
// Centralizes file path validation and authentication here, and stores the results in the context
// to avoid redundant processing in subsequent steps

type permissionFunc func(user *model.User, meta *model.Meta, path string, password string) bool

func FsUp(c *gin.Context) {
	fs(c, false, func(user *model.User, meta *model.Meta, path string, password string) bool {
		return common.CanAccess(user, meta, path, password) && (user.CanWrite() || common.CanWrite(meta, stdpath.Dir(path)))
	})
}
func FsRename(c *gin.Context) {
	fs(c, true, func(user *model.User, meta *model.Meta, path string, password string) bool {
		return user.CanRename()
	})
}
func FsRemove(c *gin.Context) {
	fs(c, true, func(user *model.User, meta *model.Meta, path string, password string) bool {
		return user.CanRemove()
	})
}

func fs(c *gin.Context, withstorage bool, permission permissionFunc) {
	path := c.GetHeader("File-Path")
	password := c.GetHeader("Password")
	path, err := url.PathUnescape(path)
	if err != nil {
		common.ErrorResp(c, err, 400)
		c.Abort()
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	path, err = user.JoinPath(path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		c.Abort()
		return
	}
	meta, err := op.GetNearestMeta(stdpath.Dir(path))
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500, true)
			c.Abort()
			return
		}
	}

	if !permission(user, meta, path, password) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		c.Abort()
		return
	}

	if withstorage {
		storage, actualPath, err := op.GetStorageAndActualPath(path)
		if err != nil {
			common.ErrorResp(c, err, 400)
			c.Abort()
			return
		}
		if storage.Config().NoUpload {
			common.ErrorStrResp(c, "Current storage doesn't support upload", 403)
			c.Abort()
			return
		}

		common.GinWithValue(c, conf.StorageKey, storage)
		common.GinWithValue(c, conf.PathKey, actualPath) //这里的路径已经是网盘真实路径了
	}
	c.Next()
}
