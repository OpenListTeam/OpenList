package smb

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/cloudsoda/go-smb2"
)

func (d *SMB) updateLastConnTime() {
	atomic.StoreInt64(&d.lastConnTime, time.Now().Unix())
}

func (d *SMB) cleanLastConnTime() {
	atomic.StoreInt64(&d.lastConnTime, 0)
}

func (d *SMB) getLastConnTime() time.Time {
	return time.Unix(atomic.LoadInt64(&d.lastConnTime), 0)
}

func (d *SMB) initFS(ctx context.Context) error {
	_, err, _ := singleflight.AnyGroup.Do(fmt.Sprintf("SMB.initFS:%p", d), func() (any, error) {
		return nil, d._initFS(ctx)
	})
	return err
}
func (d *SMB) _initFS(ctx context.Context) error {
	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     d.Username,
			Password: d.Password,
		},
	}
	s, err := dialer.Dial(ctx, d.Address)
	if err != nil {
		return err
	}
	d.fs, err = s.Mount(d.ShareName)
	if err != nil {
		return err
	}
	d.updateLastConnTime()
	return err
}

func (d *SMB) checkConn(ctx context.Context) error {
	if time.Since(d.getLastConnTime()) < 5*time.Minute {
		return nil
	}
	if d.fs != nil {
		_ = d.fs.Umount()
	}
	return d.initFS(ctx)
}

// CopyFile File copies a single file from src to dst
func (d *SMB) CopyFile(src, dst string) error {
	var err error
	var srcfd *smb2.File
	var dstfd *smb2.File
	var srcinfo fs.FileInfo

	if srcfd, err = d.fs.Open(src); err != nil {
		return err
	}
	defer srcfd.Close()

	if dstfd, err = d.CreateNestedFile(dst); err != nil {
		return err
	}
	defer dstfd.Close()

	if _, err = utils.CopyWithBuffer(dstfd, srcfd); err != nil {
		return err
	}
	if srcinfo, err = d.fs.Stat(src); err != nil {
		return err
	}
	return d.fs.Chmod(dst, srcinfo.Mode())
}

// CopyDir Dir copies a whole directory recursively
func (d *SMB) CopyDir(src string, dst string) error {
	var err error
	var fds []fs.FileInfo
	var srcinfo fs.FileInfo

	if srcinfo, err = d.fs.Stat(src); err != nil {
		return err
	}
	if err = d.fs.MkdirAll(dst, srcinfo.Mode()); err != nil {
		return err
	}
	if fds, err = d.fs.ReadDir(src); err != nil {
		return err
	}
	for _, fd := range fds {
		srcfp := filepath.Join(src, fd.Name())
		dstfp := filepath.Join(dst, fd.Name())

		if fd.IsDir() {
			if err = d.CopyDir(srcfp, dstfp); err != nil {
				return err
			}
		} else {
			if err = d.CopyFile(srcfp, dstfp); err != nil {
				return err
			}
		}
	}
	return nil
}

// Exists determine whether the file exists
func (d *SMB) Exists(name string) bool {
	if _, err := d.fs.Stat(name); err != nil {
		if os.IsNotExist(err) {
			return false
		}
	}
	return true
}

// CreateNestedFile create nested file
func (d *SMB) CreateNestedFile(path string) (*smb2.File, error) {
	basePath := filepath.Dir(path)
	if !d.Exists(basePath) {
		err := d.fs.MkdirAll(basePath, 0700)
		if err != nil {
			return nil, err
		}
	}
	return d.fs.Create(path)
}
