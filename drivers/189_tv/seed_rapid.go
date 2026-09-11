package _189_tv

import (
	"context"
	"io"
	"os"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// RapidHashAlgos 返回189电视支持的秒传哈希算法（MD5）
func (d *Cloud189TV) RapidHashAlgos() []*utils.HashType {
	return []*utils.HashType{utils.MD5}
}

// RapidHashNeedsPieces 189电视不需要分片哈希
func (d *Cloud189TV) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的MD5哈希尝试秒传
func (d *Cloud189TV) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	md5 := req.Whole.GetHash(utils.MD5)
	if md5 == "" {
		return nil, driver.ErrUnavailableHash
	}

	// 调用已有的 RapidUpload 方法
	obj, err := d.RapidUpload(ctx, dstDir.GetPath(), req.Name, req.Size, md5, req.Open)
	if err != nil {
		// 如果秒传失败，可能需要下载文件
		if err == errs.RapidUploadFailed {
			return nil, driver.ErrUnavailableHash
		}
		return nil, err
	}
	return obj, nil
}

// hashOnlyStream 仅提供文件元信息和哈希，不提供实际数据流
type hashOnlyStream struct {
	name     string
	size     int64
	hashInfo utils.HashInfo
	open     func() (model.FileStreamer, error)
}

func (s *hashOnlyStream) GetName() string              { return s.name }
func (s *hashOnlyStream) GetSize() int64               { return s.size }
func (s *hashOnlyStream) Close() error                 { return nil }
func (s *hashOnlyStream) GetHash() utils.HashInfo     { return s.hashInfo }
func (s *hashOnlyStream) GetMimetype() string          { return "" }
func (s *hashOnlyStream) NeedStore() bool              { return false }
func (s *hashOnlyStream) UseStreamer() bool            { return true }
func (s *hashOnlyStream) GetReadCloser() model.ReadCloserProvider {
	if s.open == nil {
		return nil
	}
	return func() (io.ReadCloser, error) {
		fs, err := s.open()
		if err != nil {
			return nil, err
		}
		rcp := fs.GetReadCloser()
		if rcp == nil {
			_ = fs.Close()
			return nil, os.ErrInvalid
		}
		return rcp()
	}
}

func (s *hashOnlyStream) ModTime() time.Time { return time.Now() }
