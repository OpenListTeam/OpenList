package _115

import (
	"context"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回 115 支持的秒传哈希算法（SHA1）
func (d *Pan115) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.SHA1}
}

// RapidHashNeedsPieces 115 不需要分片哈希
func (d *Pan115) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 SHA1 哈希尝试秒传。
//
// 115 的秒传协议除了整文件 SHA1，还需要文件头部 128KB 的 SHA1（pre_hash），
// 因此当内容源可用时会打开它来计算前置哈希；内容不可用时无法秒传。
func (d *Pan115) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	fullHash := strings.ToUpper(req.Whole.GetHash(utils.SHA1))
	if len(fullHash) != utils.SHA1.Width {
		return nil, errs.ErrUnavailableHash
	}
	if req.Open == nil {
		return nil, errs.ErrUnavailableHash
	}

	src, err := req.Open()
	if err != nil {
		return nil, err
	}
	defer src.Close()

	const PreHashSize int64 = 128 * utils.KB
	hashSize := PreHashSize
	if req.Size < PreHashSize {
		hashSize = req.Size
	}
	reader, err := src.RangeRead(http_range.Range{Start: 0, Length: hashSize})
	if err != nil {
		return nil, err
	}
	preHash, err := utils.HashReader(utils.SHA1, reader)
	if err != nil {
		return nil, err
	}
	preHash = strings.ToUpper(preHash)

	fastInfo, err := d.rapidUpload(req.Size, req.Name, dstDir.GetID(), preHash, fullHash, src)
	if err != nil {
		return nil, err
	}
	matched, err := fastInfo.Ok()
	if err != nil {
		return nil, err
	}
	if !matched {
		return nil, errs.ErrRapidUploadFailed
	}
	f, err := d.getNewFileByPickCode(fastInfo.PickCode)
	if err != nil {
		return nil, err
	}
	return f, nil
}
