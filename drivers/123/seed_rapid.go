package _123

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回123云盘支持的秒传哈希算法（SHA1/MD5）
func (d *Yun123) RapidHashAlgos() []*utils.HashType {
	// 123 优先使用 SHA1（Etag），也支持 MD5
	return []*utils.HashType{utils.SHA1, utils.MD5}
}

// RapidHashNeedsPieces 123云盘不需要分片哈希
func (d *Yun123) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的SHA1或MD5哈希尝试秒传
func (d *Yun123) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	// 优先尝试 SHA1
	sha1 := req.Whole.GetHash(utils.SHA1)
	if sha1 == "" {
		// 降级到 MD5
		sha1 = req.Whole.GetHash(utils.MD5)
	}
	if sha1 == "" {
		return nil, driver.ErrUnavailableHash
	}

	// 调用已有的 Put 方法，它会自动处理秒传
	return d.Put(ctx, dstDir, &hashOnlyStream{
		name: req.Name,
		size: req.Size,
		hashInfo: utils.NewHashInfo(utils.SHA1, sha1),
	}, nil)
}

// hashOnlyStream 仅提供文件元信息和哈希，不提供实际数据流
type hashOnlyStream struct {
	name     string
	size     int64
	hashInfo utils.HashInfo
}

func (s *hashOnlyStream) GetName() string              { return s.name }
func (s *hashOnlyStream) GetSize() int64               { return s.size }
func (s *hashOnlyStream) Close() error                 { return nil }
func (s *hashOnlyStream) GetHash() utils.HashInfo     { return s.hashInfo }
func (s *hashOnlyStream) GetMimetype() string          { return "" }
func (s *hashOnlyStream) NeedStore() bool              { return false }
func (s *hashOnlyStream) UseStreamer() bool            { return true }
func (s *hashOnlyStream) GetReadCloser() model.ReadCloserProvider { return nil }
