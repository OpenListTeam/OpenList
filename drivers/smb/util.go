package smb

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/cloudsoda/go-smb2"
)

func (d *SMB) updateLastConnTime() {
	d.lastConnTime.Store(time.Now().Unix())
}

func (d *SMB) cleanLastConnTime() {
	d.lastConnTime.Store(0)
}

func (d *SMB) getLastConnTime() time.Time {
	return time.Unix(d.lastConnTime.Load(), 0)
}

func (d *SMB) initFS(ctx context.Context) error {
	_, err, _ := singleflight.AnyGroup.Do(fmt.Sprintf("SMB.initFS:%p", d), func() (any, error) {
		return nil, d._initFS(ctx)
	})
	return err
}

func (d *SMB) _initFS(ctx context.Context) error {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	return d.initFSLocked(ctx)
}

func (d *SMB) initFSLocked(ctx context.Context) error {
	_ = d.closeFSLocked()
	dialer := &smb2.Dialer{
		Initiator: &smb2.NTLMInitiator{
			User:     d.Username,
			Password: d.Password,
		},
	}
	conn, err := net.Dial("tcp", d.Address)
	if err != nil {
		return err
	}
	s, err := dialer.DialConn(ctx, conn, d.Address)
	if err != nil {
		_ = conn.Close()
		return err
	}
	fs, err := s.Mount(d.ShareName)
	if err != nil {
		_ = s.Logoff()
		_ = conn.Close()
		return err
	}
	d.conn = conn
	d.session = s
	d.fs = fs
	d.updateLastConnTime()
	return nil
}

func (d *SMB) closeFS() error {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	return d.closeFSLocked()
}

func (d *SMB) closeFSLocked() error {
	var err error
	if d.fs != nil {
		err = errors.Join(err, d.fs.Umount())
		d.fs = nil
	}
	if d.session != nil {
		err = errors.Join(err, d.session.Logoff())
		d.session = nil
	}
	if d.conn != nil {
		err = errors.Join(err, d.conn.Close())
		d.conn = nil
	}
	d.cleanLastConnTime()
	return err
}

func (d *SMB) acquireConn(ctx context.Context) (*smb2.Share, func(), error) {
	d.connMu.Lock()
	defer d.connMu.Unlock()

	if d.fs == nil || (time.Since(d.getLastConnTime()) >= 5*time.Minute && d.activeOps == 0) {
		if err := d.initFSLocked(ctx); err != nil {
			return nil, nil, err
		}
	}
	if d.fs == nil {
		return nil, nil, errors.New("smb share is not initialized")
	}
	d.activeOps++
	return d.fs, d.releaseConn, nil
}

func (d *SMB) releaseConn() {
	d.connMu.Lock()
	defer d.connMu.Unlock()
	if d.activeOps > 0 {
		d.activeOps--
	}
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
