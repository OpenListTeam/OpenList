package wps

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type countingWriter struct {
	n *int64
}

func (w countingWriter) Write(p []byte) (int, error) {
	*w.n += int64(len(p))
	return len(p), nil
}

func cacheAndHash(file model.FileStreamer, up driver.UpdateProgress) (model.File, int64, string, string, error) {
	h1 := sha1.New()
	h256 := sha256.New()
	size := file.GetSize()
	var counted int64
	ws := []io.Writer{h1, h256}
	if size <= 0 {
		ws = append(ws, countingWriter{n: &counted})
	}
	p := up
	f, err := file.CacheFullAndWriter(&p, io.MultiWriter(ws...))
	if err != nil {
		return nil, 0, "", "", err
	}
	if size <= 0 {
		size = counted
	}
	return f, size, hex.EncodeToString(h1.Sum(nil)), hex.EncodeToString(h256.Sum(nil)), nil
}

func (d *Wps) createUpload(ctx context.Context, groupID, parentID int64, name string, size int64, sha1Hex, sha256Hex string) (*uploadCreateUpdateResp, error) {
	body := map[string]string{
		"group_id":  strconv.FormatInt(groupID, 10),
		"name":      name,
		"parent_id": strconv.FormatInt(parentID, 10),
		"sha1":      sha1Hex,
		"sha256":    sha256Hex,
		"size":      strconv.FormatInt(size, 10),
	}
	var resp uploadCreateUpdateResp
	r, err := d.jsonRequest(ctx).
		SetBody(body).
		SetResult(&resp).
		SetError(&resp).
		Put(d.driveURL("/api/v5/files/upload/create_update"))
	if err != nil {
		return nil, err
	}
	if err := checkAPI(r, resp.apiResult); err != nil {
		return nil, err
	}
	if resp.URL == "" {
		return nil, fmt.Errorf("empty upload url")
	}
	return &resp, nil
}

func normalizeETag(v string) string {
	v = strings.TrimSpace(v)
	if strings.HasPrefix(v, "W/") {
		v = strings.TrimSpace(strings.TrimPrefix(v, "W/"))
	}
	return strings.Trim(v, `"`)
}

func (d *Wps) commitUpload(ctx context.Context, etag, key string, groupID, parentID int64, name, sha1Hex string, size int64, store string) error {
	store = strings.TrimSpace(store)
	if store == "" {
		store = "ks3"
	}
	storeKey := ""
	if key != "" {
		storeKey = key
	}
	body := map[string]interface{}{
		"etag":     etag,
		"groupid":  groupID,
		"key":      key,
		"name":     name,
		"parentid": parentID,
		"sha1":     sha1Hex,
		"size":     size,
		"store":    store,
		"storekey": storeKey,
	}
	return d.doJSON(ctx, http.MethodPost, d.driveURL("/api/v5/files/file"), body)
}

