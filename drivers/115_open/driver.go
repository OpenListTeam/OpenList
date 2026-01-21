package _115_open

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"golang.org/x/time/rate"
)

type Open115 struct {
	model.Storage
	Addition
	client  *sdk.Client
	limiter *rate.Limiter
}

func (d *Open115) Config() driver.Config {
	return config
}

func (d *Open115) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Open115) Init(ctx context.Context) error {
	d.client = sdk.New(sdk.WithRefreshToken(d.Addition.RefreshToken),
		sdk.WithAccessToken(d.Addition.AccessToken),
		sdk.WithOnRefreshToken(func(s1, s2 string) {
			d.Addition.AccessToken = s1
			d.Addition.RefreshToken = s2
			op.MustSaveDriverStorage(d)
		}))
	if flags.Debug || flags.Dev {
		d.client.SetDebug(true)
	}
	_, err := d.client.UserInfo(ctx)
	if err != nil {
		return err
	}
	if d.Addition.LimitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.Addition.LimitRate), 1)
	}
	if d.PageSize <= 0 {
		d.PageSize = 200
	} else if d.PageSize > 1150 {
		d.PageSize = 1150
	}

	return nil
}

func (d *Open115) WaitLimit(ctx context.Context) error {
	if d.limiter != nil {
		return d.limiter.Wait(ctx)
	}
	return nil
}

func (d *Open115) Drop(ctx context.Context) error {
	return nil
}

func (d *Open115) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var res []model.Obj
	pageSize := int64(d.PageSize)
	offset := int64(0)
	for {
		if err := d.WaitLimit(ctx); err != nil {
			return nil, err
		}
		resp, err := d.client.GetFiles(ctx, &sdk.GetFilesReq{
			CID:    dir.GetID(),
			Limit:  pageSize,
			Offset: offset,
			ASC:    d.Addition.OrderDirection == "asc",
			O:      d.Addition.OrderBy,
			// Cur:     1,
			ShowDir: true,
		})
		if err != nil {
			return nil, err
		}
		res = append(res, utils.MustSliceConvert(resp.Data, func(src sdk.GetFilesResp_File) model.Obj {
			obj := Obj(src)
			return &obj
		})...)
		if len(res) >= int(resp.Count) {
			break
		}
		offset += pageSize
	}
	return res, nil
}

func (d *Open115) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	var ua string
	if args.Header != nil {
		ua = args.Header.Get("User-Agent")
	}
	if ua == "" {
		ua = base.UserAgent
	}
	obj, ok := file.(*Obj)
	if !ok {
		return nil, fmt.Errorf("can't convert obj")
	}
	pc := obj.Pc
	resp, err := d.client.DownURL(ctx, pc, ua)
	if err != nil {
		return nil, err
	}
	u, ok := resp[obj.GetID()]
	if !ok {
		return nil, fmt.Errorf("can't get link")
	}
	return &model.Link{
		URL: u.URL.URL,
		Header: http.Header{
			"User-Agent": []string{ua},
		},
	}, nil
}

func (d *Open115) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	resp, err := d.client.Mkdir(ctx, parentDir.GetID(), dirName)
	if err != nil {
		return nil, err
	}
	return &Obj{
		Fid:  resp.FileID,
		Pid:  parentDir.GetID(),
		Fn:   dirName,
		Fc:   "0",
		Upt:  time.Now().Unix(),
		Uet:  time.Now().Unix(),
		UpPt: time.Now().Unix(),
	}, nil
}

func (d *Open115) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	_, err := d.client.Move(ctx, &sdk.MoveReq{
		FileIDs: srcObj.GetID(),
		ToCid:   dstDir.GetID(),
	})
	if err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (d *Open115) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	_, err := d.client.UpdateFile(ctx, &sdk.UpdateFileReq{
		FileID:  srcObj.GetID(),
		FileNma: newName,
	})
	if err != nil {
		return nil, err
	}
	obj, ok := srcObj.(*Obj)
	if ok {
		obj.Fn = newName
	}
	return srcObj, nil
}

func (d *Open115) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if err := d.WaitLimit(ctx); err != nil {
		return nil, err
	}
	_, err := d.client.Copy(ctx, &sdk.CopyReq{
		PID:     dstDir.GetID(),
		FileID:  srcObj.GetID(),
		NoDupli: "1",
	})
	if err != nil {
		return nil, err
	}
	return srcObj, nil
}

func (d *Open115) Remove(ctx context.Context, obj model.Obj) error {
	if err := d.WaitLimit(ctx); err != nil {
		return err
	}
	_obj, ok := obj.(*Obj)
	if !ok {
		return fmt.Errorf("can't convert obj")
	}
	_, err := d.client.DelFile(ctx, &sdk.DelFileReq{
		FileIDs:  _obj.GetID(),
		ParentID: _obj.Pid,
	})
	if err != nil {
		return err
	}
	return nil
}

