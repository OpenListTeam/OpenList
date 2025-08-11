package quark

import (
	"bytes"
	"context"
	"encoding/hex"
	"hash"
	"io"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type QuarkOrUC struct {
	model.Storage
	Addition
	config driver.Config
	conf   Conf
}

func (d *QuarkOrUC) Config() driver.Config {
	return d.config
}

func (d *QuarkOrUC) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *QuarkOrUC) Init(ctx context.Context) error {
	_, err := d.request("/config", http.MethodGet, nil, nil)
	if err == nil {
		if d.AdditionVersion != 2 {
			d.AdditionVersion = 2
			if !d.UseTransCodingAddress && len(d.DownProxyURL) == 0 {
				d.WebProxy = true
				d.WebdavPolicy = "native_proxy"
			}
		}
	}
	return err
}

func (d *QuarkOrUC) Drop(ctx context.Context) error {
	return nil
}

func (d *QuarkOrUC) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.GetFiles(dir.GetID())
	if err != nil {
		return nil, err
	}

	return files, nil
}

func (d *QuarkOrUC) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	f := file.(*File)

	if d.UseTransCodingAddress && d.config.Name == "Quark" && f.Category == 1 && f.Size > 0 {
		return d.getTranscodingLink(file)
	}

	return d.getDownloadLink(file)
}

func (d *QuarkOrUC) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	data := base.Json{
		"dir_init_lock": false,
		"dir_path":      "",
		"file_name":     dirName,
		"pdir_fid":      parentDir.GetID(),
	}
	_, err := d.request("/file", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	if err == nil {
		time.Sleep(time.Second)
	}
	return err
}

func (d *QuarkOrUC) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	data := base.Json{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{srcObj.GetID()},
		"to_pdir_fid":  dstDir.GetID(),
	}
	_, err := d.request("/file/move", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkOrUC) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	data := base.Json{
		"fid":       srcObj.GetID(),
		"file_name": newName,
	}
	_, err := d.request("/file/rename", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkOrUC) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errs.NotSupport
}

func (d *QuarkOrUC) Remove(ctx context.Context, obj model.Obj) error {
	data := base.Json{
		"action_type":  1,
		"exclude_fids": []string{},
		"filelist":     []string{obj.GetID()},
	}
	_, err := d.request("/file/delete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	return err
}

func (d *QuarkOrUC) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
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
	pre, err := d.upPre(stream, dstDir.GetID())
	if err != nil {
		return err
	}
	log.Debugln("hash: ", md5Str, sha1Str)
	// hash
	finish, err := d.upHash(md5Str, sha1Str, pre.Data.TaskId)
	if err != nil {
		return err
	}
	if finish {
		return nil
	}
	// part up
	total := stream.GetSize()
	left := total
	partSize := int64(pre.Metadata.PartSize)
	part := make([]byte, partSize)
	count := int(total / partSize)
	if total%partSize > 0 {
		count++
	}
	md5s := make([]string, 0, count)
	partNumber := 1
	for left > 0 {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		if left < partSize {
			part = part[:left]
		}
		n, err := io.ReadFull(stream, part)
		if err != nil {
			return err
		}
		left -= int64(n)
		log.Debugf("left: %d", left)
		reader := driver.NewLimitedUploadStream(ctx, bytes.NewReader(part))
		m, err := d.upPart(ctx, pre, stream.GetMimetype(), partNumber, reader)
		//m, err := driver.UpPart(pre, file.GetMIMEType(), partNumber, bytes, account, md5Str, sha1Str)
		if err != nil {
			return err
		}
		if m == "finish" {
			return nil
		}
		md5s = append(md5s, m)
		partNumber++
		up(100 * float64(total-left) / float64(total))
	}
	err = d.upCommit(pre, md5s)
	if err != nil {
		return err
	}
	return d.upFinish(pre)
}

var _ driver.Driver = (*QuarkOrUC)(nil)
