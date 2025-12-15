package quark_open

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/go-resty/resty/v2"
)

type QuarkOpen struct {
	model.Storage
	Addition
	config driver.Config
	conf   Conf
}

func (d *QuarkOpen) Config() driver.Config {
	return d.config
}

func (d *QuarkOpen) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *QuarkOpen) Init(ctx context.Context) error {
	var resp UserInfoResp

	_, err := d.request(ctx, "/open/v1/user/info", http.MethodGet, nil, &resp)
	if err != nil {
		return err
	}

	if resp.Data.UserID != "" {
		d.conf.userId = resp.Data.UserID
	} else {
		return errors.New("failed to get user ID")
	}

	return err
}

func (d *QuarkOpen) Drop(ctx context.Context) error {
	return nil
}

func (d *QuarkOpen) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.GetFiles(ctx, dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *QuarkOpen) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	data := base.Json{
		"fid": file.GetID(),
	}
	var resp FileLikeResp
	_, err := d.request(ctx, "/open/v1/file/get_download_url", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)
	if err != nil {
		return nil, err
	}

	return &model.Link{
		URL: resp.Data.DownloadURL,
		Header: http.Header{
			"Cookie": []string{d.generateAuthCookie()},
		},
		Concurrency: 3,
		PartSize:    10 * utils.MB,
	}, nil
}

func (d *QuarkOpen) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	data := base.Json{
		"dir_path": dirName,
		"pdir_fid": parentDir.GetID(),
	}
	_, err := d.request(ctx, "/open/v1/dir", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)

	return err
}

func (d *QuarkOpen) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	data := base.Json{
		"action_type": 1,
		"fid_list":    []string{srcObj.GetID()},
		"to_pdir_fid": dstDir.GetID(),
	}
	_, err := d.request(ctx, "/open/v1/file/move", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)

	return err
}

func (d *QuarkOpen) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	data := base.Json{
		"fid":           srcObj.GetID(),
		"file_name":     newName,
		"conflict_mode": "REUSE",
	}
	_, err := d.request(ctx, "/open/v1/file/rename", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)

	return err
}

func (d *QuarkOpen) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *QuarkOpen) Remove(ctx context.Context, obj model.Obj) error {
	data := base.Json{
		"action_type": 1,
		"fid_list":    []string{obj.GetID()},
	}
	_, err := d.request(ctx, "/open/v1/file/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)

	return err
}

func (d *QuarkOpen) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	md5Str, sha1Str := stream.GetHash().GetHash(utils.MD5), stream.GetHash().GetHash(utils.SHA1)
	var (
		md5  hash.Hash
		sha1 hash.Hash
	)
	writers := []io.Writer{}
	if len(md5Str) != utils.MD5.Width {
		md5 = utils.MD5.NewFunc()
		writers = append(writers, md5)
	}
	if len(sha1Str) != utils.SHA1.Width {
		sha1 = utils.SHA1.NewFunc()
		writers = append(writers, sha1)
	}

	if len(writers) > 0 {
		_, err := stream.CacheFullAndWriter(&up, io.MultiWriter(writers...))
		if err != nil {
			return err
		}
		if md5 != nil {
			md5Str = hex.EncodeToString(md5.Sum(nil))
		}
		if sha1 != nil {
			sha1Str = hex.EncodeToString(sha1.Sum(nil))
		}
	}
	// pre
	pre, err := d.upPre(ctx, stream, dstDir.GetID(), md5Str, sha1Str)
	if err != nil {
		return err
	}
	// 如果预上传已经完成，直接返回--秒传
	if pre.Data.Finish {
		up(100)
		return nil
	}

	// get part info
	partInfo := d._getPartInfo(stream, pre.Data.PartSize)
	// get upload url info
	upUrlInfo, err := d.upUrl(ctx, pre, partInfo)
	if err != nil {
		return err
	}

	// part up
	ss, err := streamPkg.NewStreamSectionReader(stream, int(pre.Data.PartSize), &up)
	if err != nil {
		return err
	}
	total := stream.GetSize()
	// 用于存储每个分片的ETag，后续commit时需要
	etags := make([]string, 0, len(partInfo))

	// 遍历上传每个分片
	for i := range len(upUrlInfo.UploadUrls) {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}

		offset := int64(i) * pre.Data.PartSize
		size := min(pre.Data.PartSize, total-offset)
		rd, err := ss.GetSectionReader(offset, size)
		if err != nil {
			return err
		}
		err = retry.Do(func() error {
			rd.Seek(0, io.SeekStart)
			etag, err := d.upPart(ctx, upUrlInfo, i, driver.NewLimitedUploadStream(ctx, rd))
			if err != nil {
				return err
			}
			etags = append(etags, etag)
			return nil
		},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second))
		ss.FreeSectionReader(rd)
		if err != nil {
			return fmt.Errorf("failed to upload part %d: %w", i, err)
		}

		up(95 * float64(offset+size) / float64(total))
	}

	defer up(100)
	return d.upFinish(ctx, pre, partInfo, etags)
}

var _ driver.Driver = (*QuarkOpen)(nil)
