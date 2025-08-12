package ftp

import (
	"context"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/jlaffaye/ftp"
)

// do others that not defined in Driver interface

func (d *FTP) login() error {
	_, err, _ := singleflight.AnyGroup.Do(fmt.Sprintf("FTP.login:%p", d), func() (any, error) {
		var err error
		if d.conn != nil {
			err = d.conn.NoOp()
			if err != nil {
				d.conn.Quit()
				d.conn = nil
			}
		}
		if d.conn == nil {
			d.conn, err = d._login(d.ctx)
		}
		return nil, err
	})
	return err
}

func (d *FTP) _login(ctx context.Context) (*ftp.ServerConn, error) {
	conn, err := ftp.Dial(d.Address, ftp.DialWithShutTimeout(10*time.Second), ftp.DialWithContext(ctx))
	if err != nil {
		return nil, err
	}
	err = conn.Login(d.Username, d.Password)
	if err != nil {
		conn.Quit()
		return nil, err
	}
	return conn, nil
}
