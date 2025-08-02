package strm

import (
	"context"
	"fmt"

	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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

func (d *Strm) get(ctx context.Context, path string, dst, sub string) (model.Obj, error) {
	reqPath := stdpath.Join(dst, sub)
	obj, err := fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, err
	}
	size := int64(0)
	if !obj.IsDir() {
		if utils.Ext(obj.GetName()) == "strm" {
			size = obj.GetSize()
		} else {
			file := stdpath.Join(reqPath, obj.GetName())
			size = int64(len(d.getLink(ctx, file)))
		}
	}
	return &model.Object{
		Path:     path,
		Name:     obj.GetName(),
		Size:     size,
		Modified: obj.ModTime(),
		IsFolder: obj.IsDir(),
		HashInfo: obj.GetHash(),
	}, nil
}

func (d *Strm) list(ctx context.Context, dst, sub string, args *fs.ListArgs) ([]model.Obj, error) {
	reqPath := stdpath.Join(dst, sub)
	objs, err := fs.List(ctx, reqPath, args)
	if err != nil {
		return nil, err
	}

	var validObjs []model.Obj
	var downloadObjs []model.Obj
	for _, obj := range objs {
		if !obj.IsDir() {
			ext := strings.ToLower(utils.Ext(obj.GetName()))
			if _, ok := supportSuffix[ext]; !ok {
				if _, ok := downloadSuffix[ext]; ok {
					downloadObjs = append(downloadObjs, obj)
					continue
				}
			}
		}
		validObjs = append(validObjs, obj)
	}
	var validObjsResult, validObjsResultErr = utils.SliceConvert(validObjs, func(obj model.Obj) (model.Obj, error) {
		name := obj.GetName()
		size := int64(0)
		if !obj.IsDir() {
			ext := utils.Ext(name)
			name = strings.TrimSuffix(name, ext) + "strm"
			if ext == "strm" {
				size = obj.GetSize()
			} else {
				file := stdpath.Join(reqPath, obj.GetName())
				size = int64(len(d.getLink(ctx, file)))
			}
		}
		objRes := model.Object{
			Name:     name,
			Size:     size,
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
			Path:     stdpath.Join(reqPath, obj.GetName()),
		}
		thumb, ok := model.GetThumb(obj)
		if !ok {
			return &objRes, nil
		}
		return &model.ObjThumb{
			Object: objRes,
			Thumbnail: model.Thumbnail{
				Thumbnail: thumb,
			},
		}, nil
	})

	var downloadObjsResult, downloadObjsResultErr = utils.SliceConvert(downloadObjs, func(obj model.Obj) (model.Obj, error) {
		name := obj.GetName()
		size := int64(0)
		if !obj.IsDir() {
			file := stdpath.Join(reqPath, obj.GetName())
			size = int64(len(d.getLink(ctx, file)))
		}
		objRes := model.Object{
			Name:     name,
			Size:     size,
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
			Path:     stdpath.Join(reqPath, obj.GetName()),
		}
		thumb, ok := model.GetThumb(obj)
		if !ok {
			return &objRes, nil
		}
		return &model.ObjThumb{
			Object: objRes,
			Thumbnail: model.Thumbnail{
				Thumbnail: thumb,
			},
		}, nil
	})

	// Merge results and errors
	if validObjsResultErr != nil || downloadObjsResultErr != nil {
		return nil, utils.MergeErrors(validObjsResultErr, downloadObjsResultErr)
	}
	validObjs = append(validObjsResult, downloadObjsResult...)
	if len(validObjs) == 0 {
		return nil, fmt.Errorf("no valid objects found in %s", reqPath)
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
