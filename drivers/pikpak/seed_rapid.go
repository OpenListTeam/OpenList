package pikpak

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/hash_extend"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// SeedRapidUpload 使用种子的哈希信息进行秒传
func (d *PikPak) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// PikPak 使用 GCID 秒传
	gcid := hashes.GetHash(hash_extend.GCID)
	if len(gcid) < hash_extend.GCID.Width {
		return nil, errs.EmptyHash
	}

	var resp UploadTaskData
	res, err := d.request("https://api-drive.mypikpak.net/drive/v1/files", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"kind":        "drive#file",
			"name":        fileName,
			"size":        fileSize,
			"hash":        strings.ToUpper(gcid),
			"upload_type": "UPLOAD_TYPE_RESUMABLE",
			"objProvider": base.Json{"provider": "UPLOAD_TYPE_UNKNOWN"},
			"parent_id":   dstDir.GetID(),
			"folder_type": "NORMAL",
		})
	}, &resp)
	if err != nil {
		return nil, err
	}

	// 秒传成功（没有返回 Resumable）
	if resp.Resumable == nil {
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
