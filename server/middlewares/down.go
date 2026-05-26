package middlewares

import (
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

func PathParse(c *gin.Context) {
	rawPath := parsePath(c.Param("path"))
	// 虚拟主机路径重映射：根据 Host 头匹配虚拟主机规则，将请求路径映射到实际路径
	// 例如：vhost.Path="/123pan/Downloads"，rawPath="/tests.html" -> "/123pan/Downloads/tests.html"
	rawPath = applyDownVhostPathMapping(c, rawPath)
	common.GinAppendValues(c, conf.PathKey, rawPath)
	c.Next()
}

// applyDownVhostPathMapping 根据请求的 Host 头匹配 sharing 中带 Domain 的虚拟主机记录，
// 将下载/预览路由的路径映射到虚拟主机配置的实际路径（取 sharing.Files[0]）。
// 仅在 sharing 有效（未禁用、未过期、Files 非空）且非 Web 托管模式时生效。
func applyDownVhostPathMapping(c *gin.Context, reqPath string) string {
	rawHost := c.Request.Host
	domain := stripDownHostPort(rawHost)
	if domain == "" {
		return reqPath
	}
	sharing, err := op.GetSharingByDomain(domain)
	if err != nil || sharing == nil {
		return reqPath
	}
	if sharing.WebHosting {
		// Web 托管模式不做下载路径重映射
		return reqPath
	}
	if len(sharing.Files) == 0 {
		return reqPath
	}
	root := sharing.Files[0]
	// 路径重映射：将 reqPath 拼接到 root 后面，并校验不逃逸出 root
	mapped := stdpath.Join(root, reqPath)
	if !strings.HasPrefix(mapped, strings.TrimRight(root, "/")+"/") && mapped != root {
		utils.Log.Warnf("[VirtualHost] path traversal rejected for down: domain=%q reqPath=%q", domain, reqPath)
		return reqPath
	}
	utils.Log.Debugf("[VirtualHost] down path remapping: domain=%q reqPath=%q -> mappedPath=%q", domain, reqPath, mapped)
	return mapped
}

// stripDownHostPort removes the port from a host string.
func stripDownHostPort(host string) string {
	return common.StripHostPort(host)
}

func Down(verifyFunc func(string, string) error) func(c *gin.Context) {
	return func(c *gin.Context) {
		rawPath := c.Request.Context().Value(conf.PathKey).(string)
		meta, err := op.GetNearestMeta(rawPath)
		if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorPage(c, err, 500, true)
			return
		}
		common.GinAppendValues(c, conf.MetaKey, meta)
		// verify sign
		if needSign(meta, rawPath) {
			s := c.Query("sign")
			err = verifyFunc(rawPath, strings.TrimSuffix(s, "/"))
			if err != nil {
				common.ErrorPage(c, err, 401)
				c.Abort()
				return
			}
		}
		c.Next()
	}
}

// TODO: implement
// path maybe contains # ? etc.
func parsePath(path string) string {
	return utils.FixAndCleanPath(path)
}

func needSign(meta *model.Meta, path string) bool {
	if setting.GetBool(conf.SignAll) {
		return true
	}
	if common.IsStorageSignEnabled(path) {
		return true
	}
	if meta == nil || meta.Password == "" {
		return false
	}
	if !meta.PSub && path != meta.Path {
		return false
	}
	return true
}
