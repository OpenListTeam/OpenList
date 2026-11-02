package baidu_netdisk

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回百度网盘支持的秒传哈希算法（MD5）
func (d *BaiduNetdisk) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.MD5}
}

// RapidHashNeedsPieces 百度网盘不需要分片哈希
func (d *BaiduNetdisk) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 MD5 哈希尝试秒传
func (d *BaiduNetdisk) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	md5Hash := req.Whole.GetHash(utils.MD5)
	if len(md5Hash) < utils.MD5.Width {
		return nil, errs.ErrUnavailableHash
	}

	stream := driver.NewSeedHashStream(req)
	obj, err := d.PutRapid(ctx, dstDir, stream)
	if err != nil {
		return nil, err
	}
	return obj, nil
}
