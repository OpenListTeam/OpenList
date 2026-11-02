package thunder_browser

import (
	"context"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
	"github.com/go-resty/resty/v2"
)

// RapidHashAlgos 返回迅雷浏览器支持的秒传哈希算法（GCID）
func (xc *XunLeiBrowserCommon) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*hash_extend.GCID}
}

// RapidHashNeedsPieces 迅雷浏览器不需要分片哈希
func (xc *XunLeiBrowserCommon) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 GCID 哈希尝试秒传
func (xc *XunLeiBrowserCommon) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	gcid := req.Whole.GetHash(hash_extend.GCID)
	if len(gcid) < hash_extend.GCID.Width {
		return nil, errs.ErrUnavailableHash
	}

	var resp UploadTaskResponse
	_, err := xc.Request(FILE_API_URL, http.MethodPost, func(r *resty.Request) {
		r.SetContext(ctx)
		r.SetBody(&base.Json{
			"kind":        FILE,
			"parent_id":   dstDir.GetID(),
			"name":        req.Name,
			"size":        req.Size,
			"hash":        gcid,
			"upload_type": UPLOAD_TYPE_RESUMABLE,
		})
	}, &resp)
	if err != nil {
		return nil, err
	}

	// 秒传成功（UploadType != UPLOAD_TYPE_RESUMABLE）
	if resp.UploadType != UPLOAD_TYPE_RESUMABLE {
		return &resp.File, nil
	}

	return nil, errs.ErrHashMismatch
}
