package chunk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	stdpath "path"
	"regexp"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type Chunk struct {
	model.Storage
	Addition
	partNameMatchRe *regexp.Regexp
}

func (d *Chunk) Config() driver.Config {
	return config
}

func (d *Chunk) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Chunk) Init(ctx context.Context) error {
	if d.PartSize <= 0 {
		return errors.New("part size must be positive")
	}
	d.RemotePath = utils.FixAndCleanPath(d.RemotePath)
	d.partNameMatchRe = regexp.MustCompile("^(.+)\\.openlist_chunk_(\\d+)" + regexp.QuoteMeta(d.CustomExt) + "$")
	return nil
}

func (d *Chunk) Drop(ctx context.Context) error {
	return nil
}

func (d *Chunk) Get(ctx context.Context, path string) (model.Obj, error) {
	if utils.PathEqual(path, "/") {
		return &model.Object{
			Name:     "Root",
			IsFolder: true,
			Path:     "/",
		}, nil
	}
	dir, base := stdpath.Split(stdpath.Join(d.RemotePath, path))
	objs, err := fs.List(ctx, dir, &fs.ListArgs{NoLog: true})
	if err != nil {
		return nil, err
	}
	partPrefix := base + ".openlist_chunk_"
	var first model.Obj
	var totalSize int64 = 0
	for _, obj := range objs {
		if obj.GetName() == base {
			first = obj
			if obj.IsDir() {
				totalSize = obj.GetSize()
				break
			} else {
				totalSize += obj.GetSize()
			}
		} else if strings.HasPrefix(obj.GetName(), partPrefix) {
			totalSize += obj.GetSize()
		}
	}
	if first == nil {
		return nil, errs.ObjectNotFound
	}
	return &model.Object{
		Path:     path,
		Name:     base,
		Size:     totalSize,
		Modified: first.ModTime(),
		Ctime:    first.CreateTime(),
		IsFolder: first.IsDir(),
	}, nil
}

func (d *Chunk) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	objs, err := fs.List(ctx, stdpath.Join(d.RemotePath, dir.GetPath()), &fs.ListArgs{NoLog: true, Refresh: args.Refresh})
	if err != nil {
		return nil, err
	}
	ret := make([]model.Obj, 0)
	sizeMap := make(map[string]int64)
	for _, obj := range objs {
		if obj.IsDir() {
			ret = append(ret, &model.Object{
				Name:     obj.GetName(),
				Size:     obj.GetSize(),
				Modified: obj.ModTime(),
				Ctime:    obj.CreateTime(),
				IsFolder: true,
			})
			continue
		}
		var name string
		matches := d.partNameMatchRe.FindStringSubmatch(obj.GetName())
		if len(matches) < 3 {
			ret = append(ret, &model.Object{
				Name:     obj.GetName(),
				Size:     0,
				Modified: obj.ModTime(),
				Ctime:    obj.CreateTime(),
				IsFolder: false,
			})
			name = obj.GetName()
		} else {
			name = matches[1]
		}
		_, ok := sizeMap[name]
		if !ok {
			sizeMap[name] = obj.GetSize()
		} else {
			sizeMap[name] += obj.GetSize()
		}
	}
	for _, obj := range ret {
		if !obj.IsDir() {
			obj.(*model.Object).Size = sizeMap[obj.GetName()]
		}
	}
	return ret, nil
}

func (d *Chunk) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	storage, reqActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return nil, err
	}
	args.Redirect = false
	path := stdpath.Join(reqActualPath, file.GetPath())
	links := make([]*model.Link, 0)
	rrfs := make([]model.RangeReaderIF, 0)
	totalLength := int64(0)
	for {
		l, o, err := op.Link(ctx, storage, d.getPartName(path, len(links)), args)
		if err != nil {
			if len(links) > 0 {
				if errors.Is(err, errs.ObjectNotFound) {
					break
				}
				for _, l1 := range links {
					_ = l1.Close()
				}
			}
			return nil, fmt.Errorf("failed get Part %d link: %w", len(links), err)
		}
		if l.ContentLength <= 0 {
			l.ContentLength = o.GetSize()
		}
		rrf, err := stream.GetRangeReaderFromLink(l.ContentLength, l)
		if err != nil {
			for _, l1 := range links {
				_ = l1.Close()
			}
			_ = l.Close()
			return nil, fmt.Errorf("failed get Part %d range reader: %w", len(links), err)
		}
		links = append(links, l)
		rrfs = append(rrfs, rrf)
		totalLength += l.ContentLength
	}
	mergedRrf := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		start := httpRange.Start
		length := httpRange.Length
		if length < 0 || start+length > totalLength {
			length = totalLength - start
		}
		rs := make([]io.Reader, 0)
		cs := make(utils.Closers, 0)
		var (
			rc       io.ReadCloser
			err      error
			readFrom bool
		)
		for idx, l := range links {
			if readFrom {
				newLength := length - l.ContentLength
				if newLength >= 0 {
					length = newLength
					rc, err = rrfs[idx].RangeRead(ctx, http_range.Range{Length: -1})
				} else {
					rc, err = rrfs[idx].RangeRead(ctx, http_range.Range{Length: length})
				}
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				rs = append(rs, rc)
				cs = append(cs, rc)
				if newLength <= 0 {
					return utils.ReadCloser{
						Reader: io.MultiReader(rs...),
						Closer: &cs,
					}, nil
				}
			} else if newStart := start - l.ContentLength; newStart >= 0 {
				start = newStart
			} else {
				rc, err = rrfs[idx].RangeRead(ctx, http_range.Range{Start: start, Length: -1})
				if err != nil {
					return nil, err
				}
				length -= l.ContentLength - start
				if length <= 0 {
					return rc, nil
				}
				rs = append(rs, rc)
				cs = append(cs, rc)
				readFrom = true
			}
		}
		return nil, fmt.Errorf("invalid range: start=%d,length=%d,totalLength=%d, status: %w", httpRange.Start, httpRange.Length, totalLength, net.HttpStatusCodeError(http.StatusRequestedRangeNotSatisfiable))
	}
	linkClosers := make([]io.Closer, 0, len(links))
	for _, l := range links {
		linkClosers = append(linkClosers, l)
	}
	return &model.Link{
		RangeReader: stream.RangeReaderFunc(mergedRrf),
		SyncClosers: utils.NewSyncClosers(linkClosers...),
	}, nil
}

