package ftp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/tchap/go-patricia/v2/patricia"
)

var (
	stage      *patricia.Trie
	stageMutex sync.Mutex
)

func InitStage() {
	if stage != nil {
		return
	}
	stage = patricia.NewTrie(patricia.MaxPrefixPerNode(16))
	stageMutex = sync.Mutex{}
}

type uploadingFile struct {
	name     string
	size     int64
	modTime  time.Time
	refCount int
}

func MakeStage(ctx context.Context, buffer *os.File, size int64, path string) (*BorrowedFile, error) {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	prefix := patricia.Prefix(path)
	f := uploadingFile{
		name:     buffer.Name(),
		size:     size,
		modTime:  time.Now(),
		refCount: 1,
	}
	if !stage.Insert(prefix, f) {
		return nil, errors.New("upload path conflict")
	}
	return &BorrowedFile{
		file: buffer,
		path: prefix,
		ctx:  ctx,
	}, nil
}

func Borrow(ctx context.Context, path string) (*BorrowedFile, error) {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	prefix := patricia.Prefix(path)
	v := stage.Get(prefix)
	if v == nil {
		return nil, errs.ObjectNotFound
	}
	s := v.(*uploadingFile)
	borrowed, err := os.OpenFile(s.name, os.O_RDONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed borrow [%s]: %+v", s.name, err)
	}
	s.refCount++
	return &BorrowedFile{
		file: borrowed,
		path: prefix,
		ctx:  ctx,
	}, nil
}

func drop(path patricia.Prefix) {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	v := stage.Get(path)
	if v == nil {
		return
	}
	s := v.(*uploadingFile)
	s.refCount--
	if s.refCount == 0 {
		_ = os.RemoveAll(s.name)
		stage.Delete(path)
	}
}

func ListStage(path string) []model.Obj {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	path = path + "/"
	prefix := patricia.Prefix(path)
	ret := make([]model.Obj, 0)
	_ = stage.VisitSubtree(prefix, func(prefix patricia.Prefix, item patricia.Item) error {
		visit := string(prefix)
		visitSub := strings.TrimPrefix(visit, path)
		name, _, nonDirect := strings.Cut(visitSub, "/")
		if nonDirect {
			return nil
		}
		f := item.(*uploadingFile)
		ret = append(ret, &model.Object{
			Path:     visit,
			Name:     name,
			Size:     f.size,
			Modified: f.modTime,
			IsFolder: false,
		})
		return nil
	})
	return ret
}

type BorrowedFile struct {
	file *os.File
	path patricia.Prefix
	ctx  context.Context
}

func (f *BorrowedFile) Read(p []byte) (n int, err error) {
	n, err = f.file.Read(p)
	if err != nil {
		return n, err
	}
	err = stream.ClientDownloadLimit.WaitN(f.ctx, n)
	return n, err
}

func (f *BorrowedFile) ReadAt(p []byte, off int64) (n int, err error) {
	n, err = f.file.ReadAt(p, off)
	if err != nil {
		return n, err
	}
	err = stream.ClientDownloadLimit.WaitN(f.ctx, n)
	return n, err
}

func (f *BorrowedFile) Seek(offset int64, whence int) (int64, error) {
	return f.file.Seek(offset, whence)
}

func (f *BorrowedFile) Write(_ []byte) (n int, err error) {
	return 0, errs.NotSupport
}

func (f *BorrowedFile) Close() error {
	err := f.file.Close()
	drop(f.path)
	return err
}
