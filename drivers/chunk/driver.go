package chunk

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	stdpath "path"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/avast/retry-go"
)

type Chunk struct {
	model.Storage
	Addition
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
	if len(d.ChunkPrefix) <= 0 {
		return errors.New("chunk folder prefix must not be empty")
	}
	d.RemotePath = utils.FixAndCleanPath(d.RemotePath)
	return nil
}

func (d *Chunk) Drop(ctx context.Context) error {
	return nil
}

func (Addition) GetRootPath() string {
	return "/"
}

func (d *Chunk) Get(ctx context.Context, path string) (model.Obj, error) {
	remoteStorage, remoteActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return nil, err
	}
	remoteActualPath = stdpath.Join(remoteActualPath, path)
	if remoteObj, err := op.Get(ctx, remoteStorage, remoteActualPath); err == nil {
		return &model.Object{
			Path:     path,
			Name:     remoteObj.GetName(),
			Size:     remoteObj.GetSize(),
			Modified: remoteObj.ModTime(),
			IsFolder: remoteObj.IsDir(),
			HashInfo: remoteObj.GetHash(),
		}, nil
	}

	remoteActualDir, name := stdpath.Split(remoteActualPath)
	chunkName := d.ChunkPrefix + name
	chunkObjs, err := op.List(ctx, remoteStorage, stdpath.Join(remoteActualDir, chunkName), model.ListArgs{})
	if err != nil {
		return nil, err
	}
	var totalSize int64 = 0
	// 0号块默认为-1 以支持空文件
	chunkSizes := []int64{-1}
	h := make(map[*utils.HashType]string)
	var first model.Obj
	for _, o := range chunkObjs {
		if o.IsDir() {
			continue
		}
		if after, ok := strings.CutPrefix(o.GetName(), "hash_"); ok {
			hn, value, ok := strings.Cut(strings.TrimSuffix(after, d.CustomExt), "_")
			if ok {
				ht, ok := utils.GetHashByName(hn)
				if ok {
					h[ht] = value
				}
			}
			continue
		}
		idx, err := strconv.Atoi(strings.TrimSuffix(o.GetName(), d.CustomExt))
		if err != nil {
			continue
		}
		totalSize += o.GetSize()
		if len(chunkSizes) > idx {
			if idx == 0 {
				first = o
			}
			chunkSizes[idx] = o.GetSize()
		} else if len(chunkSizes) == idx {
			chunkSizes = append(chunkSizes, o.GetSize())
		} else {
			newChunkSizes := make([]int64, idx+1)
			copy(newChunkSizes, chunkSizes)
			chunkSizes = newChunkSizes
			chunkSizes[idx] = o.GetSize()
		}
	}
	reqDir, _ := stdpath.Split(path)
	objRes := chunkObject{
		Object: model.Object{
			Path:     stdpath.Join(reqDir, chunkName),
			Name:     name,
			Size:     totalSize,
			Modified: first.ModTime(),
			Ctime:    first.CreateTime(),
		},
		chunkSizes: chunkSizes,
	}
	if len(h) > 0 {
		objRes.HashInfo = utils.NewHashInfoByMap(h)
	}
	return &objRes, nil
}

