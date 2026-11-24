package _123_open

import (
	"context"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回 123 开放平台支持的秒传哈希算法（SHA1）
func (d *Open123) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.SHA1}
}

// RapidHashNeedsPieces 123 开放平台不需要分片哈希
func (d *Open123) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 SHA1 哈希尝试秒传
func (d *Open123) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	sha1Hash := req.Whole.GetHash(utils.SHA1)
	if len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.ErrUnavailableHash
	}

	parentID, err := strconv.ParseInt(dstDir.GetID(), 10, 64)
	if err != nil {
		return nil, err
	}

	resp, err := d.sha1Reuse(parentID, req.Name, sha1Hash, req.Size, 1)
	if err != nil {
		return nil, err
	}
	if !resp.Data.Reuse {
		return nil, errs.ErrHashMismatch
	}

	return &model.ObjThumb{
		Object: model.Object{
			ID:       strconv.FormatInt(resp.Data.FileID, 10),
			Name:     req.Name,
			Size:     req.Size,
			IsFolder: false,
			Path:     path.Join(dstDir.GetPath(), req.Name),
			Modified: time.Now(),
		},
	}, nil
}