func (d *Chunk) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	path := stdpath.Join(d.RemotePath, parentDir.GetPath(), dirName)
	return fs.MakeDir(ctx, path)
}

func (d *Chunk) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	path := stdpath.Join(d.RemotePath, srcObj.GetPath())
	dst := stdpath.Join(d.RemotePath, dstDir.GetPath())
	if srcObj.IsDir() {
		_, err := fs.Move(ctx, path, dst)
		return err
	}
	dir, base := stdpath.Split(path)
	objs, err := fs.List(ctx, dir, &fs.ListArgs{NoLog: true})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		suffix := strings.TrimPrefix(obj.GetName(), base)
		if suffix != obj.GetName() && strings.HasPrefix(suffix, ".openlist_chunk_") {
			_, e := fs.Move(ctx, path+suffix, dst, true)
			err = errors.Join(err, e)
		}
	}
	_, e := fs.Move(ctx, path, dst)
	return errors.Join(err, e)
}

func (d *Chunk) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	path := stdpath.Join(d.RemotePath, srcObj.GetPath())
	if srcObj.IsDir() {
		return fs.Rename(ctx, path, newName)
	}
	dir, base := stdpath.Split(path)
	objs, err := fs.List(ctx, dir, &fs.ListArgs{NoLog: true})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		suffix := strings.TrimPrefix(obj.GetName(), base)
		if suffix != obj.GetName() && strings.HasPrefix(suffix, ".openlist_chunk_") {
			err = errors.Join(err, fs.Rename(ctx, path+suffix, newName+suffix, true))
		}
	}
	return errors.Join(err, fs.Rename(ctx, path, newName))
}

func (d *Chunk) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	path := stdpath.Join(d.RemotePath, srcObj.GetPath())
	dst := stdpath.Join(d.RemotePath, dstDir.GetPath())
	if srcObj.IsDir() {
		_, err := fs.Copy(ctx, path, dst)
		return err
	}
	dir, base := stdpath.Split(path)
	objs, err := fs.List(ctx, dir, &fs.ListArgs{NoLog: true})
	if err != nil {
		return err
	}
	for _, obj := range objs {
		suffix := strings.TrimPrefix(obj.GetName(), base)
		if suffix != obj.GetName() && strings.HasPrefix(suffix, ".openlist_chunk_") {
			_, e := fs.Copy(ctx, path+suffix, dst, true)
			err = errors.Join(err, e)
		}
	}
	_, e := fs.Copy(ctx, path, dst)
	return errors.Join(err, e)
}

func (d *Chunk) Remove(ctx context.Context, obj model.Obj) error {
	path := stdpath.Join(d.RemotePath, obj.GetPath())
	if obj.IsDir() {
		return fs.Remove(ctx, path)
	}
	dir, base := stdpath.Split(path)
	objs, err := fs.List(ctx, dir, &fs.ListArgs{NoLog: true})
	if err != nil {
		return err
	}
	for _, o := range objs {
		suffix := strings.TrimPrefix(o.GetName(), base)
		if suffix != o.GetName() && strings.HasPrefix(suffix, ".openlist_chunk_") {
			err = errors.Join(err, fs.Remove(ctx, path+suffix))
		}
	}
	return errors.Join(err, fs.Remove(ctx, path))
}

func (d *Chunk) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	storage, reqActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return err
	}
	upReader := &driver.ReaderUpdatingProgress{
		Reader:         file,
		UpdateProgress: up,
	}
	dst := stdpath.Join(reqActualPath, dstDir.GetPath())
	fullPartCount := int(file.GetSize() / d.PartSize)
	tailSize := file.GetSize() % d.PartSize
	if tailSize == 0 && fullPartCount > 0 {
		fullPartCount--
		tailSize = d.PartSize
	}
	partIndex := 0
	for partIndex < fullPartCount {
		err = errors.Join(err, op.Put(ctx, storage, dst, &stream.FileStream{
			Obj: &model.Object{
				Name:     d.getPartName(file.GetName(), partIndex),
				Size:     d.PartSize,
				Modified: file.ModTime(),
			},
			Mimetype: file.GetMimetype(),
			Reader:   io.LimitReader(upReader, d.PartSize),
		}, nil, true))
		partIndex++
	}
	return errors.Join(err, op.Put(ctx, storage, dst, &stream.FileStream{
		Obj: &model.Object{
			Name:     d.getPartName(file.GetName(), fullPartCount),
			Size:     tailSize,
			Modified: file.ModTime(),
		},
		Mimetype: file.GetMimetype(),
		Reader:   upReader,
	}, nil))
}

func (d *Chunk) getPartName(name string, part int) string {
	if part == 0 {
		return name
	}
	return fmt.Sprintf("%s.openlist_chunk_%d%s", name, part, d.CustomExt)
}

var _ driver.Driver = (*Chunk)(nil)
