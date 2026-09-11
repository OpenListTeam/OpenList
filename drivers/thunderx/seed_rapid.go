package thunderx

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	hash_extend "github.com/OpenListTeam/OpenList/v4/pkg/utils/hash"
)

// RapidHashAlgos 返回迅雷X支持的秒传哈希算法（GCID）
func (d *ThunderX) RapidHashAlgos() []*utils.HashType {
	return []*utils.HashType{hash_extend.GCID}
}

// RapidHashNeedsPieces 迅雷X不需要分片哈希
func (d *ThunderX) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的GCID哈希尝试秒传
func (d *ThunderX) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	gcid := req.Whole.GetHash(hash_extend.GCID)
	if gcid == "" {
		return nil, driver.ErrUnavailableHash
	}

	// 调用已有的 Put 方法，它会自动处理 GCID 秒传
	// ThunderX 的 Put 实现与 Thunder 类似，通过 GCID 秒传
	return d.Put(ctx, dstDir, &hashOnlyStream{
		name: req.Name,
		size: req.Size,
		hashInfo: utils.NewHashInfo(hash_extend.GCID, gcid),
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
