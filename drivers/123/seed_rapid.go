package _123

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// RapidHashAlgos 返回 123 云盘支持的秒传哈希算法（MD5）
func (d *Pan123) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.MD5}
}

// RapidHashNeedsPieces 123 云盘不需要分片哈希
func (d *Pan123) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 MD5（Etag）哈希尝试秒传。
//
// 123 的秒传即「上传请求返回 reuse=true」，无需真正传输内容。
func (d *Pan123) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	etag := req.Whole.GetHash(utils.MD5)
	if len(etag) < utils.MD5.Width {
		return nil, errs.ErrUnavailableHash
	}

	duplicate := 0
	if overwrite {
		duplicate = 2
	}
	data := base.Json{
		"driveId":      0,
		"duplicate":    duplicate,
		"etag":         strings.ToLower(etag),
		"fileName":     req.Name,
		"parentFileId": dstDir.GetID(),
		"size":         req.Size,
		"type":         0,
	}

	var resp UploadResp
	_, err := d.Request(UploadRequest, http.MethodPost, func(r *resty.Request) {
		r.SetBody(data).SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}
	// reuse=true 或未返回上传 Key 均视为秒传成功
	if !resp.Data.Reuse && resp.Data.Key != "" {
		return nil, errs.ErrHashMismatch
	}

	return &model.ObjThumb{
		Object: model.Object{
			ID:       strconv.FormatInt(resp.Data.FileId, 10),
			Name:     req.Name,
			Size:     req.Size,
			Modified: time.Now(),
			IsFolder: false,
			HashInfo: utils.NewHashInfo(utils.MD5, strings.ToLower(etag)),
		},
	}, nil
}