func (d *Open115) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	err := d.WaitLimit(ctx)
	if err != nil {
		return err
	}
	sha1 := file.GetHash().GetHash(utils.SHA1)
	if len(sha1) != utils.SHA1.Width {
		_, sha1, err = stream.CacheFullAndHash(file, &up, utils.SHA1)
		if err != nil {
			return err
		}
	}
	const PreHashSize int64 = 128 * utils.KB
	hashSize := PreHashSize
	if file.GetSize() < PreHashSize {
		hashSize = file.GetSize()
	}
	reader, err := file.RangeRead(http_range.Range{Start: 0, Length: hashSize})
	if err != nil {
		return err
	}
	sha1128k, err := utils.HashReader(utils.SHA1, reader)
	if err != nil {
		return err
	}
	// 1. Init
	resp, err := d.client.UploadInit(ctx, &sdk.UploadInitReq{
		FileName: file.GetName(),
		FileSize: file.GetSize(),
		Target:   dstDir.GetID(),
		FileID:   strings.ToUpper(sha1),
		PreID:    strings.ToUpper(sha1128k),
	})
	if err != nil {
		return err
	}
	if resp.Status == 2 {
		up(100)
		return nil
	}
	// 2. two way verify
	if utils.SliceContains([]int{6, 7, 8}, resp.Status) {
		signCheck := strings.Split(resp.SignCheck, "-") //"sign_check": "2392148-2392298" 取2392148-2392298之间的内容(包含2392148、2392298)的sha1
		start, err := strconv.ParseInt(signCheck[0], 10, 64)
		if err != nil {
			return err
		}
		end, err := strconv.ParseInt(signCheck[1], 10, 64)
		if err != nil {
			return err
		}
		reader, err = file.RangeRead(http_range.Range{Start: start, Length: end - start + 1})
		if err != nil {
			return err
		}
		signVal, err := utils.HashReader(utils.SHA1, reader)
		if err != nil {
			return err
		}
		resp, err = d.client.UploadInit(ctx, &sdk.UploadInitReq{
			FileName: file.GetName(),
			FileSize: file.GetSize(),
			Target:   dstDir.GetID(),
			FileID:   strings.ToUpper(sha1),
			PreID:    strings.ToUpper(sha1128k),
			SignKey:  resp.SignKey,
			SignVal:  strings.ToUpper(signVal),
		})
		if err != nil {
			return err
		}
		if resp.Status == 2 {
			up(100)
			return nil
		}
	}
	// 3. get upload token
	tokenResp, err := d.client.UploadGetToken(ctx)
	if err != nil {
		return err
	}
	// 4. upload
	err = d.multpartUpload(ctx, file, up, tokenResp, resp)
	if err != nil {
		return err
	}
	return nil
}

func (d *Open115) OfflineDownload(ctx context.Context, uris []string, dstDir model.Obj) ([]string, error) {
	return d.client.AddOfflineTaskURIs(ctx, uris, dstDir.GetID())
}

func (d *Open115) DeleteOfflineTask(ctx context.Context, infoHash string, deleteFiles bool) error {
	return d.client.DeleteOfflineTask(ctx, infoHash, deleteFiles)
}

func (d *Open115) OfflineList(ctx context.Context) (*sdk.OfflineTaskListResp, error) {
	resp, err := d.client.OfflineTaskList(ctx, 1)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func (d *Open115) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	userInfo, err := d.client.UserInfo(ctx)
	if err != nil {
		return nil, err
	}
	total, err := ParseInt64(userInfo.RtSpaceInfo.AllTotal.Size)
	if err != nil {
		return nil, err
	}
	used, err := ParseInt64(userInfo.RtSpaceInfo.AllUse.Size)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}

// func (d *Open115) GetArchiveMeta(ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
// 	// TODO get archive file meta-info, return errs.NotImplement to use an internal archive tool, optional
// 	return nil, errs.NotImplement
// }

// func (d *Open115) ListArchive(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
// 	// TODO list args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
// 	return nil, errs.NotImplement
// }

// func (d *Open115) Extract(ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
// 	// TODO return link of file args.InnerPath in the archive obj, return errs.NotImplement to use an internal archive tool, optional
// 	return nil, errs.NotImplement
// }

// func (d *Open115) ArchiveDecompress(ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) ([]model.Obj, error) {
// 	// TODO extract args.InnerPath path in the archive srcObj to the dstDir location, optional
// 	// a folder with the same name as the archive file needs to be created to store the extracted results if args.PutIntoNewDir
// 	// return errs.NotImplement to use an internal archive tool
// 	return nil, errs.NotImplement
// }

//func (d *Template) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*Open115)(nil)
