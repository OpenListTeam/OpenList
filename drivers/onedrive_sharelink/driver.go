package onedrive_sharelink

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	stdpath "path"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const (
	headerTTL          = 25 * time.Minute
	driveTokenTTL      = 20 * time.Minute
	directLinkTTL      = 20 * time.Minute
	simpleUploadLimit  = 250 * 1024 * 1024
	uploadSessionChunk = 10 * 1024 * 1024
)

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
	folderSizes, err := d.driveChildrenFolderSizes(ctx, dir.GetPath())
	if err != nil {
		log.Warnf("onedrive_sharelink: failed to get folder sizes for %s: %+v", dir.GetPath(), err)
	}

	// Convert the slice of files to the required model.Obj format
	return utils.SliceConvert(files, func(src Item) (model.Obj, error) {
		obj := fileToObj(src)
		if size, ok := folderSizes[obj.GetName()]; ok {
			obj.Size = size
		}
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

	if args.Redirect {
		directURL, err := d.resolveDirectDownloadURL(ctx, file, url, header)
		if err != nil {
			return nil, err
		}
		expiration := directLinkTTL
		return &model.Link{
			URL:        directURL,
			Expiration: &expiration,
		}, nil
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
	token, err := d.getValidDriveAccessToken(ctx)
	if err != nil {
		return err
	}
	apiURL := injectAccessToken(d.drivePathAPIURL(parentDir.GetPath())+"/children", token)
	body := map[string]any{
		"name":                              dirName,
		"folder":                            map[string]any{},
		"@microsoft.graph.conflictBehavior": "fail",
	}
	resp, err := d.doJSON(ctx, http.MethodPost, apiURL, body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return nil
	case http.StatusConflict:
		return errs.ObjectAlreadyExists
	default:
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to create folder, status code: %d, body: %s", resp.StatusCode, string(data))
	}
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
	info, err := d.createUploadInfo(ctx, stdpath.Join(dstDir.GetPath(), stream.GetName()), stream.GetSize())
	if err != nil {
		return err
	}
	if info.ChunkSize == 0 {
		return d.uploadContent(ctx, info.UploadURL, stream, up)
	}
	return d.uploadToSession(ctx, info.UploadURL, stream, up)
}

func (d *OnedriveSharelink) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	if d.DisableDiskUsage {
		return nil, errs.NotImplement
	}
	size, err := d.driveItemSize(ctx, "/")
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: size,
			UsedSpace:  size,
		},
	}, nil
}

