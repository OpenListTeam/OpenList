package baidu_netdisk

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// SeedRapidUpload 使用种子的哈希信息进行秒传
func (d *BaiduNetdisk) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// 百度网盘使用 MD5 秒传
	md5Hash := hashes.GetHash(utils.MD5)
	if len(md5Hash) < utils.MD5.Width {
		return nil, errs.EmptyHash
	}

	// 使用已有的 PutRapid 方法
	stream := &hashOnlyStream{
		name:     fileName,
		size:     fileSize,
		hashInfo: hashes,
	}

	return d.PutRapid(ctx, dstDir, stream)
}

// hashOnlyStream 仅包含哈希信息的 FileStream
type hashOnlyStream struct {
	name     string
	size     int64
	hashInfo utils.HashInfo
}

func (s *hashOnlyStream) GetName() string                { return s.name }
func (s *hashOnlyStream) GetSize() int64                 { return s.size }
func (s *hashOnlyStream) GetHash() utils.HashInfo        { return s.hashInfo }
func (s *hashOnlyStream) Read(p []byte) (n int, err error) { return 0, io.EOF }
func (s *hashOnlyStream) Close() error                   { return nil }
func (s *hashOnlyStream) GetMimetype() string            { return "" }
func (s *hashOnlyStream) ModTime() time.Time             { return time.Now() }
func (s *hashOnlyStream) CreateTime() time.Time          { return time.Now() }
func (s *hashOnlyStream) GetFile() *os.File              { return nil }
