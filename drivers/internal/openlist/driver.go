package openlist

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

func List(d State, ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	var resp common.Resp[FsListResp]
	_, _, err := d.Request("/fs/list", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ListReq{
			PageReq: model.PageReq{
				Page:    1,
				PerPage: 0,
			},
			Path:     dir.GetPath(),
			Password: *d.MetaPassword,
			Refresh:  d.ForwardRefresh && args.Refresh,
		})
	})
	if err != nil {
		return nil, err
	}
	var files []model.Obj
	for _, f := range resp.Data.Content {
		file := model.ObjThumb{
			Object: model.Object{
				Name:     f.Name,
				Path:     path.Join(dir.GetPath(), f.Name),
				Modified: f.Modified,
				Ctime:    f.Created,
				Size:     f.Size,
				IsFolder: f.IsDir,
				HashInfo: utils.FromString(f.HashInfo),
			},
			Thumbnail: model.Thumbnail{Thumbnail: f.Thumb},
		}
		files = append(files, &file)
	}
	return files, nil
}

func Link(d State, ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	var resp common.Resp[FsGetResp]
	headers := map[string]string{
		"User-Agent": base.UserAgent,
	}
	// if PassUAToUpsteam is true, then pass the user-agent to the upstream
	if d.PassUserAgent {
		userAgent := args.Header.Get("user-agent")
		if userAgent != "" {
			headers["User-Agent"] = userAgent
		}
	}
	// if PassIPToUpsteam is true, then pass the ip address to the upstream
	if d.PassIP {
		ip := args.IP
		if ip != "" {
			headers["X-Forwarded-For"] = ip
			headers["X-Real-Ip"] = ip
		}
	}
	_, _, err := d.Request("/fs/get", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(FsGetReq{
			Path:     file.GetPath(),
			Password: *d.MetaPassword,
		}).SetHeaders(headers)
	})
	if err != nil {
		return nil, err
	}
	return &model.Link{
		URL: resp.Data.RawURL,
	}, nil
}

func MakeDir(d State, ctx context.Context, parentDir model.Obj, dirName string) error {
	_, _, err := d.Request("/fs/mkdir", http.MethodPost, func(req *resty.Request) {
		req.SetBody(MkdirOrLinkReq{
			Path: path.Join(parentDir.GetPath(), dirName),
		})
	})
	return err
}

func Move(d State, ctx context.Context, srcObj, dstDir model.Obj) error {
	_, _, err := d.Request("/fs/move", http.MethodPost, func(req *resty.Request) {
		req.SetBody(MoveCopyReq{
			SrcDir: path.Dir(srcObj.GetPath()),
			DstDir: dstDir.GetPath(),
			Names:  []string{srcObj.GetName()},
		})
	})
	return err
}

func Rename(d State, ctx context.Context, srcObj model.Obj, newName string) error {
	_, _, err := d.Request("/fs/rename", http.MethodPost, func(req *resty.Request) {
		req.SetBody(RenameReq{
			Path: srcObj.GetPath(),
			Name: newName,
		})
	})
	return err
}

func Copy(d State, ctx context.Context, srcObj, dstDir model.Obj) error {
	_, _, err := d.Request("/fs/copy", http.MethodPost, func(req *resty.Request) {
		req.SetBody(MoveCopyReq{
			SrcDir: path.Dir(srcObj.GetPath()),
			DstDir: dstDir.GetPath(),
			Names:  []string{srcObj.GetName()},
		})
	})
	return err
}

func Remove(d State, ctx context.Context, obj model.Obj) error {
	_, _, err := d.Request("/fs/remove", http.MethodPost, func(req *resty.Request) {
		req.SetBody(RemoveReq{
			Dir:   path.Dir(obj.GetPath()),
			Names: []string{obj.GetName()},
		})
	})
	return err
}

func Put(d State, ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	reader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         s,
		UpdateProgress: up,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, *d.Address+"/api/fs/put", reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", *d.Token)
	req.Header.Set("File-Path", path.Join(dstDir.GetPath(), s.GetName()))
	req.Header.Set("Password", *d.MetaPassword)
	if md5 := s.GetHash().GetHash(utils.MD5); len(md5) > 0 {
		req.Header.Set("X-File-Md5", md5)
	}
	if sha1 := s.GetHash().GetHash(utils.SHA1); len(sha1) > 0 {
		req.Header.Set("X-File-Sha1", sha1)
	}
	if sha256 := s.GetHash().GetHash(utils.SHA256); len(sha256) > 0 {
		req.Header.Set("X-File-Sha256", sha256)
	}

	req.ContentLength = s.GetSize()
	// client := base.NewHttpClient()
	// client.Timeout = time.Hour * 6
	res, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}

	bytes, err := io.ReadAll(res.Body)
	if err != nil {
		return err
	}
	log.Debugf("[openlist] response body: %s", string(bytes))
	if res.StatusCode >= 400 {
		return fmt.Errorf("request failed, status: %s", res.Status)
	}
	code := utils.Json.Get(bytes, "code").ToInt()
	if code != 200 {
		if code == 401 || code == 403 {
			err = d.Login()
			if err != nil {
				return err
			}
		}
		return fmt.Errorf("request failed,code: %d, message: %s", code, utils.Json.Get(bytes, "message").ToString())
	}
	return nil
}

