package onedrive_sharelink

import (
	"context"
	"fmt"
	"io"
	"net/http"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

const headerTTL = 25 * time.Minute

type OnedriveSharelink struct {
	model.Storage
	cron *cron.Cron
	Addition

	headerMu sync.RWMutex
	sg       singleflight.Group[http.Header]
}

func (d *OnedriveSharelink) Config() driver.Config {
	return config
}

func (d *OnedriveSharelink) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *OnedriveSharelink) Init(ctx context.Context) error {
	// Initialize error variable
	var err error

	// If there is "-my" in the URL, it is NOT a SharePoint link
	d.IsSharepoint = !strings.Contains(d.ShareLinkURL, "-my")

	// Initialize cron job to run every hour
	d.cron = cron.NewCron(time.Hour * 1)
	d.cron.Do(func() {
		var err error
		h, err := d.getHeaders(ctx)
		if err != nil {
			log.Errorf("%+v", err)
			return
		}
		d.storeHeaders(h)
	})

	// Get initial headers
	h, err := d.getHeaders(ctx)
	if err != nil {
		return err
	}
	d.storeHeaders(h)

	return nil
}

func (d *OnedriveSharelink) Drop(ctx context.Context) error {
	return nil
}

func (d *OnedriveSharelink) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(ctx, dir.GetPath())
	if err != nil {
		return nil, err
	}

	// Convert the slice of files to the required model.Obj format
	return utils.SliceConvert(files, func(src Item) (model.Obj, error) {
		obj := fileToObj(src)
		obj.Path = stdpath.Join(dir.GetPath(), obj.GetName())
		return obj, nil
	})
}

func (d *OnedriveSharelink) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	// Get the unique ID of the file
	uniqueId := file.GetID()
	// Cut the first char and the last char
	uniqueId = uniqueId[1 : len(uniqueId)-1]
	url := d.downloadLinkPrefix + uniqueId

	header, err := d.getValidHeaders(ctx)
	if err != nil {
		return nil, err
	}

	return &model.Link{
		URL:    url,
		Header: header,
		RangeReader: rangeReaderFunc(func(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
			return d.rangeReadWithRefresh(ctx, url, hr)
		}),
	}, nil
}

func (d *OnedriveSharelink) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	// TODO create folder, optional
	return errs.NotImplement
}

func (d *OnedriveSharelink) Move(ctx context.Context, srcObj, dstDir model.Obj) error {
	// TODO move obj, optional
	return errs.NotImplement
}

func (d *OnedriveSharelink) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	// TODO rename obj, optional
	return errs.NotImplement
}

func (d *OnedriveSharelink) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	// TODO copy obj, optional
	return errs.NotImplement
}

func (d *OnedriveSharelink) Remove(ctx context.Context, obj model.Obj) error {
	// TODO remove obj, optional
	return errs.NotImplement
}

func (d *OnedriveSharelink) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	// TODO upload file, optional
	return errs.NotImplement
}

//func (d *OnedriveSharelink) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
//	return nil, errs.NotSupport
//}

var _ driver.Driver = (*OnedriveSharelink)(nil)

// rangeReadWithRefresh tries once with current headers, and if the response
// looks invalid (error status or html login page), it refreshes headers and retries.
func (d *OnedriveSharelink) rangeReadWithRefresh(ctx context.Context, url string, hr http_range.Range) (io.ReadCloser, error) {
	tryOnce := func(header http.Header) (io.ReadCloser, error) {
		h := cloneHeader(header)
		if h == nil {
			h = http.Header{}
		}
		h = http_range.ApplyRangeToHttpHeader(hr, h)
		resp, err := net.RequestHttp(ctx, http.MethodGet, h, url)
		if err != nil {
			return nil, err
		}
		ct := strings.ToLower(resp.Header.Get("Content-Type"))
		if strings.Contains(ct, "text/html") {
			_ = resp.Body.Close()
			return nil, fmt.Errorf("unexpected html response")
		}
		return resp.Body, nil
	}

	header, err := d.getValidHeaders(ctx)
	if err != nil {
		return nil, err
	}
	if body, err := tryOnce(header); err == nil {
		return body, nil
	}

	// refresh and retry once
	header, err = d.refreshHeaders(ctx)
	if err != nil {
		return nil, err
	}
	return tryOnce(header)
}

type rangeReaderFunc func(ctx context.Context, hr http_range.Range) (io.ReadCloser, error)

func (f rangeReaderFunc) RangeRead(ctx context.Context, hr http_range.Range) (io.ReadCloser, error) {
	return f(ctx, hr)
}

func cloneHeader(header http.Header) http.Header {
	if header == nil {
		return nil
	}
	return header.Clone()
}

func (d *OnedriveSharelink) headerSnapshot() http.Header {
	d.headerMu.RLock()
	defer d.headerMu.RUnlock()
	return cloneHeader(d.Headers)
}

func (d *OnedriveSharelink) storeHeaders(header http.Header) {
	if header == nil {
		return
	}
	d.headerMu.Lock()
	d.Headers = header
	d.HeaderTime = time.Now().Unix()
	d.headerMu.Unlock()
}

func (d *OnedriveSharelink) headersExpired() bool {
	d.headerMu.RLock()
	defer d.headerMu.RUnlock()
	return time.Since(time.Unix(d.HeaderTime, 0)) > headerTTL
}

func (d *OnedriveSharelink) refreshHeaders(ctx context.Context) (http.Header, error) {
	header, err, _ := d.sg.Do("refresh", func() (http.Header, error) {
		h, e := d.getHeaders(ctx)
		if e != nil {
			return nil, e
		}
		d.storeHeaders(h)
		return h, nil
	})
	return header, err
}

func (d *OnedriveSharelink) getValidHeaders(ctx context.Context) (http.Header, error) {
	if h := d.headerSnapshot(); h != nil && !d.headersExpired() {
		return h, nil
	}
	h, err := d.refreshHeaders(ctx)
	if err != nil {
		if h2 := d.headerSnapshot(); h2 != nil {
			log.Warnf("onedrive_sharelink: use cached headers after refresh failure: %+v", err)
			return h2, nil
		}
		return nil, err
	}
	return h, nil
}