func (d *OnedriveSharelink) driveItemSize(ctx context.Context, path string) (int64, error) {
	token, err := d.getValidDriveAccessToken(ctx)
	if err != nil {
		return 0, err
	}
	apiURL := injectAccessToken(d.drivePathAPIURL(path), token)
	resp, err := d.doJSON(ctx, http.MethodGet, apiURL, nil)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("failed to get folder details, status code: %d, body: %s", resp.StatusCode, string(data))
	}
	var item struct {
		Size int64 `json:"size"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&item); err != nil {
		return 0, err
	}
	return item.Size, nil
}

func (d *OnedriveSharelink) driveChildrenFolderSizes(ctx context.Context, path string) (map[string]int64, error) {
	token, err := d.getValidDriveAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	rawURL := d.drivePathAPIURL(path) + "/children?$select=name,size,folder"
	sizes := make(map[string]int64)
	for rawURL != "" {
		resp, err := d.doJSON(ctx, http.MethodGet, injectAccessToken(rawURL, token), nil)
		if err != nil {
			return nil, err
		}
		var data struct {
			Value []struct {
				Name   string `json:"name"`
				Size   int64  `json:"size"`
				Folder any    `json:"folder"`
			} `json:"value"`
			NextLink string `json:"@odata.nextLink"`
		}
		func() {
			defer resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				err = fmt.Errorf("failed to get children details, status code: %d, body: %s", resp.StatusCode, string(body))
				return
			}
			err = json.NewDecoder(resp.Body).Decode(&data)
		}()
		if err != nil {
			return nil, err
		}
		for _, item := range data.Value {
			if item.Folder != nil {
				sizes[item.Name] = item.Size
			}
		}
		rawURL = data.NextLink
	}
	return sizes, nil
}

func (d *OnedriveSharelink) GetDirectUploadTools() []string {
	if !d.EnableDirectUpload {
		return nil
	}
	return []string{"HttpDirect"}
}

func (d *OnedriveSharelink) GetDirectUploadInfo(ctx context.Context, tool string, dstDir model.Obj, fileName string, fileSize int64) (any, error) {
	if !d.EnableDirectUpload {
		return nil, errs.NotImplement
	}
	if tool != "HttpDirect" {
		return nil, errs.NotImplement
	}
	return d.createUploadInfo(ctx, stdpath.Join(dstDir.GetPath(), fileName), fileSize)
}

func (d *OnedriveSharelink) createUploadInfo(ctx context.Context, path string, fileSize int64) (*model.HttpDirectUploadInfo, error) {
	token, err := d.getValidDriveAccessToken(ctx)
	if err != nil {
		return nil, err
	}
	if fileSize >= 0 && fileSize <= simpleUploadLimit {
		return &model.HttpDirectUploadInfo{
			UploadURL: injectAccessToken(d.drivePathAPIURL(path)+"/content", token),
			Method:    http.MethodPut,
		}, nil
	}
	apiURL := injectAccessToken(d.drivePathAPIURL(path)+"/createUploadSession", token)
	body := map[string]any{
		"item": map[string]any{
			"@microsoft.graph.conflictBehavior": "rename",
		},
	}
	resp, err := d.doJSON(ctx, http.MethodPost, apiURL, body)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("failed to create upload session, status code: %d, body: %s", resp.StatusCode, string(data))
	}
	var data uploadSessionResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, err
	}
	if data.UploadURL == "" {
		return nil, fmt.Errorf("failed to get upload URL from response")
	}
	return &model.HttpDirectUploadInfo{
		UploadURL: data.UploadURL,
		ChunkSize: uploadSessionChunk,
		Method:    http.MethodPut,
	}, nil
}

func (d *OnedriveSharelink) uploadContent(ctx context.Context, uploadURL string, file model.FileStreamer, up driver.UpdateProgress) error {
	if up == nil {
		up = func(float64) {}
	}
	reader := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader: &driver.SimpleReaderWithSize{
			Reader: file,
			Size:   file.GetSize(),
		},
		UpdateProgress: up,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, reader)
	if err != nil {
		return err
	}
	req.ContentLength = file.GetSize()
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		data, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("failed to upload content, status code: %d, body: %s", resp.StatusCode, string(data))
	}
	return nil
}

func (d *OnedriveSharelink) uploadToSession(ctx context.Context, uploadURL string, file model.FileStreamer, up driver.UpdateProgress) error {
	if up == nil {
		up = func(float64) {}
	}
	if file.GetSize() <= 0 {
		return d.uploadSessionChunk(ctx, uploadURL, file, 0, 0, file.GetSize())
	}
	ss, err := streamPkg.NewStreamSectionReader(file, uploadSessionChunk, &up)
	if err != nil {
		return err
	}
	var finish int64
	for finish < file.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		left := file.GetSize() - finish
		byteSize := min(left, int64(uploadSessionChunk))
		rd, err := ss.GetSectionReader(finish, byteSize)
		if err != nil {
			return err
		}
		err = retry.Do(
			func() error {
				if _, err := rd.Seek(0, io.SeekStart); err != nil {
					return err
				}
				return d.uploadSessionChunk(ctx, uploadURL, rd, finish, byteSize, file.GetSize())
			},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second),
		)
		ss.FreeSectionReader(rd)
		if err != nil {
			return err
		}
		finish += byteSize
		up(float64(finish) * 100 / float64(file.GetSize()))
	}
	return nil
}

func (d *OnedriveSharelink) uploadSessionChunk(ctx context.Context, uploadURL string, reader io.Reader, start, size, total int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, driver.NewLimitedUploadStream(ctx, reader))
	if err != nil {
		return err
	}
	req.ContentLength = size
	if total > 0 {
		req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, start+size-1, total))
	}
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode >= 500 && resp.StatusCode <= 504:
		return fmt.Errorf("server error: %d", resp.StatusCode)
	case resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusAccepted:
		data, _ := io.ReadAll(resp.Body)
		return errors.New(string(data))
	default:
		return nil
	}
}

func (d *OnedriveSharelink) drivePathAPIURL(path string) string {
	drivePath := stdpath.Join(d.driveRootPath, path)
	drivePath = utils.FixAndCleanPath(drivePath)
	if drivePath == "/" {
		return d.DriveURL + "/root"
	}
	return fmt.Sprintf("%s/root:%s:", d.DriveURL, utils.EncodePath(drivePath, true))
}

func injectAccessToken(rawURL, token string) string {
	if token == "" {
		return rawURL
	}
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	values := u.Query()
	if strings.HasPrefix(token, "access_token=") {
		values.Set("access_token", strings.TrimPrefix(token, "access_token="))
	} else {
		values.Set("access_token", token)
	}
	u.RawQuery = values.Encode()
	return u.String()
}

func (d *OnedriveSharelink) doJSON(ctx context.Context, method, rawURL string, body any) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json;odata.metadata=minimal")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return base.HttpClient.Do(req)
}

func (d *OnedriveSharelink) getValidDriveAccessToken(ctx context.Context) (string, error) {
	d.headerMu.RLock()
	token := d.DriveAccessToken
	expired := time.Since(time.Unix(d.DriveTokenTime, 0)) > driveTokenTTL
	d.headerMu.RUnlock()
	if token != "" && !expired {
		return token, nil
	}
	if err := d.refreshDriveContext(ctx); err != nil {
		d.headerMu.RLock()
		token = d.DriveAccessToken
		d.headerMu.RUnlock()
		if token != "" {
			log.Warnf("onedrive_sharelink: use cached drive access token after refresh failure: %+v", err)
			return token, nil
		}
		return "", err
	}
	d.headerMu.RLock()
	defer d.headerMu.RUnlock()
	return d.DriveAccessToken, nil
}

func (d *OnedriveSharelink) refreshDriveContext(ctx context.Context) error {
	header, err := d.getValidHeaders(ctx)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.ShareLinkURL, nil)
	if err != nil {
		return err
	}
	req.Header = cloneHeader(header)
	resp, err := NewNoRedirectCLient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	redirectURL := resp.Header.Get("Location")
	if redirectURL == "" && resp.Request != nil && resp.Request.URL != nil {
		redirectURL = resp.Request.URL.String()
	}
	if redirectURL == "" {
		return fmt.Errorf("share link did not return redirect URL")
	}
	return d.refreshDriveContextFromRedirect(ctx, redirectURL, header)
}

func (d *OnedriveSharelink) refreshDriveContextFromRedirect(ctx context.Context, redirectURL string, header http.Header) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, redirectURL, nil)
	if err != nil {
		return err
	}
	req.Header = cloneHeader(header)
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("onedrive page request failed, status code: %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	ctxInfo, err := parsePageContext(body)
	if err != nil {
		return err
	}
	rootPath, err := driveRootPathFromRedirect(redirectURL, ctxInfo.ListURL)
	if err != nil {
		return err
	}
	d.headerMu.Lock()
	d.DriveURL = ctxInfo.DriveInfo.DriveURL
	d.DriveAccessToken = ctxInfo.DriveInfo.DriveAccessToken
	d.DriveTokenTime = time.Now().Unix()
	d.driveRootPath = rootPath
	d.headerMu.Unlock()
	return nil
}

func driveRootPathFromRedirect(redirectURL, listURL string) (string, error) {
	u, err := url.Parse(redirectURL)
	if err != nil {
		return "", err
	}
	id := u.Query().Get("id")
	if id == "" {
		return "/", nil
	}
	if listURL == "" {
		return "/", nil
	}
	if id == listURL {
		return "/", nil
	}
	prefix := strings.TrimRight(listURL, "/") + "/"
	if strings.HasPrefix(id, prefix) {
		return utils.FixAndCleanPath(strings.TrimPrefix(id, strings.TrimRight(listURL, "/"))), nil
	}
	return "/", nil
}

var pageContextRE = regexp.MustCompile(`(?s)var _spPageContextInfo=(\{.*?\});_spPageContextInfo`)

func parsePageContext(body []byte) (*pageContextInfo, error) {
	match := pageContextRE.FindSubmatch(body)
	if len(match) < 2 {
		return nil, fmt.Errorf("failed to find _spPageContextInfo")
	}
	var info pageContextInfo
	if err := json.Unmarshal(match[1], &info); err != nil {
		return nil, err
	}
	if info.DriveInfo.DriveURL == "" {
		return nil, fmt.Errorf("failed to get drive URL from page context")
	}
	if info.DriveInfo.DriveAccessToken == "" {
		return nil, fmt.Errorf("failed to get drive access token from page context")
	}
	return &info, nil
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

func (d *OnedriveSharelink) resolveDirectDownloadURL(ctx context.Context, file model.Obj, rawURL string, header http.Header) (string, error) {
	var errs []error
	if obj, ok := unwrapObject(file); ok {
		if obj.SPItemURL != "" {
			directURL, err := d.resolveSPItemDownloadURL(ctx, obj.SPItemURL, header)
			if err == nil {
				return directURL, nil
			}
			errs = append(errs, err)
		}
		if obj.ContentDownloadURL != "" {
			return obj.ContentDownloadURL, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	req.Header = cloneHeader(header)
	if req.Header == nil {
		req.Header = http.Header{}
	}

	resp, err := NewNoRedirectCLient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if location == "" {
		errs = append(errs, fmt.Errorf("download.aspx returned no redirect location, status code: %d", resp.StatusCode))
		return "", fmt.Errorf("onedrive_sharelink: direct download URL unavailable: %v", errs)
	}
	u, err := req.URL.Parse(location)
	if err != nil {
		return "", err
	}
	return u.String(), nil
}

type spItemDownloadResp struct {
	ContentDownloadURL string `json:"@content.downloadUrl"`
}

func unwrapObject(obj model.Obj) (*Object, bool) {
	for {
		switch o := obj.(type) {
		case *Object:
			return o, true
		case model.ObjUnwrap:
			obj = o.Unwrap()
		default:
			return nil, false
		}
	}
}

func (d *OnedriveSharelink) resolveSPItemDownloadURL(ctx context.Context, spItemURL string, header http.Header) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, spItemURL, nil)
	if err != nil {
		return "", err
	}
	req.Header = cloneHeader(header)
	if req.Header == nil {
		req.Header = http.Header{}
	}
	req.Header.Set("Accept", "application/json;odata.metadata=minimal")

	resp, err := NewNoRedirectCLient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("sp item metadata request failed, status code: %d", resp.StatusCode)
	}

	var data spItemDownloadResp
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return "", err
	}
	if data.ContentDownloadURL == "" {
		return "", fmt.Errorf("sp item metadata response missing @content.downloadUrl")
	}
	return data.ContentDownloadURL, nil
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
