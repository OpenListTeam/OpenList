package quark_open

import (
	"context"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回夸克网盘支持的秒传哈希算法（MD5 + SHA1）
func (d *QuarkOpen) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.MD5, *utils.SHA1}
}

// RapidHashNeedsPieces 夸克网盘不需要分片哈希
func (d *QuarkOpen) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 MD5/SHA1 哈希尝试秒传。
//
// 夸克网盘的预上传需要 proof_code（按 proof range 读取的一段内容），
// 因此当内容源不可用时无法完成秒传。
func (d *QuarkOpen) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	md5Hash := req.Whole.GetHash(utils.MD5)
	sha1Hash := req.Whole.GetHash(utils.SHA1)
	if len(md5Hash) < utils.MD5.Width || len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.ErrUnavailableHash
	}
	if req.Open == nil {
		return nil, errs.ErrUnavailableHash
	}

	stream, err := req.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	pre, err := d.upPre(ctx, stream, dstDir.GetID(), md5Hash, sha1Hash)
	if err != nil {
		return nil, err
	}
	if !pre.Data.Finish {
		return nil, errs.ErrHashMismatch
	}

	return &model.ObjThumb{
		Object: model.Object{
			ID:       pre.Data.Fid,
			Name:     req.Name,
			Size:     req.Size,
			IsFolder: false,
			Modified: time.Now(),
		},
	}, nil
}
