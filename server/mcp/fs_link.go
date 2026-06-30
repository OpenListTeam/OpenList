package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type fsLinkArgs struct {
	Path     string `json:"path"`
	Password string `json:"password"`
	Type     string `json:"type"`
}

type fsLinkResp struct {
	Path          string      `json:"path"`
	Name          string      `json:"name"`
	Size          int64       `json:"size"`
	IsDir         bool        `json:"is_dir"`
	Modified      time.Time   `json:"modified"`
	Provider      string      `json:"provider"`
	URL           string      `json:"url"`
	URLType       string      `json:"url_type"`
	DirectURL     string      `json:"direct_url,omitempty"`
	ProxyURL      string      `json:"proxy_url,omitempty"`
	DownloadURL   string      `json:"download_url,omitempty"`
	Header        http.Header `json:"header,omitempty"`
	ContentLength int64       `json:"content_length,omitempty"`
	Concurrency   int         `json:"concurrency,omitempty"`
	PartSize      int         `json:"part_size,omitempty"`
}

func (s *Server) callFSLink(c *gin.Context, raw json.RawMessage) (any, *rpcError) {
	args, mcpErr := parseFSLinkArgs(raw)
	if mcpErr != nil {
		return nil, mcpErr
	}

	user, ok := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !ok || user == nil {
		return nil, &rpcError{Code: -32603, Message: "missing user context"}
	}
	if user.IsGuest() && user.Disabled {
		return nil, &rpcError{Code: -32001, Message: "guest user is disabled"}
	}

	reqPath, err := user.JoinPath(args.Path)
	if err != nil {
		return nil, &rpcError{Code: -32003, Message: err.Error()}
	}

	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if !common.CanAccess(user, meta, reqPath, args.Password) {
		return nil, &rpcError{Code: -32003, Message: "password is incorrect or you have no permission"}
	}

	ctx := context.WithValue(c.Request.Context(), conf.MetaKey, meta)
	obj, err := fs.Get(ctx, reqPath, &fs.GetArgs{
		WithStorageDetails: !user.IsGuest() && !setting.GetBool(conf.HideStorageDetails),
	})
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	if obj.IsDir() {
		return nil, &rpcError{Code: -32003, Message: "path is a directory"}
	}

	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}

	linkInfo, err := buildFSLinkInfo(ctx, c, reqPath, args, obj, meta, storage)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}
	return linkInfo, nil
}

func parseFSLinkArgs(raw json.RawMessage) (*fsLinkArgs, *rpcError) {
	args := &fsLinkArgs{}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, &rpcError{Code: -32602, Message: "invalid openlist.fs.link arguments"}
	}
	if err := json.Unmarshal(raw, args); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid openlist.fs.link arguments"}
	}
	if args.Path == "" {
		return nil, &rpcError{Code: -32602, Message: "path is required"}
	}
	return args, nil
}

func buildFSLinkInfo(ctx context.Context, c *gin.Context, reqPath string, args *fsLinkArgs, obj model.Obj, meta *model.Meta, storage driver.Driver) (*fsLinkResp, error) {
	provider, ok := model.GetProvider(obj)
	if !ok {
		provider = storage.Config().Name
	}

	resp := &fsLinkResp{
		Path:        reqPath,
		Name:        obj.GetName(),
		Size:        obj.GetSize(),
		IsDir:       obj.IsDir(),
		Modified:    obj.ModTime(),
		Provider:    provider,
		DownloadURL: signedFileURL(ctx, "/d", reqPath, meta, args.Type),
	}

	if canProxyFile(storage, stdpath.Base(reqPath)) {
		proxyURL := proxyFileURL(ctx, reqPath, meta, storage.GetStorage(), args.Type)
		resp.ProxyURL = proxyURL
	}

	if common.ShouldProxy(storage, stdpath.Base(reqPath)) {
		resp.URL = resp.ProxyURL
		resp.URLType = "proxy"
		return resp, nil
	}

	link, _, err := fs.Link(ctx, reqPath, model.LinkArgs{
		IP:       c.ClientIP(),
		Header:   c.Request.Header,
		Type:     args.Type,
		Redirect: true,
	})
	if err != nil {
		return nil, err
	}
	defer link.Close()

	resp.DirectURL = link.URL
	resp.URL = link.URL
	resp.URLType = "direct"
	resp.Header = link.Header
	resp.ContentLength = link.ContentLength
	resp.Concurrency = link.Concurrency
	resp.PartSize = link.PartSize
	return resp, nil
}

func canProxyFile(storage driver.Driver, filename string) bool {
	if storage.Config().MustProxy() || storage.GetStorage().WebProxy {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.ProxyTypes], utils.Ext(filename)) {
		return true
	}
	if utils.SliceContains(conf.SlicesMap[conf.TextTypes], utils.Ext(filename)) {
		return true
	}
	return false
}

func proxyFileURL(ctx context.Context, reqPath string, meta *model.Meta, storage *model.Storage, linkType string) string {
	if url := common.GenerateDownProxyURL(storage, reqPath); url != "" {
		return url
	}
	return signedFileURL(ctx, "/p", reqPath, meta, linkType)
}

func signedFileURL(ctx context.Context, prefix, reqPath string, meta *model.Meta, linkType string) string {
	query := url.Values{}
	if isEncrypt(meta, reqPath) || setting.GetBool(conf.SignAll) {
		query.Set("sign", sign.Sign(reqPath))
	}
	if linkType != "" {
		query.Set("type", linkType)
	}
	rawQuery := ""
	if encoded := query.Encode(); encoded != "" {
		rawQuery = "?" + encoded
	}
	return fmt.Sprintf("%s%s%s%s",
		common.GetApiUrl(ctx),
		prefix,
		utils.EncodePath(reqPath, true),
		rawQuery,
	)
}
