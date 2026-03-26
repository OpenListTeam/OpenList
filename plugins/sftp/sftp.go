package sftp

import (
	"context"
	"io"
	fs2 "io/fs"
	"net/http"
	"os"
	stdpath "path"
	"sync"
	"syscall"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/common"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	pkgsftp "github.com/pkg/sftp"
	"github.com/shirou/gopsutil/v4/disk"
)

type Handler struct {
	user        *model.User
	metaPass    string
	clientIP    string
	proxyHeader http.Header
}

func (h *Handler) Fileread(r *pkgsftp.Request) (io.ReaderAt, error) {
	flags := r.Pflags()
	if !flags.Read {
		return nil, os.ErrInvalid
	}
	return openDownload(h.requestContext(r), r.Filepath)
}

func (h *Handler) Filewrite(r *pkgsftp.Request) (io.WriterAt, error) {
	flags := r.Pflags()
	if !flags.Write {
		return nil, os.ErrInvalid
	}
	return openUpload(h.requestContext(r), r.Filepath, flags)
}

func (h *Handler) OpenFile(r *pkgsftp.Request) (pkgsftp.WriterAtReaderAt, error) {
	flags := r.Pflags()
	if flags.Read && !flags.Write {
		return newReadWriteHandle(openDownload(h.requestContext(r), r.Filepath))
	}
	if !flags.Write {
		return nil, os.ErrInvalid
	}
	return openUpload(h.requestContext(r), r.Filepath, flags)
}

func (h *Handler) Filecmd(r *pkgsftp.Request) error {
	ctx := h.requestContext(r)
	switch r.Method {
	case "Setstat":
		return setstat(ctx, r)
	case "Mkdir":
		return mkdir(ctx, r.Filepath)
	case "Remove", "Rmdir":
		return remove(ctx, r.Filepath)
	case "Rename":
		return rename(ctx, r.Filepath, r.Target)
	case "Link", "Symlink":
		return nil
	default:
		return nil
	}
}

func (h *Handler) Filelist(r *pkgsftp.Request) (pkgsftp.ListerAt, error) {
	ctx := h.requestContext(r)
	switch r.Method {
	case "List":
		items, err := list(ctx, r.Filepath)
		if err != nil {
			return nil, err
		}
		return &fileLister{items: items}, nil
	case "Stat":
		item, err := stat(ctx, r.Filepath)
		if err != nil {
			return nil, err
		}
		return &fileLister{items: []os.FileInfo{item}}, nil
	default:
		item, err := stat(ctx, r.Filepath)
		if err != nil {
			return nil, err
		}
		return &fileLister{items: []os.FileInfo{item}}, nil
	}
}

func (h *Handler) Lstat(r *pkgsftp.Request) (pkgsftp.ListerAt, error) {
	return h.Filelist(r)
}

func (h *Handler) RealPath(path string) (string, error) {
	return utils.FixAndCleanPath(path), nil
}

func (h *Handler) Readlink(path string) (string, error) {
	return utils.FixAndCleanPath(path), nil
}

func (h *Handler) PosixRename(r *pkgsftp.Request) error {
	return rename(h.requestContext(r), r.Filepath, r.Target)
}

func (h *Handler) StatVFS(_ *pkgsftp.Request) (*pkgsftp.StatVFS, error) {
	usage, err := disk.Usage(conf.Conf.TempDir)
	if err != nil {
		return nil, err
	}
	return &pkgsftp.StatVFS{
		Bsize:   4096,
		Frsize:  4096,
		Blocks:  usage.Total / 4096,
		Bfree:   usage.Free / 4096,
		Bavail:  usage.Free / 4096,
		Files:   usage.InodesTotal,
		Ffree:   usage.InodesFree,
		Favail:  usage.InodesFree,
		Namemax: 255,
	}, nil
}

func (h *Handler) requestContext(r *pkgsftp.Request) context.Context {
	ctx := r.Context()
	ctx = context.WithValue(ctx, conf.UserKey, h.user)
	ctx = context.WithValue(ctx, conf.MetaPassKey, h.metaPass)
	ctx = context.WithValue(ctx, conf.ClientIPKey, h.clientIP)
	ctx = context.WithValue(ctx, conf.ProxyHeaderKey, h.proxyHeader)
	return ctx
}

type fileDownloadProxy struct {
	model.File
	io.Closer
	ctx context.Context
}

func openDownload(ctx context.Context, filePath string) (*fileDownloadProxy, error) {
	reqPath, meta, err := resolveAccessiblePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, conf.MetaKey, meta)
	header, _ := ctx.Value(conf.ProxyHeaderKey).(http.Header)
	ip, _ := ctx.Value(conf.ClientIPKey).(string)
	link, obj, err := fs.Link(ctx, reqPath, model.LinkArgs{IP: ip, Header: header})
	if err != nil {
		return nil, err
	}
	ss, err := stream.NewSeekableStream(&stream.FileStream{
		Obj: obj,
		Ctx: ctx,
	}, link)
	if err != nil {
		_ = link.Close()
		return nil, err
	}
	reader, err := stream.NewReadAtSeeker(ss, 0)
	if err != nil {
		_ = ss.Close()
		return nil, err
	}
	return &fileDownloadProxy{File: reader, Closer: ss, ctx: ctx}, nil
}

