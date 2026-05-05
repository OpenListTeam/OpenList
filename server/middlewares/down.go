package middlewares

import (
	"net"
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
	common.GinWithValue(c, conf.PathKey, rawPath)
	c.Next()
}

// applyDownVhostPathMapping 根据请求的 Host 头匹配虚拟主机规则，
// 将下载/预览路由的路径映射到虚拟主机配置的实际路径。
// 仅在虚拟主机启用且非 Web 托管模式时生效。
func applyDownVhostPathMapping(c *gin.Context, reqPath string) string {
	rawHost := c.Request.Host
	domain := stripDownHostPort(rawHost)
	if domain == "" {
		return reqPath
	}
	vhost, err := op.GetVirtualHostByDomain(domain)
	if err != nil || vhost == nil {
		return reqPath
	}
	if !vhost.Enabled || vhost.WebHosting {
		// 未启用，或者是 Web 托管模式（Web 托管不做路径重映射）
		return reqPath
	}
	// 路径重映射：将 reqPath 拼接到 vhost.Path 后面
	mapped := stdpath.Join(vhost.Path, reqPath)
	utils.Log.Debugf("[VirtualHost] down path remapping: domain=%q reqPath=%q -> mappedPath=%q", domain, reqPath, mapped)
	return mapped
}

// stripDownHostPort removes the port from a host string (supports IPv4, IPv6, and bracketed IPv6).
func stripDownHostPort(host string) string {
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		// No port present; return host as-is
		return host
	}
	return h
}

func Down(verifyFunc func(string, string) error) func(c *gin.Context) {
	return func(c *gin.Context) {
		rawPath := c.Request.Context().Value(conf.PathKey).(string)
		meta, err := op.GetNearestMeta(rawPath)
		if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorPage(c, err, 500, true)
			return
		}
		common.GinWithValue(c, conf.MetaKey, meta)
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
