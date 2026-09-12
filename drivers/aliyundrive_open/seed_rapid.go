package aliyundrive_open

import (
	"context"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
)

// RapidHashAlgos 返回阿里云盘支持的秒传哈希算法（SHA1）
func (d *AliyundriveOpen) RapidHashAlgos() []utils.HashType {
	return []utils.HashType{*utils.SHA1}
}

// RapidHashNeedsPieces 阿里云盘不需要分片哈希
func (d *AliyundriveOpen) RapidHashNeedsPieces() bool {
	return false
}

// RapidUploadByHashes 使用种子中的 SHA1 哈希尝试秒传。
//
// 阿里云盘的秒传还需要 proof_code（按 proof range 读取的一段内容），
// 因此当内容源不可用时无法完成秒传。
func (d *AliyundriveOpen) RapidUploadByHashes(ctx context.Context, dstDir model.Obj, req *driver.SeedRapidUploadRequest, overwrite bool) (model.Obj, error) {
	sha1Hash := req.Whole.GetHash(utils.SHA1)
	if len(sha1Hash) < utils.SHA1.Width {
		return nil, errs.ErrUnavailableHash
	}
	if req.Open == nil {
		return nil, errs.ErrUnavailableHash
	}

	stream, err := req.Open()
	if err != nil {
		return nil, err
	}
	defer stream.Close()

	proofCode, err := d.calProofCode(stream)
	if err != nil {
		return nil, err
	}

	var resp CreateResp
	_, err = d.request(ctx, limiterOther, "/adrive/v1.0/openFile/create", http.MethodPost, func(r *resty.Request) {
		r.SetBody(base.Json{
			"drive_id":          d.DriveId,
			"parent_file_id":    dstDir.GetID(),
			"name":              req.Name,
			"type":              "file",
			"check_name_mode":   "auto_rename",
			"size":              req.Size,
			"content_hash":      sha1Hash,
			"content_hash_name": "sha1",
			"proof_version":     "v1",
			"proof_code":        proofCode,
		}).SetResult(&resp)
	})
	if err != nil {
		return nil, err
	}
	if !resp.RapidUpload {
		return nil, errs.ErrHashMismatch
	}

	if resp.FileId != "" {
		obj, err := d.completeUpload(ctx, resp.FileId, resp.UploadId)
		if err != nil {
			return nil, err
		}
		return obj, nil
	}

	return &model.ObjThumb{
		Object: model.Object{
			Name:     req.Name,
			Size:     req.Size,
			Modified: time.Now(),
			IsFolder: false,
		},
	}, nil
}
