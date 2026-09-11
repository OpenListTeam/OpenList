package _115

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
func (d *Pan115) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// 115 使用 SHA1 秒传
	sha1Hash := hashes.GetHash(utils.SHA1)
	if len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.EmptyHash
	}

	// 使用已有的 rapidUpload 私有方法
	stream := &hashOnlyStream{
		name:     fileName,
		size:     fileSize,
		hashInfo: hashes,
	}

	// 调用内部秒传方法
	return d.rapidUpload(fileSize, fileName, dstDir.GetID(), "", "", stream)
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