func GetArchiveMeta(d State, ctx context.Context, obj model.Obj, args model.ArchiveArgs) (model.ArchiveMeta, error) {
	if !d.ForwardArchives {
		return nil, errs.NotImplement
	}
	var resp common.Resp[ArchiveMetaResp]
	_, code, err := d.Request("/fs/archive/meta", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ArchiveMetaReq{
			ArchivePass: args.Password,
			Password:    *d.MetaPassword,
			Path:        obj.GetPath(),
			Refresh:     false,
		})
	})
	if code == 202 {
		return nil, errs.WrongArchivePassword
	}
	if err != nil {
		return nil, err
	}
	var tree []model.ObjTree
	if resp.Data.Content != nil {
		tree = make([]model.ObjTree, 0, len(resp.Data.Content))
		for _, content := range resp.Data.Content {
			tree = append(tree, &content)
		}
	}
	return &model.ArchiveMetaInfo{
		Comment:   resp.Data.Comment,
		Encrypted: resp.Data.Encrypted,
		Tree:      tree,
	}, nil
}

func ListArchive(d State, ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) ([]model.Obj, error) {
	if !d.ForwardArchives {
		return nil, errs.NotImplement
	}
	var resp common.Resp[ArchiveListResp]
	_, code, err := d.Request("/fs/archive/list", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ArchiveListReq{
			ArchiveMetaReq: ArchiveMetaReq{
				ArchivePass: args.Password,
				Password:    *d.MetaPassword,
				Path:        obj.GetPath(),
				Refresh:     false,
			},
			PageReq: model.PageReq{
				Page:    1,
				PerPage: 0,
			},
			InnerPath: args.InnerPath,
		})
	})
	if code == 202 {
		return nil, errs.WrongArchivePassword
	}
	if err != nil {
		return nil, err
	}
	var files []model.Obj
	for _, f := range resp.Data.Content {
		file := model.ObjThumb{
			Object: model.Object{
				Name:     f.Name,
				Modified: f.Modified,
				Ctime:    f.Created,
				Size:     f.Size,
				IsFolder: f.IsDir,
				HashInfo: utils.FromString(f.HashInfo),
			},
			Thumbnail: model.Thumbnail{Thumbnail: f.Thumb},
		}
		files = append(files, &file)
	}
	return files, nil
}

func Extract(d State, ctx context.Context, obj model.Obj, args model.ArchiveInnerArgs) (*model.Link, error) {
	if !d.ForwardArchives {
		return nil, errs.NotSupport
	}
	var resp common.Resp[ArchiveMetaResp]
	_, _, err := d.Request("/fs/archive/meta", http.MethodPost, func(req *resty.Request) {
		req.SetResult(&resp).SetBody(ArchiveMetaReq{
			ArchivePass: args.Password,
			Password:    *d.MetaPassword,
			Path:        obj.GetPath(),
			Refresh:     false,
		})
	})
	if err != nil {
		return nil, err
	}
	return &model.Link{
		URL: fmt.Sprintf("%s?inner=%s&pass=%s&sign=%s",
			resp.Data.RawURL,
			utils.EncodePath(args.InnerPath, true),
			url.QueryEscape(args.Password),
			resp.Data.Sign),
	}, nil
}

func ArchiveDecompress(d State, ctx context.Context, srcObj, dstDir model.Obj, args model.ArchiveDecompressArgs) error {
	if !d.ForwardArchives {
		return errs.NotImplement
	}
	dir, name := path.Split(srcObj.GetPath())
	_, _, err := d.Request("/fs/archive/decompress", http.MethodPost, func(req *resty.Request) {
		body := DecompressReq{
			ArchivePass:   args.Password,
			CacheFull:     args.CacheFull,
			DstDir:        dstDir.GetPath(),
			InnerPath:     args.InnerPath,
			Name:          []string{name},
			PutIntoNewDir: args.PutIntoNewDir,
			SrcDir:        dir,
		}
		if d.SendOverwrite {
			req.SetBody(struct {
				DecompressReq
				Overwrite bool `json:"overwrite"`
			}{body, args.Overwrite})
			return
		}
		req.SetBody(body)
	})
	return err
}

func ResolveLinkCacheMode(d State, _ string) driver.LinkCacheMode {
	var mode driver.LinkCacheMode
	if d.PassIP {
		mode |= driver.LinkCacheIP
	}
	if d.PassUserAgent {
		mode |= driver.LinkCacheUA
	}
	return mode
}