func (d *Chunk) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	remoteStorage, remoteActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return nil, err
	}
	remoteActualDir := stdpath.Join(remoteActualPath, dir.GetPath())
	remoteObjs, err := op.List(ctx, remoteStorage, remoteActualDir, model.ListArgs{
		ReqPath: args.ReqPath,
		Refresh: args.Refresh,
	})
	if err != nil {
		return nil, err
	}
	result := make([]model.Obj, 0, len(remoteObjs))
	listG, listCtx := errgroup.NewGroupWithContext(ctx, d.NumListWorkers, retry.Attempts(3))
	for _, obj := range remoteObjs {
		if utils.IsCanceled(listCtx) {
			break
		}
		rawName := obj.GetName()
		if obj.IsDir() {
			if name, ok := strings.CutPrefix(rawName, d.ChunkPrefix); ok {
				resultIdx := len(result)
				result = append(result, nil)
				listG.Go(func(ctx context.Context) error {
					chunkObjs, err := op.List(ctx, remoteStorage, stdpath.Join(remoteActualDir, rawName), model.ListArgs{
						ReqPath: stdpath.Join(args.ReqPath, rawName),
						Refresh: args.Refresh,
					})
					if err != nil {
						return err
					}
					totalSize := int64(0)
					h := make(map[*utils.HashType]string)
					first := obj
					for _, o := range chunkObjs {
						if o.IsDir() {
							continue
						}
						if after, ok := strings.CutPrefix(strings.TrimSuffix(o.GetName(), d.CustomExt), "hash_"); ok {
							hn, value, ok := strings.Cut(after, "_")
							if ok {
								ht, ok := utils.GetHashByName(hn)
								if ok {
									h[ht] = value
								}
								continue
							}
						}
						idx, err := strconv.Atoi(strings.TrimSuffix(o.GetName(), d.CustomExt))
						if err != nil {
							continue
						}
						if idx == 0 {
							first = o
						}
						totalSize += o.GetSize()
					}
					objRes := model.Object{
						Name:     name,
						Size:     totalSize,
						Modified: first.ModTime(),
						Ctime:    first.CreateTime(),
					}
					if len(h) > 0 {
						objRes.HashInfo = utils.NewHashInfoByMap(h)
					}
					if !d.Thumbnail {
						result[resultIdx] = &objRes
					} else {
						thumbPath := stdpath.Join(args.ReqPath, ".thumbnails", name+".webp")
						thumb := fmt.Sprintf("%s/d%s?sign=%s",
							common.GetApiUrl(ctx),
							utils.EncodePath(thumbPath, true),
							sign.Sign(thumbPath))
						result[resultIdx] = &model.ObjThumb{
							Object: objRes,
							Thumbnail: model.Thumbnail{
								Thumbnail: thumb,
							},
						}
					}
					return nil
				})
				continue
			}
		}

		if !d.ShowHidden && strings.HasPrefix(rawName, ".") {
			continue
		}
		thumb, ok := model.GetThumb(obj)
		objRes := model.Object{
			Name:     rawName,
			Size:     obj.GetSize(),
			Modified: obj.ModTime(),
			IsFolder: obj.IsDir(),
			HashInfo: obj.GetHash(),
		}
		if !ok {
			result = append(result, &objRes)
		} else {
			result = append(result, &model.ObjThumb{
				Object: objRes,
				Thumbnail: model.Thumbnail{
					Thumbnail: thumb,
				},
			})
		}
	}
	if err = listG.Wait(); err != nil {
		return nil, err
	}
	return result, nil
}

func (d *Chunk) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	remoteStorage, remoteActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return nil, err
	}
	chunkFile, ok := file.(*chunkObject)
	remoteActualPath = stdpath.Join(remoteActualPath, file.GetPath())
	if !ok {
		l, _, err := op.Link(ctx, remoteStorage, remoteActualPath, args)
		if err != nil {
			return nil, err
		}
		resultLink := *l
		resultLink.SyncClosers = utils.NewSyncClosers(l)
		return &resultLink, nil
	}
	// 检查0号块不等于-1 以支持空文件
	// 如果块数量大于1 最后一块不可能为0
	// 只检查中间块是否有0
	if chunkFile.chunkSizes[0] == -1 {
		return nil, fmt.Errorf("chunk part[%d] are missing", 0)
	}
	for i, l := 1, len(chunkFile.chunkSizes)-1; i < l; i++ {
		if chunkFile.chunkSizes[i] == 0 {
			return nil, fmt.Errorf("chunk part[%d] are missing", i)
		}
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
				l, o, err := op.Link(ctx, remoteStorage, stdpath.Join(remoteActualPath, d.getPartName(idx)), args)
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				cs = append(cs, l)
				chunkSize2 := l.ContentLength
				if chunkSize2 <= 0 {
					chunkSize2 = o.GetSize()
				}
				if chunkSize2 != chunkSize {
					_ = cs.Close()
					return nil, fmt.Errorf("chunk part[%d] size not match", idx)
				}
				rrf, err := stream.GetRangeReaderFromLink(chunkSize2, l)
				if err != nil {
					_ = cs.Close()
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
				cs = append(cs, rc)
				if newLength <= 0 {
					return utils.ReadCloser{
						Reader: io.MultiReader(rs...),
						Closer: &cs,
					}, nil
				}
			} else if newStart := start - chunkSize; newStart >= 0 {
				start = newStart
			} else {
				l, o, err := op.Link(ctx, remoteStorage, stdpath.Join(remoteActualPath, d.getPartName(idx)), args)
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				cs = append(cs, l)
				chunkSize2 := l.ContentLength
				if chunkSize2 <= 0 {
					chunkSize2 = o.GetSize()
				}
				if chunkSize2 != chunkSize {
					_ = cs.Close()
					return nil, fmt.Errorf("chunk part[%d] size not match", idx)
				}
				rrf, err := stream.GetRangeReaderFromLink(chunkSize2, l)
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				rc, err = rrf.RangeRead(ctx, http_range.Range{Start: start, Length: -1})
				if err != nil {
					_ = cs.Close()
					return nil, err
				}
				length -= chunkSize2 - start
				cs = append(cs, rc)
				if length <= 0 {
					return utils.ReadCloser{
						Reader: rc,
						Closer: &cs,
					}, nil
				}
				rs = append(rs, rc)
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
	src := stdpath.Join(d.RemotePath, srcObj.GetPath())
	dst := stdpath.Join(d.RemotePath, dstDir.GetPath())
	_, err := fs.Move(ctx, src, dst)
	return err
}

func (d *Chunk) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if _, ok := srcObj.(*chunkObject); ok {
		newName = d.ChunkPrefix + newName
	}
	return fs.Rename(ctx, stdpath.Join(d.RemotePath, srcObj.GetPath()), newName)
}

