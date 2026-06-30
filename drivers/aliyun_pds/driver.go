package aliyun_pds

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"math"
	"net/http"
	"os"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

type AliPDS struct {
	model.Storage
	Addition
	AccessToken  string
	ApiEndpoint  string
	AuthEndpoint string
	UIEndpoint   string
	cron         *cron.Cron
}

func (d *AliPDS) Config() driver.Config {
	return config
}

func (d *AliPDS) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *AliPDS) Init(ctx context.Context) error {
	// get entrypoint
	var endp EndpointResp
	_, err, _ := d.request("https://web-sv.aliyunpds.com/endpoint/get_endpoints", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{"domain_id": d.Addition.DomainID, "is_vpc": false, "product_type": "edm", "ignoreError": true})
	}, &endp)
	if err != nil {
		return err
	}
	d.ApiEndpoint = endp.APIEndpoint
	d.AuthEndpoint = endp.AuthEndpoint
	d.UIEndpoint = endp.UIEndpoint

	err = d.refreshToken()
	if err != nil {
		return err
	}
	d.cron = cron.NewCron(time.Hour * 2)
	d.cron.Do(func() {
		err := d.refreshToken()
		if err != nil {
			log.Errorf("%+v", err)
		}
	})
	return nil
}

func (d *AliPDS) Drop(ctx context.Context) error {
	if d.cron != nil {
		d.cron.Stop()
	}
	return nil
}

func (d *AliPDS) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *AliPDS) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	data := base.Json{
		"drive_id":   d.DriveID,
		"file_id":    file.GetID(),
		"file_name":  file.GetName(),
		"expire_sec": 7200,
	}
	res, err, _ := d.request(d.ApiEndpoint+"/v2/file/get_download_url", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, nil)
	if err != nil {
		return nil, err
	}
	var exp time.Duration
	parsedTime, _ := time.Parse(time.RFC3339, utils.Json.Get(res, "expiration").ToString())
	exp = time.Until(parsedTime) - time.Minute
	return &model.Link{
		Header: http.Header{
			"Referer": []string{d.UIEndpoint + "/"},
		},
		URL:        utils.Json.Get(res, "url").ToString(),
		Expiration: &exp,
	}, nil
}

func (d *AliPDS) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	_, err, _ := d.request(d.ApiEndpoint+"/v2/file/create", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"check_name_mode": "refuse",
			"drive_id":        d.DriveID,
			"name":            dirName,
			"parent_file_id":  parentDir.GetID(),
			"type":            "folder",
			"actionType":      "folder",
		})
	}, nil)
	return err
}

func (d *AliPDS) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	_, err, _ := d.request(d.ApiEndpoint+"/v2/file/move", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"auto_rename":       true,
			"drive_id":          d.DriveID,
			"file_id":           srcObj.GetID(),
			"to_drive_id":       d.DriveID,
			"to_parent_file_id": dstDir.GetID(),
		})
	}, nil)
	return err
}

func (d *AliPDS) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	_, err, _ := d.request(d.ApiEndpoint+"/v2/file/update", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"check_name_mode": "refuse",
			"drive_id":        d.DriveID,
			"file_id":         srcObj.GetID(),
			"name":            newName,
		})
	}, nil)
	return err
}

func (d *AliPDS) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	_, err, _ := d.request(d.ApiEndpoint+"/v2/file/copy", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"auto_rename":       true,
			"drive_id":          d.DriveID,
			"file_id":           srcObj.GetID(),
			"to_drive_id":       d.DriveID,
			"to_parent_file_id": dstDir.GetID(),
		})
	}, nil)
	return err
}

func (d *AliPDS) Remove(ctx context.Context, obj model.Obj) error {
	_, err, _ := d.request(d.ApiEndpoint+"/v2/recyclebin/trash", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"drive_id":    d.DriveID,
			"file_id":     obj.GetID(),
			"permanently": true,
		})
	}, nil)
	return err
}

