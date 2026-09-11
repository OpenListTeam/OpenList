package quark_open

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
func (d *QuarkOpen) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// 夸克网盘使用 MD5 + SHA1 秒传
	md5Hash := hashes.GetHash(utils.MD5)
	sha1Hash := hashes.GetHash(utils.SHA1)
	
	if len(md5Hash) < utils.MD5.Width || len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.EmptyHash
	}

	// 调用预上传接口
	pre, err := d.upPre(ctx, &hashOnlyStream{
		name:     fileName,
		size:     fileSize,
		hashInfo: hashes,
	}, dstDir.GetID(), md5Hash, sha1Hash)
	if err != nil {
		return nil, err
	}

	// 如果预上传已经完成，说明秒传成功
	if pre.Data.Finish {
		// 返回文件对象
		return &model.ObjThumb{
			Object: model.Object{
				ID:       pre.Data.FID,
				Name:     fileName,
				Size:     fileSize,
				IsFolder: false,
				Modified: time.Now(),
			},
		}, nil
	}

	// 秒传失败
	return nil, errs.HashMismatch
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
