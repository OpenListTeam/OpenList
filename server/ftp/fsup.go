package ftp

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	stdpath "path"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/pkg/errors"
)

type FileUploadProxy struct {
	ftpserver.FileTransfer
	buffer *os.File
	path   string
	ctx    context.Context
	trunc  bool
}

func uploadAuth(ctx context.Context, path string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	meta, err := op.GetNearestMeta(stdpath.Dir(path))
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return err
		}
	}
	if !(common.CanAccess(user, meta, path, ctx.Value(conf.MetaPassKey).(string)) &&
		((user.CanFTPManage() && user.CanWrite()) || common.CanWrite(meta, stdpath.Dir(path)))) {
		return errs.PermissionDenied
	}
	return nil
}

func OpenUpload(ctx context.Context, path string, trunc bool) (*FileUploadProxy, error) {
	err := uploadAuth(ctx, path)
	if err != nil {
		return nil, err
	}
	// Check if system file should be ignored
	_, name := stdpath.Split(path)
	if setting.GetBool(conf.IgnoreSystemFiles) && utils.IsSystemFile(name) {
		return nil, errs.IgnoredSystemFile
	}
	tmpFile, err := os.CreateTemp(conf.Conf.TempDir, "file-*")
	if err != nil {
		return nil, err
	}
	return &FileUploadProxy{buffer: tmpFile, path: path, ctx: ctx, trunc: trunc}, nil
}

func (f *FileUploadProxy) Read(p []byte) (n int, err error) {
	return 0, errs.NotSupport
}

func (f *FileUploadProxy) Write(p []byte) (n int, err error) {
	n, err = f.buffer.Write(p)
	if err != nil {
		return n, err
	}
	err = stream.ClientUploadLimit.WaitN(f.ctx, n)
	return n, err
}

func (f *FileUploadProxy) Seek(offset int64, whence int) (int64, error) {
	return f.buffer.Seek(offset, whence)
}

func (f *FileUploadProxy) Close() error {
	dir, name := stdpath.Split(f.path)
	size, err := f.buffer.Seek(0, io.SeekCurrent)
	if err != nil {
		return err
	}
	if _, err := f.buffer.Seek(0, io.SeekStart); err != nil {
		return err
	}
	arr := make([]byte, 512)
	if _, err := f.buffer.Read(arr); err != nil {
		return err
	}
	contentType := http.DetectContentType(arr)
	if _, err := f.buffer.Seek(0, io.SeekStart); err != nil {
		return err
	}
	user := f.ctx.Value(conf.UserKey).(*model.User)
	sf, borrow, err := MakeStage(f.ctx, f.buffer, size, f.path, func(target string) {
		ctx := context.WithValue(context.Background(), conf.UserKey, user)
		dstDir, dstBase := stdpath.Split(target)
		if dir == dstDir {
			_ = fs.Rename(ctx, f.path, dstBase)
		} else {
			if name != dstBase {
				e := fs.Rename(ctx, f.path, dstBase, true)
				if e != nil {
					return
				}
			}
			_, _ = fs.Move(ctx, stdpath.Join(dir, dstBase), dstDir)
		}
	})
	if err != nil {
		return fmt.Errorf("failed make stage for [%s]: %+v", f.path, err)
	}
	if f.trunc {
		_ = fs.Remove(f.ctx, f.path)
	}
	s := &stream.FileStream{
		Obj: &model.Object{
			Name:     name,
			Size:     size,
			Modified: time.Now(),
		},
		Mimetype:     contentType,
		WebPutAsTask: true,
		Reader:       f.buffer,
	}
	s.Add(borrow)
	task, err := fs.PutAsTask(f.ctx, dir, s)
	if err != nil {
		_ = s.Close()
		return err
	}
	sf.SetRemoveCallback(func() {
		fs.UploadTaskManager.Cancel(task.GetID())
	})
	return nil
}

type FileUploadWithLengthProxy struct {
	ftpserver.FileTransfer
	ctx           context.Context
	path          string
	length        int64
	first512Bytes [512]byte
	pFirst        int
	pipeWriter    io.WriteCloser
	errChan       chan error
}

func OpenUploadWithLength(ctx context.Context, path string, trunc bool, length int64) (*FileUploadWithLengthProxy, error) {
	err := uploadAuth(ctx, path)
	if err != nil {
		return nil, err
	}
	// Check if system file should be ignored
	_, name := stdpath.Split(path)
	if setting.GetBool(conf.IgnoreSystemFiles) && utils.IsSystemFile(name) {
		return nil, errs.IgnoredSystemFile
	}
	if trunc {
		_ = fs.Remove(ctx, path)
	}
	return &FileUploadWithLengthProxy{ctx: ctx, path: path, length: length}, nil
}

func (f *FileUploadWithLengthProxy) Read(p []byte) (n int, err error) {
	return 0, errs.NotSupport
}

func (f *FileUploadWithLengthProxy) write(p []byte) (n int, err error) {
	if f.pipeWriter != nil {
		select {
		case e := <-f.errChan:
			return 0, e
		default:
			return f.pipeWriter.Write(p)
		}
	} else if len(p) < 512-f.pFirst {
		copy(f.first512Bytes[f.pFirst:], p)
		f.pFirst += len(p)
		return len(p), nil
	} else {
		copy(f.first512Bytes[f.pFirst:], p[:512-f.pFirst])
		contentType := http.DetectContentType(f.first512Bytes[:])
		dir, name := stdpath.Split(f.path)
		reader, writer := io.Pipe()
		f.errChan = make(chan error, 1)
		s := &stream.FileStream{
			Obj: &model.Object{
				Name:     name,
				Size:     f.length,
				Modified: time.Now(),
			},
			Mimetype:     contentType,
			WebPutAsTask: false,
			Reader:       reader,
		}
		go func() {
			e := fs.PutDirectly(f.ctx, dir, s, true)
			f.errChan <- e
			close(f.errChan)
		}()
		f.pipeWriter = writer
		n, err = writer.Write(f.first512Bytes[:])
		if err != nil {
			return n, err
		}
		n1, err := writer.Write(p[512-f.pFirst:])
		if err != nil {
			return n1 + 512 - f.pFirst, err
		}
		f.pFirst = 512
		return len(p), nil
	}
}

func (f *FileUploadWithLengthProxy) Write(p []byte) (n int, err error) {
	n, err = f.write(p)
	if err != nil {
		return n, err
	}
	err = stream.ClientUploadLimit.WaitN(f.ctx, n)
	return n, err
}

func (f *FileUploadWithLengthProxy) Seek(offset int64, whence int) (int64, error) {
	return 0, errs.NotSupport
}

func (f *FileUploadWithLengthProxy) Close() error {
	if f.pipeWriter != nil {
		err := f.pipeWriter.Close()
		if err != nil {
			return err
		}
		err = <-f.errChan
		return err
	} else {
		data := f.first512Bytes[:f.pFirst]
		contentType := http.DetectContentType(data)
		dir, name := stdpath.Split(f.path)
		s := &stream.FileStream{
			Obj: &model.Object{
				Name:     name,
				Size:     int64(f.pFirst),
				Modified: time.Now(),
			},
			Mimetype:     contentType,
			WebPutAsTask: false,
			Reader:       bytes.NewReader(data),
		}
		return fs.PutDirectly(f.ctx, dir, s)
	}
}