func (d *AliPDS) Put(ctx context.Context, dstDir model.Obj, streamer model.FileStreamer, up driver.UpdateProgress) error {
	file := &stream.FileStream{
		Obj:      streamer,
		Reader:   streamer,
		Mimetype: streamer.GetMimetype(),
	}
	const DEFAULT int64 = 10485760
	count := int(math.Ceil(float64(streamer.GetSize()) / float64(DEFAULT)))

	partInfoList := make([]base.Json, 0, count)
	for i := 1; i <= count; i++ {
		partInfoList = append(partInfoList, base.Json{"part_number": i})
	}
	reqBody := base.Json{
		"check_name_mode": "refuse",
		"drive_id":        d.DriveID,
		"name":            file.GetName(),
		"parent_file_id":  dstDir.GetID(),
		"part_info_list":  partInfoList,
		"size":            file.GetSize(),
		"type":            "file",
	}

	var localFile *os.File
	if fileStream, ok := file.Reader.(*stream.FileStream); ok {
		localFile, _ = fileStream.Reader.(*os.File)
	}
	if d.RapidUpload {
		buf := bytes.NewBuffer(make([]byte, 0, 1024))
		_, err := utils.CopyWithBufferN(buf, file, 1024)
		if err != nil {
			return err
		}
		reqBody["pre_hash"] = utils.HashData(utils.SHA1, buf.Bytes())
		if localFile != nil {
			if _, err := localFile.Seek(0, io.SeekStart); err != nil {
				return err
			}
		} else {
			// 把头部拼接回去
			file.Reader = struct {
				io.Reader
				io.Closer
			}{
				Reader: io.MultiReader(buf, streamer),
				Closer: streamer,
			}
		}
	} else {
		reqBody["content_hash_name"] = "none"
	}

	var resp UploadResp
	_, err, e := d.request(d.ApiEndpoint+"/v2/file/create", http.MethodPost, func(req *resty.Request) {
		req.SetBody(reqBody)
	}, &resp)

	if err != nil && e.Code != "PreHashMatched" {
		return err
	}

	if d.RapidUpload && e.Code == "PreHashMatched" {
		delete(reqBody, "pre_hash")
		h := sha1.New()
		if localFile != nil {
			if err = utils.CopyWithCtx(ctx, h, localFile, 0, nil); err != nil {
				return err
			}
			if _, err = localFile.Seek(0, io.SeekStart); err != nil {
				return err
			}
		} else {
			tempFile, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
			if err != nil {
				return err
			}
			defer func() {
				_ = tempFile.Close()
				_ = os.Remove(tempFile.Name())
			}()
			if err = utils.CopyWithCtx(ctx, io.MultiWriter(tempFile, h), file, 0, nil); err != nil {
				return err
			}
			localFile = tempFile
		}
		reqBody["content_hash"] = hex.EncodeToString(h.Sum(nil))
		reqBody["content_hash_name"] = "sha1"

		_, err, e := d.request(d.ApiEndpoint+"/v2/file/create", http.MethodPost, func(req *resty.Request) {
			req.SetBody(reqBody)
		}, &resp)
		if err != nil && e.Code != "PreHashMatched" {
			return err
		}
		if resp.RapidUpload {
			return nil
		}
		// 秒传失败
		if _, err = localFile.Seek(0, io.SeekStart); err != nil {
			return err
		}
		file.Reader = localFile
	}

	rateLimited := driver.NewLimitedUploadStream(ctx, file)
	for i, partInfo := range resp.PartInfoList {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		url := partInfo.UploadUrl
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, io.LimitReader(rateLimited, DEFAULT))
		if err != nil {
			return err
		}
		res, err := base.HttpClient.Do(req)
		if err != nil {
			return err
		}
		_ = res.Body.Close()
		if count > 0 {
			up(float64(i) * 100 / float64(count))
		}
	}
	var resp2 base.Json
	_, err, e = d.request(d.ApiEndpoint+"/v2/file/complete", http.MethodPost, func(req *resty.Request) {
		req.SetBody(base.Json{
			"drive_id":  d.DriveID,
			"file_id":   resp.FileID,
			"upload_id": resp.UploadID,
		})
	}, &resp2)
	if err != nil && e.Code != "PreHashMatched" {
		return err
	}
	if resp2["file_id"] == resp.FileID {
		return nil
	}
	return fmt.Errorf("%+v", resp2)
}

func (d *AliPDS) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	var resp DriveResp
	_, err, _ := d.request(d.ApiEndpoint+"/v2/drive/get", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx)
		req.SetBody(base.Json{"drive_id": d.DriveID})
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: resp.TotalSize,
			UsedSpace:  resp.UsedSize,
		},
	}, nil
}

var _ driver.Driver = (*AliPDS)(nil)
