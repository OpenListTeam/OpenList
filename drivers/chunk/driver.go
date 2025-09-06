package chunk

import (
	"context"
	"errors"
	"fmt"
	"io"
	stdpath "path"
	"regexp"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
	chunkSizes := []int64{-1}
	for _, obj := range objs {
		if obj.GetName() == base {
			first = obj
			if obj.IsDir() {
				totalSize = obj.GetSize()
				break
			} else {
				totalSize += obj.GetSize()
				chunkSizes[0] = obj.GetSize()
			}
		} else if suffix, ok := strings.CutPrefix(obj.GetName(), partPrefix); ok {
			idx, err := strconv.Atoi(strings.TrimSuffix(suffix, d.CustomExt))
			if err != nil {
				return nil, fmt.Errorf("invalid chunk part name: %s", obj.GetName())
			}
			totalSize += obj.GetSize()
			if len(chunkSizes) > idx {
				chunkSizes[idx] = obj.GetSize()
			} else if len(chunkSizes) == idx {
				chunkSizes = append(chunkSizes, obj.GetSize())
			} else {
				newChunkSizes := make([]int64, idx+1)
				copy(newChunkSizes, chunkSizes)
				chunkSizes = newChunkSizes
				chunkSizes[idx] = obj.GetSize()
			}
		}
	}
	if first == nil {
		return nil, errs.ObjectNotFound
	}
	for i, l := 0, len(chunkSizes)-1; i <= l; i++ {
		if (i == 0 && chunkSizes[i] == -1) || chunkSizes[i] == 0 {
			return nil, fmt.Errorf("some chunk parts are missing for file: %s", base)
		}
	}
	return &chunkObject{
		Object: model.Object{
			Path:     path,
			Name:     base,
			Size:     totalSize,
			Modified: first.ModTime(),
			Ctime:    first.CreateTime(),
			IsFolder: first.IsDir(),
		},
		chunkSizes: chunkSizes,
	}, nil
}

func (d *Chunk) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	objs, err := fs.List(ctx, stdpath.Join(d.RemotePath, dir.GetPath()), &fs.ListArgs{NoLog: true, Refresh: args.Refresh})
	if err != nil {
		return nil, err
	}
	ret := make([]model.Obj, 0)
	sizeMap := make(map[string]int64)
	chunkSizeMap := make(map[string][]int64)
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
		idx := 0
		if len(matches) < 3 {
			ret = append(ret, &chunkObject{
				Object: model.Object{
					Name:     obj.GetName(),
					Size:     0,
					Modified: obj.ModTime(),
					Ctime:    obj.CreateTime(),
					IsFolder: false,
				},
			})
			name = obj.GetName()
		} else {
			name = matches[1]
			idx, _ = strconv.Atoi(matches[2])
		}
		sizeMap[name] += obj.GetSize()
		// Collect chunk sizes
		chunkSizes, ok := chunkSizeMap[name]
		if !ok {
			chunkSizes = []int64{-1}
		}
		if len(chunkSizes) > idx {
			chunkSizes[idx] = obj.GetSize()
		} else if len(chunkSizes) == idx {
			chunkSizes = append(chunkSizes, obj.GetSize())
		} else {
			newChunkSizes := make([]int64, idx+1)
			copy(newChunkSizes, chunkSizes)
			chunkSizes = newChunkSizes
			chunkSizes[idx] = obj.GetSize()
		}
		chunkSizeMap[name] = chunkSizes
	}
	for _, obj := range ret {
		if !obj.IsDir() {
			chunkSizes := chunkSizeMap[obj.GetName()]
			for i, l := 0, len(chunkSizes)-1; i < l; i++ {
				if (i == 0 && chunkSizes[i] == -1) || chunkSizes[i] == 0 {
					return nil, fmt.Errorf("some chunk parts are missing for file: %s", obj.GetName())
				}
			}
			cObj := obj.(*chunkObject)
			cObj.Size = sizeMap[cObj.GetName()]
			cObj.chunkSizes = chunkSizes
		}
	}
	return ret, nil
}

func (d *Chunk) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	storage, reqActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return nil, err
	}
	chunkFile := file.(*chunkObject)
	args.Redirect = false
	path := stdpath.Join(reqActualPath, chunkFile.GetPath())
	if len(chunkFile.chunkSizes) <= 1 {
		l, _, err := op.Link(ctx, storage, path, args)
		return l, err
	}
	fileSize := chunkFile.GetSize()
	mergedRrf := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		start := httpRange.Start
		length := httpRange.Length
		if length < 0 || start+length > fileSize {
			length = fileSize - start
		}
		if length == 0 {
			return io.NopCloser(strings.NewReader("")), nil
		}
		rs := make([]io.Reader, 0)
		cs := make(utils.Closers, 0)
		var (
			rc       io.ReadCloser
			readFrom bool
		)
		for idx, chunkSize := range chunkFile.chunkSizes {
			if readFrom {
				l, o, err := op.Link(ctx, storage, d.getPartName(path, idx), args)
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				chunkSize2 := l.ContentLength
				if chunkSize2 <= 0 {
					chunkSize2 = o.GetSize()
				}
				if chunkSize2 != chunkSize {
					_ = cs.Close()
					_ = l.Close()
					return nil, fmt.Errorf("chunk part size changed, please retry: %s", o.GetPath())
				}
				rrf, err := stream.GetRangeReaderFromLink(chunkSize2, l)
				if err != nil {
					_ = cs.Close()
					_ = l.Close()
					return nil, err
				}
				newLength := length - chunkSize2
				if newLength >= 0 {
					length = newLength
					rc, err = rrf.RangeRead(ctx, http_range.Range{Length: -1})
				} else {
					rc, err = rrf.RangeRead(ctx, http_range.Range{Length: length})
				}
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				rs = append(rs, rc)
				cs = append(cs, rc, l)
				if newLength <= 0 {
					return utils.ReadCloser{
						Reader: io.MultiReader(rs...),
						Closer: &cs,
					}, nil
				}
			} else if newStart := start - chunkSize; newStart >= 0 {
				start = newStart
			} else {
				l, o, err := op.Link(ctx, storage, d.getPartName(path, idx), args)
				if err != nil {
					_ = cs.Close()
					_ = l.Close()
					return nil, err
				}
				chunkSize2 := l.ContentLength
				if chunkSize2 <= 0 {
					chunkSize2 = o.GetSize()
				}
				if chunkSize2 != chunkSize {
					_ = cs.Close()
					_ = l.Close()
					return nil, fmt.Errorf("chunk part size not match, need refresh cache: %s", o.GetPath())
				}
				rrf, err := stream.GetRangeReaderFromLink(chunkSize2, l)
				if err != nil {
					_ = cs.Close()
					_ = l.Close()
					return nil, err
				}
				rc, err = rrf.RangeRead(ctx, http_range.Range{Start: start, Length: -1})
				if err != nil {
					return nil, err
				}
				length -= chunkSize2 - start
				cs = append(cs, rc, l)
				if length <= 0 {
					return utils.ReadCloser{
						Reader: rc,
						Closer: &cs,
					}, nil
				}
				rs = append(rs, rc)
				cs = append(cs, rc, l)
				readFrom = true
			}
		}
		return nil, fmt.Errorf("invalid range: start=%d,length=%d,fileSize=%d", httpRange.Start, httpRange.Length, fileSize)
	}
	return &model.Link{
		RangeReader: stream.RangeReaderFunc(mergedRrf),
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
