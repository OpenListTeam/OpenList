package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/handles"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type fsGetArgs struct {
	Path     string `json:"path"`
	Password string `json:"password"`
}

func (s *Server) callFSGet(c *gin.Context, raw json.RawMessage) (any, *rpcError) {
	args, mcpErr := parseFSGetArgs(raw)
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

	rawURL, provider, err := buildFSGetRawURL(ctx, c, reqPath, obj, meta)
	if err != nil {
		return nil, &rpcError{Code: -32603, Message: err.Error()}
	}

	parentPath := stdpath.Dir(reqPath)
	var related []model.Obj
	sameLevelFiles, err := fs.List(ctx, parentPath, &fs.ListArgs{})
	if err == nil {
		related = filterRelatedObjs(sameLevelFiles, obj)
	}

	parentMeta, _ := op.GetNearestMeta(parentPath)
	thumb, _ := model.GetThumb(obj)
	mountDetails, _ := model.GetStorageDetails(obj)
	return handles.FsGetResp{
		ObjResp: handles.ObjResp{
			Name:         obj.GetName(),
			Size:         obj.GetSize(),
			IsDir:        obj.IsDir(),
			Modified:     obj.ModTime(),
			Created:      obj.CreateTime(),
			Sign:         common.Sign(obj, parentPath, isEncrypt(meta, reqPath)),
			Thumb:        thumb,
			Type:         utils.GetFileType(obj.GetName()),
			HashInfoStr:  obj.GetHash().String(),
			HashInfo:     obj.GetHash().Export(),
			MountDetails: mountDetails,
		},
		RawURL:   rawURL,
		Readme:   getReadme(meta, reqPath),
		Header:   getHeader(meta, reqPath),
		Provider: provider,
		Related:  toObjResp(related, parentPath, isEncrypt(parentMeta, parentPath)),
	}, nil
}

func parseFSGetArgs(raw json.RawMessage) (*fsGetArgs, *rpcError) {
	args := &fsGetArgs{}
	if len(raw) == 0 || string(raw) == "null" {
		return nil, &rpcError{Code: -32602, Message: "invalid openlist.fs.get arguments"}
	}

	if err := json.Unmarshal(raw, args); err != nil {
		return nil, &rpcError{Code: -32602, Message: "invalid openlist.fs.get arguments"}
	}
	if args.Path == "" {
		return nil, &rpcError{Code: -32602, Message: "path is required"}
	}
	return args, nil
}

func buildFSGetRawURL(ctx context.Context, c *gin.Context, reqPath string, obj model.Obj, meta *model.Meta) (string, string, error) {
	storage, storageErr := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	provider, ok := model.GetProvider(obj)
	if !ok && storageErr == nil {
		provider = storage.Config().Name
	}
	if obj.IsDir() {
		return "", provider, nil
	}
	if storageErr != nil {
		return "", provider, storageErr
	}

	if storage.Config().MustProxy() || storage.GetStorage().WebProxy {
		rawURL := common.GenerateDownProxyURL(storage.GetStorage(), reqPath)
		if rawURL != "" {
			return rawURL, provider, nil
		}
		query := ""
		if isEncrypt(meta, reqPath) || setting.GetBool(conf.SignAll) {
			query = "?sign=" + sign.Sign(reqPath)
		}
		return fmt.Sprintf("%s/p%s%s", common.GetApiUrl(ctx), utils.EncodePath(reqPath, true), query), provider, nil
	}

	if url, ok := model.GetUrl(obj); ok {
		return url, provider, nil
	}
	link, _, err := fs.Link(ctx, reqPath, model.LinkArgs{
		IP:       c.ClientIP(),
		Header:   c.Request.Header,
		Redirect: true,
	})
	if err != nil {
		return "", provider, err
	}
	defer link.Close()
	return link.URL, provider, nil
}

func filterRelatedObjs(objs []model.Obj, obj model.Obj) []model.Obj {
	related := make([]model.Obj, 0)
	nameWithoutExt := strings.TrimSuffix(obj.GetName(), stdpath.Ext(obj.GetName()))
	for _, current := range objs {
		if current.GetName() == obj.GetName() {
			continue
		}
		if strings.HasPrefix(current.GetName(), nameWithoutExt) {
			related = append(related, current)
		}
	}
	return related
}
