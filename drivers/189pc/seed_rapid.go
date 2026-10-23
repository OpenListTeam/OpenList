package _189pc

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回 189pc 支持的秒传哈希算法（MD5）
func (d *Cloud189PC) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.MD5}
}

// RapidHashNeedsPieces 189pc 的 CAS 秒传依赖分片 MD5
func (d *Cloud189PC) RapidHashNeedsPieces() bool {
	return true
}

// RapidUploadByHashes 使用种子中的 MD5 哈希尝试秒传
func (d *Cloud189PC) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	md5Hash := req.Whole.GetHash(utils.MD5)
	if len(md5Hash) < utils.MD5.Width {
		return nil, errs.ErrUnavailableHash
	}

	stream := driver.NewSeedHashStream(req)
	obj, err := d.RapidUpload(ctx, dstDir, stream, d.isFamily(), overwrite)
	if err != nil {
		return nil, err
	}
	return obj, nil
}
