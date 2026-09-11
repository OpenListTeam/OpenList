package thunder

import (
	"context"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/hash_extend"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// SeedRapidUpload 使用种子的哈希信息进行秒传
func (xc *XunLeiCommon) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// 迅雷使用 GCID 秒传
	gcid := hashes.GetHash(hash_extend.GCID)
	if len(gcid) < hash_extend.GCID.Width {
		return nil, errs.EmptyHash
	}

	var resp UploadTaskResponse
	_, err := xc.Request(FILE_API_URL, http.MethodPost, func(r *resty.Request) {
		r.SetContext(ctx)
		r.SetBody(&base.Json{
			"kind":        FILE,
			"parent_id":   dstDir.GetID(),
			"name":        fileName,
			"size":        fileSize,
			"hash":        gcid,
			"upload_type": UPLOAD_TYPE_RESUMABLE,
			"space":       xc.Space,
		})
	}, &resp)
	if err != nil {
		return nil, err
	}

	// 秒传成功（UploadType != UPLOAD_TYPE_RESUMABLE）
	if resp.UploadType != UPLOAD_TYPE_RESUMABLE {
		// 解析返回的文件信息
		file := fileToObj(resp.File)
		return file, nil
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
