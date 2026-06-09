package webdav

import (
	"crypto/tls"
	"net/http"
	"net/http/cookiejar"

	"github.com/OpenListTeam/OpenList/v4/drivers/webdav/odrvcookie"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/gowebdav"
)

// do others that not defined in Driver interface

func (d *WebDav) isSharepoint() bool {
	return d.Vendor == "sharepoint"
}

func (d *WebDav) setClient() error {
	c := gowebdav.NewClient(d.Address, d.Username, d.Password)
	c.SetTransport(&http.Transport{
		Proxy:           http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{InsecureSkipVerify: d.TlsInsecureSkipVerify},
	})
	if d.isSharepoint() {
		cookie, err := odrvcookie.GetCookie(d.Username, d.Password, d.Address)
		if err == nil {
			c.SetInterceptor(func(method string, rq *http.Request) {
				rq.Header.Del("Authorization")
				rq.Header.Set("Cookie", cookie)
			})
		} else {
			return err
		}
	} else {
		cookieJar, err := cookiejar.New(nil)
		if err == nil {
			c.SetJar(cookieJar)
		} else {
			return err
		}
	}
	d.client = c
	return nil
}

func getPath(obj model.Obj) string {
	if obj.IsDir() {
		return obj.GetPath() + "/"
	}
	return obj.GetPath()
}

func normalizeError(err error) error {
	if err == nil {
		return nil
	}
	if gowebdav.IsErrNotFound(err) {
		return errs.ObjectNotFound
	}
	return err
}
