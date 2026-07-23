package huggingface

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	"github.com/pkg/errors"
)

// hfClient is a dedicated HTTP client for HF API requests,
// separate from base.HttpClient to avoid keep-alive connection reuse issues.
func hfClient() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Minute,
		Transport: &http.Transport{
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: false},
			DisableKeepAlives: true,
			MaxIdleConns:      0,
		},
	}
}

const lfsThreshold = 5 << 20

type HuggingFace struct {
	model.Storage
	Addition
	client *resty.Client
}

func (d *HuggingFace) Config() driver.Config {
	return config
}

func (d *HuggingFace) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *HuggingFace) Init(ctx context.Context) error {
	d.RootFolderPath = utils.FixAndCleanPath(d.RootFolderPath)
	if d.RepoID == "" {
		return errors.New("repo_id is required")
	}
	if d.RepoType == "" {
		d.RepoType = "model"
	}
	if d.Ref == "" {
		d.Ref = "main"
	}
	d.client = base.NewRestyClient().
		SetHeader("Accept", "application/json").
		SetDebug(false)
	return nil
}

func (d *HuggingFace) Drop(ctx context.Context) error {
	return nil
}

func (d *HuggingFace) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	path := dir.GetPath()
	res, err := d.request().Get(d.apiURL(fmt.Sprintf("/tree/%s%s", d.Ref, path)))
	if err != nil {
		return nil, err
	}
	if res.StatusCode() != 200 {
		return nil, toHFError(res)
	}
	var entries []TreeEntry
	if err = utils.Json.Unmarshal(res.Body(), &entries); err != nil {
		return nil, err
	}
	objs := make([]model.Obj, 0, len(entries))
	for _, entry := range entries {
		objs = append(objs, entry.toModelObj())
	}
	return objs, nil
}

func (d *HuggingFace) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	url := d.resolveURL(d.Ref, file.GetPath())
	if proxy := strings.TrimSpace(d.Addition.HFProxy); proxy != "" {
		url = strings.Replace(url, hfAPIBase, proxy, 1)
	}
	return &model.Link{
		URL: url,
	}, nil
}

func (d *HuggingFace) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	filePath := relativePath(stdpath.Join(dstDir.GetPath(), stream.GetName()))
	fileName := stream.GetName()

	tmp, size, sha256Hex, err := d.saveStream(ctx, stream, up)
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()

	sample, err := d.readSample(tmp)
	if err != nil {
		return err
	}

	entries, err := d.preupload([]PreuploadFile{{
		Path: filePath, Size: size, Sample: sample,
	}})
	if err != nil {
		return err
	}

	isLFS := size >= lfsThreshold
	if len(entries) > 0 && entries[0].UploadMode == "lfs" {
		isLFS = true
	}

	if isLFS {
		return d.lfsUploadAndCommit(ctx, tmp, filePath, fileName, sha256Hex, size, up)
	}
	return d.streamCommit(ctx, tmp, filePath, fileName, size, up)
}

// saveStream writes stream to a temp file, returning the file, its size and sha256 hex.
func (d *HuggingFace) saveStream(ctx context.Context, stream model.FileStreamer, up driver.UpdateProgress) (*os.File, int64, string, error) {
	tmp, err := os.CreateTemp("", "hf-upload-*")
	if err != nil {
		return nil, 0, "", err
	}
	hasher := sha256.New()
	writer := io.MultiWriter(tmp, hasher)
	if err = utils.CopyWithCtx(ctx, writer, stream, 0, up); err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, 0, "", err
	}
	fi, err := tmp.Stat()
	if err != nil {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, 0, "", err
	}
	expectedSize := stream.GetSize()
	if expectedSize > 0 && fi.Size() != expectedSize {
		tmp.Close()
		os.Remove(tmp.Name())
		return nil, 0, "", fmt.Errorf("saveStream: wrote %d bytes to temp file but stream.GetSize()=%d", fi.Size(), expectedSize)
	}
	size := fi.Size()
	return tmp, size, fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

