package _123_open

import (
	"context"
	"io"
	"os"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// SeedRapidUpload 使用种子的哈希信息进行秒传
func (d *Open123) SeedRapidUpload(ctx context.Context, dstDir model.Obj, fileName string, fileSize int64, hashes utils.HashInfo) (model.Obj, error) {
	// 123云盘使用 SHA1 秒传
	sha1Hash := hashes.GetHash(utils.SHA1)
	if len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.EmptyHash
	}

	// 获取父目录ID
	parentID, err := strconv.ParseInt(dstDir.GetID(), 10, 64)
	if err != nil {
		return nil, err
	}

	// 调用已有的 sha1Reuse 方法
	resp, err := d.sha1Reuse(parentID, fileName, sha1Hash, fileSize, 1)
	if err != nil {
		return nil, err
	}

	if !resp.Reuse {
		return nil, errs.HashMismatch
	}

	// 返回文件对象
	return &model.ObjThumb{
		Object: model.Object{
			ID:       strconv.FormatInt(resp.FileID, 10),
			Name:     fileName,
			Size:     fileSize,
			IsFolder: false,
			Path:     path.Join(dstDir.GetPath(), fileName),
			Modified: time.Now(),
		},
	}, nil
}