func (f *fileDownloadProxy) ReadAt(p []byte, off int64) (n int, err error) {
	n, err = f.File.ReadAt(p, off)
	if err != nil && !errors.Is(err, io.EOF) {
		return n, err
	}
	waitErr := stream.ClientDownloadLimit.WaitN(f.ctx, n)
	if errors.Is(err, io.EOF) {
		return n, err
	}
	return n, waitErr
}

type uploadHandle struct {
	ctx    context.Context
	path   string
	file   *os.File
	flags  pkgsftp.FileOpenFlags
	closed sync.Once
}

func openUpload(ctx context.Context, filePath string, flags pkgsftp.FileOpenFlags) (*uploadHandle, error) {
	reqPath, err := resolveWritablePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	_, name := stdpath.Split(reqPath)
	if setting.GetBool(conf.IgnoreSystemFiles) && utils.IsSystemFile(name) {
		return nil, errs.IgnoredSystemFile
	}
	tmpFile, err := os.CreateTemp(conf.Conf.TempDir, "sftp-*")
	if err != nil {
		return nil, err
	}
	handle := &uploadHandle{ctx: ctx, path: reqPath, file: tmpFile, flags: flags}
	if !flags.Trunc || flags.Read || flags.Append {
		if err := handle.seed(); err != nil {
			_ = os.Remove(tmpFile.Name())
			_ = tmpFile.Close()
			return nil, err
		}
	}
	return handle, nil
}

func (u *uploadHandle) ReadAt(p []byte, off int64) (int, error) {
	return u.file.ReadAt(p, off)
}

func (u *uploadHandle) WriteAt(p []byte, off int64) (int, error) {
	n, err := u.file.WriteAt(p, off)
	if err != nil {
		return n, err
	}
	return n, stream.ClientUploadLimit.WaitN(u.ctx, n)
}

func (u *uploadHandle) Close() error {
	var retErr error
	u.closed.Do(func() {
		defer func() {
			_ = os.Remove(u.file.Name())
			_ = u.file.Close()
		}()
		size, err := u.file.Seek(0, io.SeekEnd)
		if err != nil {
			retErr = err
			return
		}
		if _, err := u.file.Seek(0, io.SeekStart); err != nil {
			retErr = err
			return
		}
		header := make([]byte, 512)
		n, err := u.file.Read(header)
		if err != nil && !errors.Is(err, io.EOF) {
			retErr = err
			return
		}
		if _, err := u.file.Seek(0, io.SeekStart); err != nil {
			retErr = err
			return
		}
		if u.flags.Trunc {
			_ = fs.Remove(u.ctx, u.path)
		}
		dir, name := stdpath.Split(u.path)
		file := &stream.FileStream{
			Obj: &model.Object{
				Name:     name,
				Size:     size,
				Modified: time.Now(),
				Ctime:    time.Now(),
			},
			Mimetype: http.DetectContentType(header[:n]),
			Reader:   u.file,
		}
		retErr = fs.PutDirectly(u.ctx, dir, file, true)
	})
	return retErr
}

func (u *uploadHandle) seed() error {
	reader, err := openDownload(u.ctx, u.path)
	if err != nil {
		if errors.Is(err, errs.ObjectNotFound) {
			return nil
		}
		return err
	}
	defer reader.Close()
	_, err = io.Copy(u.file, reader)
	if err != nil {
		return err
	}
	_, err = u.file.Seek(0, io.SeekStart)
	return err
}

type readWriteHandle struct {
	rd io.ReaderAt
}

func newReadWriteHandle(rd io.ReaderAt, err error) (*readWriteHandle, error) {
	if err != nil {
		return nil, err
	}
	return &readWriteHandle{rd: rd}, nil
}

func (h *readWriteHandle) ReadAt(p []byte, off int64) (int, error) {
	return h.rd.ReadAt(p, off)
}

func (h *readWriteHandle) WriteAt(_ []byte, _ int64) (int, error) {
	return 0, syscall.EBADF
}

