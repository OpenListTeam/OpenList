package strm

import (
	"context"
	"errors"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type Strm struct {
	model.Storage
	Addition
	pathMap     map[string][]string
	autoFlatten bool
	oneKey      string
}

func (d *Strm) Config() driver.Config {
	return config
}

func (d *Strm) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Strm) Init(ctx context.Context) error {
	if d.Paths == "" {
		return errors.New("paths is required")
	}
	d.pathMap = make(map[string][]string)
	for _, path := range strings.Split(d.Paths, "\n") {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		k, v := getPair(path)
		d.pathMap[k] = append(d.pathMap[k], v)
	}
	if len(d.pathMap) == 1 {
		for k := range d.pathMap {
			d.oneKey = k
		}
		d.autoFlatten = true
	} else {
		d.oneKey = ""
		d.autoFlatten = false
	}

	if d.FilterFileTypes != "" {
		types := strings.Split(d.FilterFileTypes, ",")
		for _, ext := range types {
			ext = strings.ToLower(strings.TrimSpace(ext))
			if ext != "" {
				supportSuffix[ext] = struct{}{}
			}
		}
	}
	
	if d.DownloadFileTypes != "" {
        downloadTypes := strings.Split(d.DownloadFileTypes, ",")
        for _, ext := range downloadTypes {
            ext = strings.ToLower(strings.TrimSpace(ext))
            if ext != "" {
                downloadSuffix[ext] = struct{}{}
            }
        }
    }
    return nil
}

func (d *Strm) Drop(ctx context.Context) error {
	d.pathMap = nil
	return nil
}

func (d *Strm) Get(ctx context.Context, path string) (model.Obj, error) {
	if utils.PathEqual(path, "/") {
		return &model.Object{
			Name:     "Root",
			IsFolder: true,
			Path:     "/",
		}, nil
	}
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	for _, dst := range dsts {
		obj, err := d.get(ctx, path, dst, sub)
		if err == nil {
			return obj, nil
		}
	}
	return nil, errs.ObjectNotFound
}

func (d *Strm) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	path := dir.GetPath()
	if utils.PathEqual(path, "/") && !d.autoFlatten {
		return d.listRoot(), nil
	}
	root, sub := d.getRootAndPath(path)
	dsts, ok := d.pathMap[root]
	if !ok {
		return nil, errs.ObjectNotFound
	}
	var objs []model.Obj
	fsArgs := &fs.ListArgs{NoLog: true, Refresh: args.Refresh}
	for _, dst := range dsts {
		tmp, err := d.list(ctx, dst, sub, fsArgs)
		if err == nil {
			objs = append(objs, tmp...)
		}
	}
	return objs, nil
}

func (d *Strm) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	link := d.getLink(ctx, file.GetPath())
	return &model.Link{
		MFile: strings.NewReader(link),
	}, nil
}

func (d *Strm) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	return errors.New("strm Driver cannot make dir")
}

func (d *Strm) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errors.New("strm Driver cannot move file")
}

func (d *Strm) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	return errors.New("strm Driver cannot rename file")
}

func (d *Strm) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	return errors.New("strm Driver cannot copy file")
}

func (d *Strm) Remove(ctx context.Context, obj model.Obj) error {
	return errors.New("strm Driver cannot remove file")
}

func (d *Strm) Put(ctx context.Context, dstDir model.Obj, s model.FileStreamer, up driver.UpdateProgress) error {
	return errors.New("strm Driver cannot put file")
}

var _ driver.Driver = (*Strm)(nil)