func (d *Chunk) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	dst := stdpath.Join(d.RemotePath, dstDir.GetPath())
	src := stdpath.Join(d.RemotePath, srcObj.GetPath())
	_, err := fs.Copy(ctx, src, dst)
	return err
}

func (d *Chunk) Remove(ctx context.Context, obj model.Obj) error {
	return fs.Remove(ctx, stdpath.Join(d.RemotePath, obj.GetPath()))
}

func (d *Chunk) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	remoteStorage, remoteActualPath, err := op.GetStorageAndActualPath(d.RemotePath)
	if err != nil {
		return err
	}
	if (d.Thumbnail && dstDir.GetName() == ".thumbnails") || (d.ChunkLargeFileOnly && file.GetSize() <= d.PartSize) {
		return op.Put(ctx, remoteStorage, stdpath.Join(remoteActualPath, dstDir.GetPath()), file, up)
	}
	upReader := &driver.ReaderUpdatingProgress{
		Reader:         file,
		UpdateProgress: up,
	}
	dst := stdpath.Join(remoteActualPath, dstDir.GetPath(), d.ChunkPrefix+file.GetName())
	skipHookCtx := context.WithValue(ctx, conf.SkipHookKey, struct{}{})
	if d.StoreHash {
		for ht, value := range file.GetHash().All() {
			_ = op.Put(skipHookCtx, remoteStorage, dst, &stream.FileStream{
				Obj: &model.Object{
					Name:     fmt.Sprintf("hash_%s_%s%s", ht.Name, value, d.CustomExt),
					Size:     1,
					Modified: file.ModTime(),
				},
				Mimetype: "application/octet-stream",
				Reader:   bytes.NewReader([]byte{0}), // 兼容不支持空文件的驱动
			}, nil)
		}
	}
	fullPartCount := int(file.GetSize() / d.PartSize)
	tailSize := file.GetSize() % d.PartSize
	if tailSize == 0 && fullPartCount > 0 {
		fullPartCount--
		tailSize = d.PartSize
	}
	partIndex := 0
	for partIndex < fullPartCount {
		err = op.Put(skipHookCtx, remoteStorage, dst, &stream.FileStream{
			Obj: &model.Object{
				Name:     d.getPartName(partIndex),
				Size:     d.PartSize,
				Modified: file.ModTime(),
			},
			Mimetype: file.GetMimetype(),
			Reader:   io.LimitReader(upReader, d.PartSize),
		}, nil)
		if err != nil {
			_ = op.Remove(ctx, remoteStorage, dst)
			return err
		}
		partIndex++
	}
	err = op.Put(ctx, remoteStorage, dst, &stream.FileStream{
		Obj: &model.Object{
			Name:     d.getPartName(fullPartCount),
			Size:     tailSize,
			Modified: file.ModTime(),
		},
		Mimetype: file.GetMimetype(),
		Reader:   upReader,
	}, nil)
	if err != nil {
		_ = op.Remove(ctx, remoteStorage, dst)
	}
	return err
}

func (d *Chunk) getPartName(part int) string {
	return fmt.Sprintf("%d%s", part, d.CustomExt)
}

func (d *Chunk) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	remoteStorage, err := fs.GetStorage(d.RemotePath, &fs.GetStoragesArgs{})
	if err != nil {
		return nil, errs.NotImplement
	}
	remoteDetails, err := op.GetStorageDetails(ctx, remoteStorage)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: remoteDetails.DiskUsage,
	}, nil
}

var _ driver.Driver = (*Chunk)(nil)