func (h *readWriteHandle) Close() error {
	if closer, ok := h.rd.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

type osFileInfoAdapter struct {
	obj model.Obj
}

func (o *osFileInfoAdapter) Name() string       { return o.obj.GetName() }
func (o *osFileInfoAdapter) Size() int64        { return o.obj.GetSize() }
func (o *osFileInfoAdapter) ModTime() time.Time { return o.obj.ModTime() }
func (o *osFileInfoAdapter) IsDir() bool        { return o.obj.IsDir() }
func (o *osFileInfoAdapter) Sys() any           { return o.obj }

func (o *osFileInfoAdapter) Mode() fs2.FileMode {
	var mode fs2.FileMode = 0o755
	if o.obj.IsDir() {
		mode |= fs2.ModeDir
	}
	return mode
}

type fileLister struct {
	items []os.FileInfo
}

func (l *fileLister) ListAt(dst []os.FileInfo, offset int64) (int, error) {
	if offset >= int64(len(l.items)) {
		return 0, io.EOF
	}
	n := copy(dst, l.items[offset:])
	if int(offset)+n >= len(l.items) {
		return n, io.EOF
	}
	return n, nil
}

func stat(ctx context.Context, filePath string) (os.FileInfo, error) {
	reqPath, meta, err := resolveAccessiblePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, conf.MetaKey, meta)
	obj, err := fs.Get(ctx, reqPath, &fs.GetArgs{})
	if err != nil {
		return nil, err
	}
	return &osFileInfoAdapter{obj: obj}, nil
}

func list(ctx context.Context, filePath string) ([]os.FileInfo, error) {
	reqPath, meta, err := resolveAccessiblePath(ctx, filePath)
	if err != nil {
		return nil, err
	}
	ctx = context.WithValue(ctx, conf.MetaKey, meta)
	objs, err := fs.List(ctx, reqPath, &fs.ListArgs{})
	if err != nil {
		return nil, err
	}
	ret := make([]os.FileInfo, len(objs))
	for i, obj := range objs {
		ret[i] = &osFileInfoAdapter{obj: obj}
	}
	return ret, nil
}

func mkdir(ctx context.Context, filePath string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	reqPath, err := user.JoinPath(filePath)
	if err != nil {
		return err
	}
	if !user.CanWrite() || !user.CanFTPManage() {
		meta, err := op.GetNearestMeta(stdpath.Dir(reqPath))
		if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			return err
		}
		if !common.CanWrite(meta, reqPath) {
			return errs.PermissionDenied
		}
	}
	return fs.MakeDir(ctx, reqPath)
}

func remove(ctx context.Context, filePath string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	if !user.CanRemove() || !user.CanFTPManage() {
		return errs.PermissionDenied
	}
	reqPath, err := user.JoinPath(filePath)
	if err != nil {
		return err
	}
	return fs.Remove(ctx, reqPath)
}

func rename(ctx context.Context, oldPath, newPath string) error {
	user := ctx.Value(conf.UserKey).(*model.User)
	srcPath, err := user.JoinPath(oldPath)
	if err != nil {
		return err
	}
	dstPath, err := user.JoinPath(newPath)
	if err != nil {
		return err
	}
	srcDir, srcBase := stdpath.Split(srcPath)
	dstDir, dstBase := stdpath.Split(dstPath)
	if srcDir == dstDir {
		if !user.CanRename() || !user.CanFTPManage() {
			return errs.PermissionDenied
		}
		return fs.Rename(ctx, srcPath, dstBase)
	}
	if !user.CanFTPManage() || !user.CanMove() || (srcBase != dstBase && !user.CanRename()) {
		return errs.PermissionDenied
	}
	if srcBase != dstBase {
		if err := fs.Rename(ctx, srcPath, dstBase, true); err != nil {
			return err
		}
	}
	_, err = fs.Move(ctx, stdpath.Join(srcDir, dstBase), dstDir)
	return err
}

func setstat(ctx context.Context, r *pkgsftp.Request) error {
	if r.AttrFlags().Size {
		// Truncation semantics are storage-driver dependent.
		// Ignore the request for compatibility instead of failing the session.
		return nil
	}
	if r.AttrFlags().Permissions || r.AttrFlags().Acmodtime || r.AttrFlags().UidGid {
		return nil
	}
	return nil
}

func resolveAccessiblePath(ctx context.Context, filePath string) (string, *model.Meta, error) {
	user := ctx.Value(conf.UserKey).(*model.User)
	reqPath, err := user.JoinPath(filePath)
	if err != nil {
		return "", nil, err
	}
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return "", nil, err
	}
	if !common.CanAccess(user, meta, reqPath, ctx.Value(conf.MetaPassKey).(string)) {
		return "", nil, errs.PermissionDenied
	}
	return reqPath, meta, nil
}

func resolveWritablePath(ctx context.Context, filePath string) (string, error) {
	user := ctx.Value(conf.UserKey).(*model.User)
	reqPath, err := user.JoinPath(filePath)
	if err != nil {
		return "", err
	}
	meta, err := op.GetNearestMeta(stdpath.Dir(reqPath))
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return "", err
	}
	if !(common.CanAccess(user, meta, reqPath, ctx.Value(conf.MetaPassKey).(string)) &&
		((user.CanFTPManage() && user.CanWrite()) || common.CanWrite(meta, stdpath.Dir(reqPath)))) {
		return "", errs.PermissionDenied
	}
	return reqPath, nil
}