func (d *Wps) put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	if dstDir == nil || file == nil {
		return errs.NotSupport
	}
	if up == nil {
		up = func(float64) {}
	}
	node, err := unwrapWpsObj(dstDir)
	if err != nil {
		return err
	}
	if node.Kind != "group" && node.Kind != "folder" {
		return errs.NotSupport
	}
	parentID := int64(0)
	if node.HasFile && node.Kind == "folder" {
		parentID = node.FileID
	}
	f, size, sha1Hex, sha256Hex, err := cacheAndHash(file, func(float64) {})
	if err != nil {
		return err
	}
	if c, ok := f.(io.Closer); ok {
		defer c.Close()
	}

	// 在隐藏文件名前加_上传，这是WPS的限制，无法上传隐藏文件，也无法将任何文件重命名为隐藏文件，所有隐藏文件会被自动加上_ 上传
	// 甚至可以上传前缀是..的文件，但是单个点就是不行
	realName := file.GetName()
	uploadName := realName
	if strings.HasPrefix(realName, ".") {
		uploadName = "_" + realName
	}

	info, err := d.createUpload(ctx, node.GroupID, parentID, uploadName, size, sha1Hex, sha256Hex)
	if err != nil {
		return err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	rf := driver.NewLimitedUploadFile(ctx, f)
	prog := driver.NewProgress(size, model.UpdateProgressWithRange(up, 0, 1))

	method := strings.ToUpper(strings.TrimSpace(info.Method))
	if method == "" {
		method = http.MethodPut
	}

	var req *http.Request
	if method == http.MethodPost && len(info.Request.FormData) > 0 {
		if size == 0 {
			var buf bytes.Buffer
			mw := multipart.NewWriter(&buf)
			for k, v := range info.Request.FormData {
				if err := mw.WriteField(k, v); err != nil {
					return err
				}
			}
			part, err := mw.CreateFormFile("file", uploadName)
			if err != nil {
				return err
			}
			if _, err := io.Copy(part, io.TeeReader(rf, prog)); err != nil {
				return err
			}
			if err := mw.Close(); err != nil {
				return err
			}
			req, err = http.NewRequestWithContext(ctx, method, info.URL, bytes.NewReader(buf.Bytes()))
			if err != nil {
				return err
			}
			for k, v := range info.Request.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
			req.ContentLength = int64(buf.Len())
			req.Header.Set("Content-Length", strconv.FormatInt(req.ContentLength, 10))
		} else {
			pr, pw := io.Pipe()
			mw := multipart.NewWriter(pw)
			req, err = http.NewRequestWithContext(ctx, method, info.URL, pr)
			if err != nil {
				return err
			}
			for k, v := range info.Request.Headers {
				req.Header.Set(k, v)
			}
			req.Header.Set("Content-Type", mw.FormDataContentType())
			go func() {
				for k, v := range info.Request.FormData {
					if err := mw.WriteField(k, v); err != nil {
						pw.CloseWithError(err)
						return
					}
				}
				part, err := mw.CreateFormFile("file", uploadName)
				if err != nil {
					pw.CloseWithError(err)
					return
				}
				if _, err := io.Copy(part, io.TeeReader(rf, prog)); err != nil {
					pw.CloseWithError(err)
					return
				}
				if err := mw.Close(); err != nil {
					pw.CloseWithError(err)
					return
				}
				pw.Close()
			}()
		}
	} else {
		var body = io.TeeReader(rf, prog)
		if size == 0 {
			body = bytes.NewReader(nil)
		}
		req, err = http.NewRequestWithContext(ctx, method, info.URL, body)
		if err != nil {
			return err
		}
		for k, v := range info.Request.Headers {
			req.Header.Set(k, v)
		}
		req.ContentLength = size
		req.Header.Set("Content-Length", strconv.FormatInt(size, 10))
	}

	c := *d.client.GetClient()
	c.Timeout = 0
	resp, err := (&c).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if !statusOK(resp.StatusCode, info.Response.ExpectCode) {
		io.Copy(io.Discard, resp.Body)
		return fmt.Errorf("http error: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	etag := normalizeETag(respArg(info.Response.ArgsETag, resp, body))
	if etag == "" {
		etag = normalizeETag(resp.Header.Get("ETag"))
	}

	key := strings.TrimSpace(respArg(info.Response.ArgsKey, resp, body))
	if key == "" {
		key = strings.TrimSpace(resp.Header.Get("x-obs-save-key"))
	}

	var pr uploadPutResp
	sha1FromServer := ""
	if err := json.Unmarshal(body, &pr); err == nil {
		sha1FromServer = strings.TrimSpace(pr.NewFilename)
		if sha1FromServer == "" {
			sha1FromServer = strings.TrimSpace(pr.Sha1)
		}
		if etag == "" && pr.MD5 != "" {
			etag = strings.TrimSpace(pr.MD5)
		}
	}

	if sha1FromServer == "" {
		if v := extractXMLTag(string(body), "ETag"); v != "" {
			sha1FromServer = v
			if etag == "" {
				etag = v
			}
		}
	}
	if sha1FromServer == "" && key != "" && len(key) == 40 {
		sha1FromServer = key
	}
	if sha1FromServer == "" {
		sha1FromServer = sha1Hex
	}

	if etag == "" {
		return fmt.Errorf("empty etag")
	}
	if sha1FromServer == "" {
		return fmt.Errorf("empty sha1")
	}

	store := strings.TrimSpace(info.Store)
	commitKey := ""
	if strings.TrimSpace(info.Response.ArgsKey) != "" {
		commitKey = key
		if commitKey == "" {
			commitKey = sha1FromServer
		}
	}

	if err := d.commitUpload(ctx, etag, commitKey, node.GroupID, parentID, uploadName, sha1FromServer, size, store); err != nil {
		return err
	}

	up(1)
	return nil
}
