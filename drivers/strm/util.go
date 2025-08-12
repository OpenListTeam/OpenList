package strm

import (
	"context"
	"fmt"

	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

func (d *Strm) listRoot() []model.Obj {
	var objs []model.Obj
	for k := range d.pathMap {
		obj := model.Object{
			Name:     k,
			IsFolder: true,
			Modified: d.Modified,
		}
		objs = append(objs, &obj)
	}
	return objs
}

// do others that not defined in Driver interface
func getPair(path string) (string, string) {
	//path = strings.TrimSpace(path)
	if strings.Contains(path, ":") {
		pair := strings.SplitN(path, ":", 2)
		if !strings.Contains(pair[0], "/") {
			return pair[0], pair[1]
		}
	}
	return stdpath.Base(path), path
}

func (d *Strm) getRootAndPath(path string) (string, string) {
	if d.autoFlatten {
		return d.oneKey, path
	}
	path = strings.TrimPrefix(path, "/")
	parts := strings.SplitN(path, "/", 2)
	if len(parts) == 1 {
		return parts[0], ""
	}
	return parts[0], parts[1]
}

func (d *Strm) list(ctx context.Context, dst, sub string, args *fs.ListArgs) ([]model.Obj, error) {
	reqPath := stdpath.Join(dst, sub)
	objs, err := fs.List(ctx, reqPath, args)
	if err != nil {
		return nil, err
	}

	var validObjs []model.Obj
	for _, obj := range objs {
		id, name, path := "", obj.GetName(), ""
		size := int64(0)
		if !obj.IsDir() {
			path = stdpath.Join(reqPath, obj.GetName())
			ext := strings.ToLower(utils.Ext(name))
			if _, ok := d.supportSuffix[ext]; ok {
				id = "strm"
				name = strings.TrimSuffix(name, ext) + "strm"
				size = int64(len(d.getLink(ctx, path)))
			} else if _, ok := d.downloadSuffix[ext]; ok {
				size = obj.GetSize()
			} else {
				continue
			}
		}
		objRes := model.Object{
			ID:       id,
			Path:     path,
			Name:     name,
			Size:     size,
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
		}

		thumb, ok := model.GetThumb(obj)
		if !ok {
			validObjs = append(validObjs, &objRes)
			continue
		}

		validObjs = append(validObjs, &model.ObjThumb{
			Object: objRes,
			Thumbnail: model.Thumbnail{
				Thumbnail: thumb,
			},
		})
	}
	return validObjs, nil
}

func (d *Strm) getLink(ctx context.Context, path string) string {
	finalPath := path
	if d.EncodePath {
		finalPath = utils.EncodePath(path, true)
	}
	if d.EnableSign {
		signPath := sign.Sign(path)
		finalPath = fmt.Sprintf("%s?sign=%s", finalPath, signPath)
	}
	if d.LocalModel {
		return finalPath
	}
	apiUrl := d.SiteUrl
	if len(apiUrl) > 0 {
		apiUrl = strings.TrimSuffix(apiUrl, "/")
	} else {
		apiUrl = common.GetApiUrl(ctx)
	}

	return fmt.Sprintf("%s/d%s",
		apiUrl,
		finalPath)
}

func (d *Strm) link(ctx context.Context, reqPath string, args model.LinkArgs) (*model.Link, model.Obj, error) {
	storage, reqActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		return nil, nil, err
	}
	if !args.Redirect {
		return op.Link(ctx, storage, reqActualPath, args)
	}
	obj, err := fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, nil, err
	}
	if common.ShouldProxy(storage, stdpath.Base(reqPath)) {
		return nil, obj, nil
	}
	return op.Link(ctx, storage, reqActualPath, args)
}
