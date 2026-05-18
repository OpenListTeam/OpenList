package server

import (
	"context"
	"encoding/json"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/handles"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type mcpFSListArgs struct {
	Path     string `json:"path"`
	Password string `json:"password"`
	Refresh  bool   `json:"refresh"`
	Page     int    `json:"page"`
	PerPage  int    `json:"per_page"`
}

type mcpToolCallEnvelope struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *mcpServer) callFSList(c *gin.Context, raw json.RawMessage) (any, *mcpError) {
	args, mcpErr := parseMCPFSListArgs(raw)
	if mcpErr != nil {
		return nil, mcpErr
	}

	user, ok := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !ok || user == nil {
		return nil, &mcpError{Code: -32603, Message: "missing user context"}
	}
	if user.IsGuest() && user.Disabled {
		return nil, &mcpError{Code: -32001, Message: "guest user is disabled"}
	}

	reqPath, err := user.JoinPath(args.Path)
	if err != nil {
		return nil, &mcpError{Code: -32003, Message: err.Error()}
	}

	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return nil, &mcpError{Code: -32603, Message: err.Error()}
	}
	if !common.CanAccess(user, meta, reqPath, args.Password) {
		return nil, &mcpError{Code: -32003, Message: "password is incorrect or you have no permission"}
	}

	write := common.CanWrite(user, meta, reqPath)
	writeContentBypass := common.CanWriteContentBypassUserPerms(meta, reqPath)
	canWriteContentAtPath := write && (user.CanWriteContent() || writeContentBypass)
	if args.Refresh && !canWriteContentAtPath {
		return nil, &mcpError{Code: -32003, Message: "refresh without permission"}
	}

	ctx := context.WithValue(c.Request.Context(), conf.MetaKey, meta)
	objs, err := fs.List(ctx, reqPath, &fs.ListArgs{
		Refresh:            args.Refresh,
		WithStorageDetails: !user.IsGuest() && !setting.GetBool(conf.HideStorageDetails),
	})
	if err != nil {
		return nil, &mcpError{Code: -32603, Message: err.Error()}
	}

	total, paged := paginateMCPObjs(objs, args.Page, args.PerPage)
	return handles.FsListResp{
		Content:            toMCPObjResp(paged, reqPath, isEncryptMCP(meta, reqPath)),
		Total:              int64(total),
		Write:              write,
		WriteContentBypass: writeContentBypass,
		Provider:           detectMCPProvider(reqPath, paged),
		Readme:             getMCPReadme(meta, reqPath),
		Header:             getMCPHeader(meta, reqPath),
	}, nil
}

func parseMCPFSListArgs(raw json.RawMessage) (*mcpFSListArgs, *mcpError) {
	args := &mcpFSListArgs{
		Page:    1,
		PerPage: model.MaxInt,
	}
	if len(raw) == 0 || string(raw) == "null" {
		return args, nil
	}

	if err := json.Unmarshal(raw, args); err == nil {
		normalizeMCPFSListArgs(args)
		return args, nil
	}

	var envelope mcpToolCallEnvelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return nil, &mcpError{Code: -32602, Message: "invalid openlist.fs.list arguments"}
	}
	if len(envelope.Arguments) > 0 {
		if err := json.Unmarshal(envelope.Arguments, args); err != nil {
			return nil, &mcpError{Code: -32602, Message: "invalid openlist.fs.list arguments"}
		}
	}
	normalizeMCPFSListArgs(args)
	return args, nil
}

func normalizeMCPFSListArgs(args *mcpFSListArgs) {
	pageReq := model.PageReq{
		Page:    args.Page,
		PerPage: args.PerPage,
	}
	pageReq.Validate()
	args.Page = pageReq.Page
	args.PerPage = pageReq.PerPage
}

func paginateMCPObjs(objs []model.Obj, page, perPage int) (int, []model.Obj) {
	total := len(objs)
	start := (page - 1) * perPage
	if start > total {
		return total, []model.Obj{}
	}
	end := start + perPage
	if end > total {
		end = total
	}
	return total, objs[start:end]
}

func toMCPObjResp(objs []model.Obj, parent string, encrypt bool) []handles.ObjResp {
	resp := make([]handles.ObjResp, 0, len(objs))
	for _, obj := range objs {
		thumb, _ := model.GetThumb(obj)
		mountDetails, _ := model.GetStorageDetails(obj)
		resp = append(resp, handles.ObjResp{
			Name:         obj.GetName(),
			Size:         obj.GetSize(),
			IsDir:        obj.IsDir(),
			Modified:     obj.ModTime(),
			Created:      obj.CreateTime(),
			Sign:         common.Sign(obj, parent, encrypt),
			Thumb:        thumb,
			Type:         utils.GetObjType(obj.GetName(), obj.IsDir()),
			HashInfoStr:  obj.GetHash().String(),
			HashInfo:     obj.GetHash().Export(),
			MountDetails: mountDetails,
		})
	}
	return resp
}

func detectMCPProvider(reqPath string, objs []model.Obj) string {
	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	if err == nil && storage != nil {
		return storage.Config().Name
	}
	for _, obj := range objs {
		if provider, ok := model.GetProvider(obj); ok && provider != "" {
			return provider
		}
	}
	return "unknown"
}

func getMCPReadme(meta *model.Meta, path string) string {
	if meta != nil && common.MetaCoversPath(meta.Path, path, meta.RSub) {
		return meta.Readme
	}
	return ""
}

func getMCPHeader(meta *model.Meta, path string) string {
	if meta != nil && common.MetaCoversPath(meta.Path, path, meta.HeaderSub) {
		return meta.Header
	}
	return ""
}

func isEncryptMCP(meta *model.Meta, path string) bool {
	if common.IsStorageSignEnabled(path) {
		return true
	}
	if meta == nil || meta.Password == "" {
		return false
	}
	if !common.MetaCoversPath(meta.Path, path, meta.PSub) {
		return false
	}
	return true
}
