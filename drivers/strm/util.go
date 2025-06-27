package strm

import (
	"context"
	"fmt"
	"github.com/OpenListTeam/OpenList/pkg/http_range"
	"io"

	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/internal/sign"
	"github.com/OpenListTeam/OpenList/pkg/utils"
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
	return &model.Object{
		Path: path,
		Name: obj.GetName(),
		Size: func() int64 {
			if obj.IsDir() {
				return 0
			}
			if utils.Ext(obj.GetName()) == "strm" {
				return obj.GetSize()
			}
			path := stdpath.Join(reqPath, obj.GetName())
			_, size := getLink(path, d)
			return size
		}(),
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
	for _, obj := range objs {
		fileName := obj.GetName()
		ext := utils.Ext(fileName)
		if _, ok := supportSuffix[ext]; !ok && !obj.IsDir() {
			continue
		}
		validObjs = append(validObjs, obj)
	}
	return utils.SliceConvert(validObjs, func(obj model.Obj) (model.Obj, error) {
		name := obj.GetName()
		if !obj.IsDir() {
			ext := utils.Ext(name)
			name = strings.TrimSuffix(name, ext) + "strm"
		}
		objRes := model.Object{
			Name: name,
			Size: func() int64 {
				if obj.IsDir() {
					return 0
				}
				if utils.Ext(obj.GetName()) == "strm" {
					return obj.GetSize()
				}
				path := stdpath.Join(reqPath, obj.GetName())
				_, size := getLink(path, d)
				return size
			}(),
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
			Path:     stdpath.Join(sub, obj.GetName()),
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
}

func (d *Strm) link(ctx context.Context, dst, sub string, args model.LinkArgs) (*model.Link, error) {
	reqPath := stdpath.Join(dst, sub)
	_, err := fs.Get(ctx, reqPath, &fs.GetArgs{NoLog: true})
	if err != nil {
		return nil, err
	}
	link, _ := getLink(reqPath, d)
	return link, nil
}

func getLink(path string, d *Strm) (*model.Link, int64) {
	url := d.SiteUrl
	if strings.HasSuffix(path, "/") {
		url = strings.TrimSuffix(url, "/")
	}
	finalUrl := fmt.Sprintf("%s/d%s?sign=%s",
		url,
		utils.EncodePath(path, true),
		sign.Sign(path))
	reader := strings.NewReader(finalUrl)
	return &model.Link{
		RangeReadCloser: &model.RangeReadCloser{
			RangeReader: func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
				if httpRange.Length < 0 {
					return io.NopCloser(reader), nil
				}
				sr := io.NewSectionReader(reader, httpRange.Start, httpRange.Length)
				return io.NopCloser(sr), nil
			},
		},
	}, int64(reader.Len())
}
