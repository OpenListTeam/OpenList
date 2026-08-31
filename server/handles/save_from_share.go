package handles

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

type SaveFromShareReq struct {
	Urls     []string `json:"urls"`
	Path     string   `json:"path"`
	Password string   `json:"password"`
}

func SaveFromShare(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)

	var req SaveFromShareReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if !user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, reqPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	if !common.CanWrite(user, meta, reqPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}

	storage, err := fs.GetStorage(reqPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if !op.SupportsSaveFromShare(storage) {
		common.ErrorStrResp(c, "unsupported storage driver for save from share", 400)
		return
	}

	saved := 0
	for _, url := range req.Urls {
		trimmedURL := strings.TrimSpace(url)
		if trimmedURL == "" {
			continue
		}
		if err := fs.SaveFromShare(c.Request.Context(), reqPath, trimmedURL, req.Password); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		saved++
	}
	if saved == 0 {
		common.ErrorStrResp(c, "no share links provided", 400)
		return
	}
	common.SuccessResp(c, gin.H{
		"saved": saved,
	})
}
