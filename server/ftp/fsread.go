package ftp

import (
	"context"
	"io"
	fs2 "io/fs"
	"net/http"
	"os"
	"time"

	"github.com/OpenListTeam/OpenList/internal/errs"
	"github.com/OpenListTeam/OpenList/internal/fs"
	"github.com/OpenListTeam/OpenList/internal/model"
	"github.com/OpenListTeam/OpenList/internal/op"
	"github.com/OpenListTeam/OpenList/internal/stream"
	"github.com/OpenListTeam/OpenList/server/common"
	"github.com/pkg/errors"
)

type FileDownloadProxy struct {
	model.File
	io.Closer
	ctx context.Context
}

func OpenDownload(ctx context.Context, reqPath string, offset int64) (*FileDownloadProxy, error) {
	user := ctx.Value("user").(*model.User)
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, "meta", meta)
	if !common.CanAccess(user, meta, reqPath, ctx.Value("meta_pass").(string)) {
		return nil, errs.PermissionDenied
	}

	// directly use proxy
	header := *(ctx.Value("proxy_header").(*http.Header))
	link, obj, err := fs.Link(ctx, reqPath, model.LinkArgs{
		IP:     ctx.Value("client_ip").(string),
		Header: header,
	})
	if err != nil {
		return nil, err
	}
	fileStream := stream.FileStream{
		Obj: obj,
		Ctx: ctx,
	}
	ss, err := stream.NewSeekableStream(fileStream, link)
	if err != nil {
		return nil, err
	}
	reader, err := stream.NewReadAtSeeker(ss, offset)
	if err != nil {
		_ = ss.Close()
		return nil, err
	}
	return &FileDownloadProxy{File: reader, Closer: ss, ctx: ctx}, nil
}

func (f *FileDownloadProxy) Read(p []byte) (n int, err error) {
	n, err = f.File.Read(p)
	if err != nil {
		return
	}
	err = stream.ClientDownloadLimit.WaitN(f.ctx, n)
	return
}

func (f *FileDownloadProxy) ReadAt(p []byte, off int64) (n int, err error) {
	n, err = f.File.ReadAt(p, off)
	if err != nil {
		return
	}
	err = stream.ClientDownloadLimit.WaitN(f.ctx, n)
	return
}

func (f *FileDownloadProxy) Write(p []byte) (n int, err error) {
	return 0, errs.NotSupport
}

type OsFileInfoAdapter struct {
	obj model.Obj
}

func (o *OsFileInfoAdapter) Name() string {
	return o.obj.GetName()
}

func (o *OsFileInfoAdapter) Size() int64 {
	return o.obj.GetSize()
}

func (o *OsFileInfoAdapter) Mode() fs2.FileMode {
	var mode fs2.FileMode = 0755
	if o.IsDir() {
		mode |= fs2.ModeDir
	}
	return mode
}

func (o *OsFileInfoAdapter) ModTime() time.Time {
	return o.obj.ModTime()
}

func (o *OsFileInfoAdapter) IsDir() bool {
	return o.obj.IsDir()
}

func (o *OsFileInfoAdapter) Sys() any {
	return o.obj
}

func Stat(ctx context.Context, path string) (os.FileInfo, error) {
	user := ctx.Value("user").(*model.User)
	reqPath, err := user.JoinPath(path)
	if err != nil {
		return nil, err
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, "meta", meta)
	if !common.CanAccess(user, meta, reqPath, ctx.Value("meta_pass").(string)) {
		return nil, errs.PermissionDenied
	}
	obj, err := fs.Get(ctx, reqPath, &fs.GetArgs{})
	if err != nil {
		return nil, err
	}
	return &OsFileInfoAdapter{obj: obj}, nil
}

func List(ctx context.Context, path string) ([]os.FileInfo, error) {
	user := ctx.Value("user").(*model.User)
	reqPath, err := user.JoinPath(path)
	if err != nil {
		return nil, err
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return nil, err
		}
	}
	ctx = context.WithValue(ctx, "meta", meta)
	if !common.CanAccess(user, meta, reqPath, ctx.Value("meta_pass").(string)) {
		return nil, errs.PermissionDenied
	}
	objs, err := fs.List(ctx, reqPath, &fs.ListArgs{})
	if err != nil {
		return nil, err
	}
	ret := make([]os.FileInfo, len(objs))
	for i, obj := range objs {
		ret[i] = &OsFileInfoAdapter{obj: obj}
	}
	return ret, nil
}
