package ftp

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	ftpserver "github.com/fclairamb/ftpserverlib"
	"github.com/spf13/afero"
)

type AferoAdapter struct {
	ctx          context.Context
	nextFileSize int64
}

func NewAferoAdapter(ctx context.Context) *AferoAdapter {
	return &AferoAdapter{ctx: ctx}
}

func (a *AferoAdapter) Create(_ string) (afero.File, error) {
	// See also GetHandle
	return nil, errs.NotImplement
}

func (a *AferoAdapter) Mkdir(name string, _ os.FileMode) error {
	return Mkdir(a.ctx, name)
}

func (a *AferoAdapter) MkdirAll(path string, perm os.FileMode) error {
	return a.Mkdir(path, perm)
}

func (a *AferoAdapter) Open(_ string) (afero.File, error) {
	// See also GetHandle and ReadDir
	return nil, errs.NotImplement
}

func (a *AferoAdapter) OpenFile(_ string, _ int, _ os.FileMode) (afero.File, error) {
	// See also GetHandle
	return nil, errs.NotImplement
}

func (a *AferoAdapter) Remove(name string) error {
	return Remove(a.ctx, name)
}

func (a *AferoAdapter) RemoveAll(path string) error {
	return a.Remove(path)
}

func (a *AferoAdapter) Rename(oldName, newName string) error {
	return Rename(a.ctx, oldName, newName)
}

func (a *AferoAdapter) Stat(name string) (os.FileInfo, error) {
	return Stat(a.ctx, name)
}

func (a *AferoAdapter) Name() string {
	return "OpenList FTP Endpoint"
}

func (a *AferoAdapter) Chmod(_ string, _ os.FileMode) error {
	return errs.NotSupport
}

func (a *AferoAdapter) Chown(_ string, _, _ int) error {
	return errs.NotSupport
}

func (a *AferoAdapter) Chtimes(_ string, _ time.Time, _ time.Time) error {
	return errs.NotSupport
}

func (a *AferoAdapter) ReadDir(name string) ([]os.FileInfo, error) {
	return List(a.ctx, name)
}

func (a *AferoAdapter) GetHandle(name string, flags int, offset int64) (ftpserver.FileTransfer, error) {
	fileSize := a.nextFileSize
	a.nextFileSize = 0
	if (flags & os.O_SYNC) != 0 {
		return nil, errs.NotSupport
	}
	if (flags & os.O_APPEND) != 0 {
		return nil, errs.NotSupport
	}
	user := a.ctx.Value(conf.UserKey).(*model.User)
	path, err := user.JoinPath(name)
	if err != nil {
		return nil, err
	}
	if f, err := Borrow(a.ctx, path); !errors.Is(err, errs.ObjectNotFound) {
		if err != nil {
			return nil, err
		}
		if (flags & os.O_EXCL) != 0 {
			return nil, errs.ObjectAlreadyExists
		}
		if (flags & os.O_WRONLY) != 0 {
			return nil, errors.New("cannot write to uploading file")
		}
		_, err = f.Seek(offset, io.SeekStart)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("failed seek borrow: %+v", err)
		}
		return f, nil
	}
	_, err = fs.Get(a.ctx, path, &fs.GetArgs{})
	exists := err == nil
	if (flags&os.O_CREATE) == 0 && !exists {
		return nil, errs.ObjectNotFound
	}
	if (flags&os.O_EXCL) != 0 && exists {
		return nil, errs.ObjectAlreadyExists
	}
	if (flags & os.O_WRONLY) != 0 {
		if offset != 0 {
			return nil, errs.NotSupport
		}
		trunc := (flags & os.O_TRUNC) != 0
		if fileSize > 0 {
			return OpenUploadWithLength(a.ctx, path, trunc, fileSize)
		} else {
			return OpenUpload(a.ctx, path, trunc)
		}
	}
	return OpenDownload(a.ctx, path, offset)
}

func (a *AferoAdapter) Site(param string) *ftpserver.AnswerCommand {
	spl := strings.SplitN(param, " ", 2)
	cmd := strings.ToUpper(spl[0])
	var params string
	if len(spl) > 1 {
		params = spl[1]
	} else {
		params = ""
	}
	switch cmd {
	case "SIZE":
		code, msg := HandleSIZE(params, a)
		return &ftpserver.AnswerCommand{
			Code:    code,
			Message: msg,
		}
	}
	return nil
}

func (a *AferoAdapter) SetNextFileSize(size int64) {
	a.nextFileSize = size
}
