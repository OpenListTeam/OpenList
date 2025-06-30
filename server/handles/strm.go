package handles

import (
	"context"
	"github.com/OpenListTeam/OpenList/internal/errs"
	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/internal/op"
	"github.com/OpenListTeam/OpenList/pkg/strm"
	"github.com/OpenListTeam/OpenList/pkg/utils"
	"github.com/OpenListTeam/OpenList/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"math/rand"
	"path"
	"time"
)

func GenerateStrm(c *gin.Context) {
	var req ListReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	req.Validate()
	user := c.MustGet("user").(*model.User)
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
	c.Set("meta", meta)

	if !common.CanAccess(user, meta, reqPath, req.Password) {
		common.ErrorStrResp(c, "password is incorrect or you have no permission", 403)
		return
	}
	if !user.CanWrite() && !common.CanWrite(meta, reqPath) && req.Refresh {
		common.ErrorStrResp(c, "Refresh without permission", 403)
		return
	}

	filePaths, err := ListAllFile(c, reqPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	resMap := strm.WriteFiles(filePaths)
	resMap["deletedFiles"] = strm.DeleteExtraFiles(reqPath, filePaths)
	common.SuccessResp(c, resMap)
}

func ListAllFile(ctx context.Context, parentDir string) ([]string, error) {
	utils.Log.Debugf("Listing files in directory: %s", parentDir)
	var filePaths []string

	objs, err := fs.List(ctx, parentDir, &fs.ListArgs{Refresh: true})
	if err != nil {
		utils.Log.Errorf("Failed to list files in directory: %s, error: %v", parentDir, err)
		return nil, err
	}

	for i := range objs {
		obj := objs[i]
		if obj.IsDir() {
			// 防止风控，随机等待
			source := rand.NewSource(time.Now().UnixNano())
			r := rand.New(source)
			randomDuration := time.Duration(r.Intn(400)+100) * time.Millisecond
			time.Sleep(randomDuration)
			subDirFiles, err := ListAllFile(ctx, path.Join(parentDir, obj.GetName()))
			if err != nil {
				return nil, err
			}
			filePaths = append(filePaths, subDirFiles...)
		} else {
			filePaths = append(filePaths, path.Join(parentDir, obj.GetName()))
		}
	}
	return filePaths, nil
}
