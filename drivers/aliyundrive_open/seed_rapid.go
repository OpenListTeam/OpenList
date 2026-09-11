package aliyundrive_open

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// SeedRapidUpload 使用种子的哈希信息进行秒传
func (d *AliyundriveOpen) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// 阿里云盘使用 SHA1 秒传
	sha1Hash := hashes.GetHash(utils.SHA1)
	if len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.EmptyHash
	}

	// 调用创建文件接口，尝试秒传
	var resp CreateResp
	_, err := d.request(ctx, limiterOther, "/adrive/v1.0/openFile/create", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"drive_id":        d.DriveId,
			"parent_file_id":  dstDir.GetID(),
			"name":            fileName,
			"type":            "file",
			"check_name_mode": "auto_rename",
			"size":            fileSize,
			"content_hash":    sha1Hash,
			"content_hash_name": "sha1",
			"proof_version":   "v1",
		}).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}

	// 如果没有返回 UploadId，说明秒传成功
	if resp.UploadId == "" {
		if resp.RapidUpload {
			return fileToObj(resp.File), nil
		}
		// 文件已存在但不是秒传
		return fileToObj(resp.File), nil
	}

	// 秒传失败，需要分片上传
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
