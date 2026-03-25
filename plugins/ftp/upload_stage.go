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
	log "github.com/sirupsen/logrus"
	"github.com/tchap/go-patricia/v2/patricia"
)

var (
	stage                *patricia.Trie
	stageMutex           = sync.Mutex{}
	ErrStagePathConflict = errors.New("upload path conflict")
	ErrStageMoved        = errors.New("uploading file has been moved")
)

func InitStage() {
	if stage != nil {
		return
	}
	stage = patricia.NewTrie(patricia.MaxPrefixPerNode(16))
}

type UploadingFile struct {
	name        string
	size        int64
	modTime     time.Time
	refCount    int
	currentPath string
	softLinks   []patricia.Prefix
	mvCallback  func(string)
	rmCallback  func()
}

func (u *UploadingFile) SetRemoveCallback(rm func()) {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	u.rmCallback = rm
}

type softLink struct {
	target *UploadingFile
}

func MakeStage(ctx context.Context, buffer *os.File, size int64, path string, mv func(string)) (*UploadingFile, *BorrowedFile, error) {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	prefix := patricia.Prefix(path)
	f := &UploadingFile{
		name:        buffer.Name(),
		size:        size,
		modTime:     time.Now(),
		refCount:    1,
		currentPath: path,
		softLinks:   []patricia.Prefix{},
		mvCallback:  mv,
	}
	if !stage.Insert(prefix, f) {
		return nil, nil, ErrStagePathConflict
	}
	log.Debugf("[ftp-stage] succeed to make [%s] stage", buffer.Name())
	return f, &BorrowedFile{
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
	s, ok := v.(*UploadingFile)
	if !ok {
		s = v.(*softLink).target
	}
	if s.currentPath != path {
		return nil, ErrStageMoved
	}
	borrowed, err := os.OpenFile(s.name, os.O_RDONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("failed borrow [%s]: %+v", s.name, err)
	}
	s.refCount++
	log.Debugf("[ftp-stage] borrow [%s] succeed", s.name)
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
	s, ok := v.(*UploadingFile)
	if !ok {
		s = v.(*softLink).target
	}
	s.refCount--
	log.Debugf("[ftp-stage] dropped [%s]", s.name)
	if s.refCount == 0 {
		log.Debugf("[ftp-stage] there is no more reference to [%s], removing temp file", s.name)
		err := os.RemoveAll(s.name)
		if err != nil {
			log.Errorf("[ftp-stage] failed to remove stage file [%s]: %+v", s.name, err)
		}
		for _, sl := range s.softLinks {
			stage.Delete(sl)
		}
		stage.Delete(path)
		if s.currentPath != string(path) {
			if s.currentPath != "" {
				go s.mvCallback(s.currentPath)
			}
		}
	}
}

func ListStage(path string) map[string]model.Obj {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	path = path + "/"
	prefix := patricia.Prefix(path)
	ret := make(map[string]model.Obj)
	_ = stage.VisitSubtree(prefix, func(prefix patricia.Prefix, item patricia.Item) error {
		visit := string(prefix)
		visitSub := strings.TrimPrefix(visit, path)
		name, _, nonDirect := strings.Cut(visitSub, "/")
		if nonDirect {
			return nil
		}
		f, ok := item.(*UploadingFile)
		if !ok {
			f = item.(*softLink).target
		}
		if f.currentPath == visit {
			ret[name] = &model.Object{
				Path:     visit,
				Name:     name,
				Size:     f.size,
				Modified: f.modTime,
				IsFolder: false,
			}
		}
		return nil
	})
	return ret
}

func StatStage(path string) (os.FileInfo, error) {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	prefix := patricia.Prefix(path)
	v := stage.Get(prefix)
	if v == nil {
		return nil, errs.ObjectNotFound
	}
	s, ok := v.(*UploadingFile)
	if !ok {
		s = v.(*softLink).target
	}
	if s.currentPath != path {
		return nil, ErrStageMoved
	}
	return os.Stat(s.name)
}

func MoveStage(from, to string) error {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	prefix := patricia.Prefix(from)
	v := stage.Get(prefix)
	if v == nil {
		return errs.ObjectNotFound
	}
	s, ok := v.(*UploadingFile)
	if !ok {
		s = v.(*softLink).target
	}
	if s.currentPath != from {
		return ErrStageMoved
	}
	slPrefix := patricia.Prefix(to)
	sl := &softLink{target: s}
	if !stage.Insert(slPrefix, sl) {
		return ErrStagePathConflict
	}
	s.currentPath = to
	s.softLinks = append(s.softLinks, slPrefix)
	return nil
}

func RemoveStage(path string) error {
	stageMutex.Lock()
	defer stageMutex.Unlock()
	prefix := patricia.Prefix(path)
	v := stage.Get(prefix)
	if v == nil {
		return errs.ObjectNotFound
	}
	s, ok := v.(*UploadingFile)
	if !ok {
		s = v.(*softLink).target
	}
	if s.currentPath != path {
		return ErrStageMoved
	}
	s.currentPath = ""
	if s.rmCallback != nil {
		s.rmCallback()
	}
	return nil
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