// streamCommit streams file content as base64 via NDJSON commit API.
func (d *HuggingFace) streamCommit(ctx context.Context, tmp *os.File, filePath, fileName string, size int64, up driver.UpdateProgress) error {
	headerLine := fmt.Sprintf(`{"key":"header","value":{"summary":"Upload %s"}}`, fileName) + "\n"
	filePrefix := fmt.Sprintf(`{"key":"file","value":{"path":"%s","content":"`, filePath)
	fileSuffix := `","encoding":"base64"}}` + "\n"

	b64Len := calculateBase64Length(size)
	contentLength := int64(len(headerLine)+len(filePrefix)) + b64Len + int64(len(fileSuffix))

	pr, pw := io.Pipe()
	go func() {
		if _, err := tmp.Seek(0, io.SeekStart); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		encoder := base64.NewEncoder(base64.StdEncoding, pw)
		if _, err := io.Copy(encoder, tmp); err != nil {
			_ = pw.CloseWithError(err)
			return
		}
		_ = encoder.Close()
		_ = pw.Close()
	}()

	body := io.MultiReader(
		strings.NewReader(headerLine),
		strings.NewReader(filePrefix),
		pr,
		strings.NewReader(fileSuffix),
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.apiURL(fmt.Sprintf("/commit/%s", d.Ref)),
		driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
			Reader: &driver.SimpleReaderWithSize{Reader: body, Size: contentLength},
			UpdateProgress: up,
		}))
	if err != nil {
		return fmt.Errorf("stream commit request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if d.ApiToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.ApiToken)
	}
	req.ContentLength = contentLength

	res, err := hfClient().Do(req)
	if err != nil {
		return fmt.Errorf("stream commit request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(res.Body)
		return fmt.Errorf("commit failed: %s - %s", res.Status, string(bodyBytes))
	}
	return nil
}

// lfsUploadAndCommit handles LFS file upload and NDJSON commit with lfsFile reference.
func (d *HuggingFace) lfsUploadAndCommit(ctx context.Context, tmp *os.File, filePath, fileName, sha256Hex string, size int64, up driver.UpdateProgress) error {
	batchURL := fmt.Sprintf("%s/%s.git/info/lfs/objects/batch", d.repoBase(), d.RepoID)
	batchReq := LFSBatchRequest{
		Operation: "upload", Transfers: []string{"basic"},
		HashAlgo: "sha256", Ref: LFSRef{Name: d.Ref},
		Objects: []LFSObject{{OID: sha256Hex, Size: size}},
	}
	lfsReq := d.client.R().
		SetHeader("Accept", "application/vnd.git-lfs+json").
		SetHeader("Content-Type", "application/vnd.git-lfs+json")
	if d.ApiToken != "" {
		lfsReq.SetHeader("Authorization", "Bearer "+d.ApiToken)
	}
	res, err := lfsReq.SetBody(batchReq).Post(batchURL)
	if err != nil {
		return err
	}
	if res.StatusCode() != 200 {
		return toHFError(res)
	}
	var batchResp LFSBatchResponse
	if err = utils.Json.Unmarshal(res.Body(), &batchResp); err != nil {
		return err
	}
	if len(batchResp.Objects) == 0 {
		return errors.New("lfs batch returned empty objects")
	}
	obj := batchResp.Objects[0]
	if obj.Actions == nil {
		return d.doCommitLFS(ctx, filePath, sha256Hex, size)
	}
	uploadAction, ok := obj.Actions["upload"]
	if !ok {
		return d.doCommitLFS(ctx, filePath, sha256Hex, size)
	}

	// Verify temp file has the expected content before uploading to S3.
	fi, statErr := tmp.Stat()
	if statErr != nil {
		return fmt.Errorf("failed to stat temp file before s3 upload: %w", statErr)
	}
	if fi.Size() != size {
		return fmt.Errorf("temp file size %d does not match expected size %d: stream may have returned 0 bytes", fi.Size(), size)
	}
	if err = d.streamUpload(ctx, tmp, uploadAction, size, up); err != nil {
		return err
	}

	if verifyAction, ok := obj.Actions["verify"]; ok {
		verifyBody := map[string]interface{}{"oid": sha256Hex, "size": size}
		verifyReq := d.client.R().SetBody(verifyBody)
		for k, v := range verifyAction.Header {
			verifyReq.SetHeader(k, fmt.Sprint(v))
		}
		verifyRes, err := verifyReq.Post(verifyAction.Href)
		if err != nil {
			return err
		}
		if verifyRes.StatusCode() > 299 {
			return fmt.Errorf("lfs verify failed: %s", verifyRes.Status())
		}
	}

	return d.doCommitLFS(ctx, filePath, sha256Hex, size)
}

var s3HTTPClient *http.Client
var s3HTTPClientOnce sync.Once

// s3Client returns a shared HTTP client for S3 LFS uploads.
func s3Client() *http.Client {
	s3HTTPClientOnce.Do(func() {
		s3HTTPClient = &http.Client{
			Timeout: 30 * time.Minute,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{
					InsecureSkipVerify: false,
					MinVersion:         tls.VersionTLS12,
				},
				DialContext: (&net.Dialer{
					Timeout:   30 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				ForceAttemptHTTP2:     false,
				MaxIdleConns:          2,
				MaxIdleConnsPerHost:   2,
				IdleConnTimeout:       90 * time.Second,
				ResponseHeaderTimeout: 5 * time.Minute,
				ExpectContinueTimeout: 5 * time.Second,
			},
		}
	})
	return s3HTTPClient
}

// streamUpload streams file content to the LFS upload URL.
func (d *HuggingFace) streamUpload(ctx context.Context, tmp *os.File, action LFSAction, size int64, up driver.UpdateProgress) error {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	// Use WithoutCancel for the body so the S3 upload completes even if ctx is canceled.
	uploadCtx := context.WithoutCancel(ctx)
	req, err := http.NewRequestWithContext(uploadCtx, http.MethodPut, action.Href,
		driver.NewLimitedUploadStream(uploadCtx, &driver.ReaderUpdatingProgress{
			Reader: &driver.SimpleReaderWithSize{Reader: tmp, Size: size},
			UpdateProgress: up,
		}))
	if err != nil {
		return fmt.Errorf("s3 upload request build: %w", err)
	}
	for k, v := range action.Header {
		// Skip x-amz-* headers that were not signed (X-Amz-SignedHeaders=host)
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") {
			continue
		}
		req.Header.Set(k, fmt.Sprint(v))
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.ContentLength = size

	res, err := s3Client().Do(req)
	if err != nil {
		return fmt.Errorf("s3 upload request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode == 501 {
		return d.streamUploadPost(ctx, tmp, action, size, up)
	}
	if res.StatusCode > 299 {
		bodyBytes, _ := io.ReadAll(res.Body)
		return fmt.Errorf("lfs upload failed: %s - %s", res.Status, string(bodyBytes))
	}
	return nil
}

// streamUploadPost retries upload with POST when PUT returns 501.
func (d *HuggingFace) streamUploadPost(ctx context.Context, tmp *os.File, action LFSAction, size int64, up driver.UpdateProgress) error {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return err
	}
	uploadCtx := context.WithoutCancel(ctx)
	req, err := http.NewRequestWithContext(uploadCtx, http.MethodPost, action.Href,
		driver.NewLimitedUploadStream(uploadCtx, &driver.ReaderUpdatingProgress{
			Reader: &driver.SimpleReaderWithSize{Reader: tmp, Size: size},
			UpdateProgress: up,
		}))
	if err != nil {
		return fmt.Errorf("s3 upload post request build: %w", err)
	}
	for k, v := range action.Header {
		if strings.HasPrefix(strings.ToLower(k), "x-amz-") {
			continue
		}
		req.Header.Set(k, fmt.Sprint(v))
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	req.ContentLength = size

	res, err := s3Client().Do(req)
	if err != nil {
		return fmt.Errorf("s3 upload post request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode > 299 {
		bodyBytes, _ := io.ReadAll(res.Body)
		return fmt.Errorf("lfs upload failed: %s - %s", res.Status, string(bodyBytes))
	}
	return nil
}

// doCommitLFS sends a NDJSON commit with a lfsFile reference.
func (d *HuggingFace) doCommitLFS(ctx context.Context, filePath, sha256Hex string, size int64) error {
	body := fmt.Sprintf(`{"key":"header","value":{"summary":"Upload %s"}}`+"\n"+`{"key":"lfsFile","value":{"path":"%s","algo":"sha256","oid":"%s","size":%d}}`+"\n",
		stdpath.Base(filePath), filePath, sha256Hex, size)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		d.apiURL(fmt.Sprintf("/commit/%s", d.Ref)),
		strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("commit request build: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-ndjson")
	if d.ApiToken != "" {
		req.Header.Set("Authorization", "Bearer "+d.ApiToken)
	}
	req.ContentLength = int64(len(body))

	res, err := hfClient().Do(req)
	if err != nil {
		return fmt.Errorf("commit request: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		bodyBytes, _ := io.ReadAll(res.Body)
		return fmt.Errorf("commit failed: %s - %s", res.Status, string(bodyBytes))
	}
	return nil
}

func (d *HuggingFace) preupload(files []PreuploadFile) ([]PreuploadResponseEntry, error) {
	body := map[string]interface{}{"files": files}
	res, err := d.request().SetBody(body).Post(d.apiURL(fmt.Sprintf("/preupload/%s", d.Ref)))
	if err != nil {
		return nil, err
	}
	if res.StatusCode() != 200 {
		return nil, toHFError(res)
	}
	var resp PreuploadResponse
	if err = utils.Json.Unmarshal(res.Body(), &resp); err != nil {
		return nil, err
	}
	return resp.Files, nil
}

func (d *HuggingFace) repoBase() string {
	if d.RepoType == "model" {
		return hfAPIBase
	}
	return hfAPIBase + "/" + apiRepoType(d.RepoType)
}

func (d *HuggingFace) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	path := relativePath(stdpath.Join(parentDir.GetPath(), dirName) + "/.gitkeep")
	body := map[string]interface{}{
		"summary": fmt.Sprintf("Create directory %s", dirName),
		"files": []map[string]interface{}{
			{"path": path, "content": ""},
		},
	}
	res, err := d.request().SetBody(body).Post(d.apiURL(fmt.Sprintf("/commit/%s", d.Ref)))
	if err != nil {
		return err
	}
	if res.StatusCode() != 200 {
		return toHFError(res)
	}
	return nil
}

func (d *HuggingFace) Remove(ctx context.Context, obj model.Obj) error {
	body := map[string]interface{}{
		"summary": fmt.Sprintf("Delete %s", obj.GetName()),
		"deletedEntries": []map[string]interface{}{
			{"path": relativePath(obj.GetPath())},
		},
	}
	res, err := d.request().SetBody(body).Post(d.apiURL(fmt.Sprintf("/commit/%s", d.Ref)))
	if err != nil {
		return err
	}
	if res.StatusCode() != 200 {
		return toHFError(res)
	}
	return nil
}

func (d *HuggingFace) readSample(tmp *os.File) (string, error) {
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	buf := make([]byte, 512)
	n, err := tmp.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf[:n]), nil
}

func calculateBase64Length(inputLength int64) int64 {
	return 4 * ((inputLength + 2) / 3)
}

var _ driver.Driver = (*HuggingFace)(nil)
