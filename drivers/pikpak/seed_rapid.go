package pikpak

import (
	"context"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
	"github.com/go-resty/resty/v2"
)

// RapidHashAlgos 返回 PikPak 支持的秒传哈希算法（GCID）
func (d *PikPak) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*hash_extend.GCID}
}

// RapidHashNeedsPieces PikPak 不需要分片哈希
func (d *PikPak) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 GCID 哈希尝试秒传
func (d *PikPak) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	gcid := req.Whole.GetHash(hash_extend.GCID)
	if len(gcid) < hash_extend.GCID.Width {
		return nil, errs.ErrUnavailableHash
	}

	var resp UploadTaskData
	_, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodPost, func(r *resty.Request) {
		r.SetContext(ctx).SetBody(base.Json{
			"kind":        "drive#file",
			"name":        req.Name,
			"size":        req.Size,
			"hash":        strings.ToUpper(gcid),
			"upload_type": "UPLOAD_TYPE_RESUMABLE",
			"objProvider": base.Json{"provider": "UPLOAD_TYPE_UNKNOWN"},
			"parent_id":   dstDir.GetID(),
			"folder_type": "NORMAL",
		})
	}, &resp)
	if err != nil {
		return nil, err
	}

	// 秒传成功时不会返回 Resumable
	if resp.Resumable == nil {
		file := fileToObj(resp.File)
		return file, nil
	}

	return nil, errs.ErrHashMismatch
}
