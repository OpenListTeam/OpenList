package rakuten_drive

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/s3"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
)

type RakutenDrive struct {
	model.Storage
	Addition

	client *resty.Client

	accessToken       string
	accessTokenExpire time.Time
	tokenMu           sync.Mutex

	stsCreds       *filelinkTokenResp
	stsCredsDir    string
	stsCredsExpire time.Time
	stsMu          sync.Mutex
}

func (d *RakutenDrive) Config() driver.Config {
	return config
}

func (d *RakutenDrive) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *RakutenDrive) Init(ctx context.Context) error {
	if d.client == nil {
		d.client = newHTTPClient()
	}
	// drop credentials cached for a previous configuration
	d.tokenMu.Lock()
	d.accessToken = ""
	d.accessTokenExpire = time.Time{}
	d.tokenMu.Unlock()
	d.stsMu.Lock()
	d.stsCreds = nil
	d.stsMu.Unlock()
	return d.ensureAccessToken()
}

func (d *RakutenDrive) Drop(ctx context.Context) error {
	return nil
}

func (d *RakutenDrive) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	remoteDir := d.localPath2RemotePath(dir.GetPath(), true)
	pageSize := d.PageSize
	if pageSize <= 0 {
		pageSize = 200
	}
	from := 0
	files := make([]model.Obj, 0)
	for {
		body := base.Json{
			"host_id":        d.HostID,
			"path":           remoteDir,
			"from":           from,
			"to":             from + pageSize,
			"sort_type":      "path",
			"reverse":        false,
			"thumbnail_size": 130,
		}
		res, err := d.newForestRequest(ctx, http.MethodPost, forestBase+"/v1/files", body, nil)
		if err != nil {
			return nil, err
		}
		items, raw, err := d.parseList(res.Body(), remoteDir)
		if err != nil {
			return nil, err
		}
		if raw == 0 {
			break
		}
		files = append(files, items...)
		// compare the raw page size: parseList may skip unparsable entries
		if raw < pageSize {
			break
		}
		from += pageSize
	}
	return files, nil
}

func (d *RakutenDrive) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	remotePath := file.GetID()
	if remotePath == "" {
		remotePath = d.localPath2RemotePath(file.GetPath(), false)
	}
	dirPath := path.Dir(remotePath)
	if dirPath == "." {
		dirPath = ""
	} else if dirPath != "" && !strings.HasSuffix(dirPath, "/") {
		dirPath += "/"
	}
	body := base.Json{
		"host_id":     d.HostID,
		"path":        dirPath,
		"file":        []base.Json{{"path": remotePath, "size": file.GetSize()}},
		"app_version": d.AppVersion,
	}
	var resp downloadResp
	_, err := d.newForestRequest(ctx, http.MethodPost, forestBase+"/v1/filelink/download", body, &resp)
	if err != nil {
		return nil, err
	}
	if resp.URL == "" {
		return nil, fmt.Errorf("download url empty")
	}
	return &model.Link{URL: resp.URL}, nil
}

func (d *RakutenDrive) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	remoteDir := d.localPath2RemotePath(parentDir.GetPath(), true)
	body := base.Json{
		"host_id": d.HostID,
		"name":    dirName,
		"path":    remoteDir,
	}
	_, err := d.newForestRequest(ctx, http.MethodPost, forestBase+"/v1/files/create", body, nil)
	if err != nil {
		return nil, err
	}
	newPath := path.Join(parentDir.GetPath(), dirName)
	return &File{
		Object: model.Object{
			ID:       d.localPath2RemotePath(newPath, true),
			Path:     newPath,
			Name:     dirName,
			IsFolder: true,
			Modified: time.Now(),
		},
	}, nil
}

func (d *RakutenDrive) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	f, ok := d.getFile(srcObj)
	if !ok {
		return nil, fmt.Errorf("move requires File metadata")
	}
	srcRemote := f.GetID()
	if srcRemote == "" {
		srcRemote = d.localPath2RemotePath(srcObj.GetPath(), srcObj.IsDir())
	}
	prefix := d.normalizePrefix(srcRemote)
	dstRemote := d.localPath2RemotePath(dstDir.GetPath(), true)
	body := base.Json{
		"host_id":   d.HostID,
		"file":      []base.Json{{"path": srcRemote, "size": f.GetSize(), "version_id": f.VersionID, "last_modified": f.LastModified}},
		"target_id": d.HostID,
		"to_path":   dstRemote,
		"prefix":    prefix,
	}
	_, err := d.newForestRequest(ctx, http.MethodPut, forestBase+"/v3/files/move", body, nil)
	if err != nil {
		return nil, err
	}
	newPath := path.Join(dstDir.GetPath(), srcObj.GetName())
	f.Path = newPath
	f.ID = d.localPath2RemotePath(newPath, srcObj.IsDir())
	return f, nil
}

func (d *RakutenDrive) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	f, ok := d.getFile(srcObj)
	if !ok {
		return nil, fmt.Errorf("rename requires File metadata")
	}
	srcRemote := f.GetID()
	if srcRemote == "" {
		srcRemote = d.localPath2RemotePath(srcObj.GetPath(), srcObj.IsDir())
	}
	prefix := d.normalizePrefix(srcRemote)
	body := base.Json{
		"host_id": d.HostID,
		"name":    newName,
		"file":    base.Json{"path": srcRemote, "size": f.GetSize(), "version_id": f.VersionID, "last_modified": f.LastModified},
		"prefix":  prefix,
	}
	_, err := d.newForestRequest(ctx, http.MethodPut, forestBase+"/v3/files/rename", body, nil)
	if err != nil {
		return nil, err
	}
	newPath := path.Join(path.Dir(srcObj.GetPath()), newName)
	f.Path = newPath
	f.Name = newName
	f.ID = d.localPath2RemotePath(newPath, srcObj.IsDir())
	return f, nil
}

func (d *RakutenDrive) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	f, ok := d.getFile(srcObj)
	if !ok {
		return nil, fmt.Errorf("copy requires File metadata")
	}
	srcRemote := f.GetID()
	if srcRemote == "" {
		srcRemote = d.localPath2RemotePath(srcObj.GetPath(), srcObj.IsDir())
	}
	prefix := d.normalizePrefix(srcRemote)
	dstRemote := d.localPath2RemotePath(dstDir.GetPath(), true)
	body := base.Json{
		"host_id":   d.HostID,
		"file":      []base.Json{{"path": srcRemote, "size": f.GetSize(), "version_id": f.VersionID, "last_modified": f.LastModified}},
		"target_id": d.HostID,
		"to_path":   dstRemote,
		"prefix":    prefix,
	}
	var resp struct {
		Key string `json:"key"`
	}
	_, err := d.newForestRequest(ctx, http.MethodPost, forestBase+"/v3/files/copy", body, &resp)
	if err != nil {
		return nil, err
	}
	if resp.Key != "" {
		if err := d.waitCheckComplete(ctx, resp.Key); err != nil {
			return nil, err
		}
	}
	newPath := path.Join(dstDir.GetPath(), srcObj.GetName())
	return &File{
		Object: model.Object{
			ID:       d.localPath2RemotePath(newPath, srcObj.IsDir()),
			Path:     newPath,
			Name:     srcObj.GetName(),
			Size:     srcObj.GetSize(),
			IsFolder: srcObj.IsDir(),
			Modified: time.Now(),
		},
	}, nil
}

func (d *RakutenDrive) Remove(ctx context.Context, obj model.Obj) error {
	f, ok := d.getFile(obj)
	if !ok {
		return fmt.Errorf("remove requires File metadata")
	}
	remotePath := f.GetID()
	if remotePath == "" {
		remotePath = d.localPath2RemotePath(obj.GetPath(), obj.IsDir())
	}
	prefix := d.normalizePrefix(remotePath)
	body := base.Json{
		"host_id": d.HostID,
		"file":    []base.Json{{"path": remotePath, "size": f.GetSize(), "version_id": f.VersionID, "last_modified": f.LastModified}},
		"prefix":  prefix,
	}
	var resp struct {
		Key string `json:"key"`
	}
	_, err := d.newForestRequest(ctx, http.MethodDelete, forestBase+"/v3/files", body, &resp)
	if err != nil {
		return err
	}
	if resp.Key == "" {
		return nil
	}
	return d.waitCheckComplete(ctx, resp.Key)
}

func (d *RakutenDrive) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	remoteDir := d.localPath2RemotePath(dstDir.GetPath(), true)
	reader := io.Reader(file)
	size := file.GetSize()
	if size <= 0 {
		temp, err := file.CacheFullAndWriter(&up, nil)
		if err != nil {
			return nil, err
		}
		end, err := temp.Seek(0, io.SeekEnd)
		if err != nil {
			return nil, err
		}
		_, err = temp.Seek(0, io.SeekStart)
		if err != nil {
			return nil, err
		}
		size = end
		reader = temp
	}

	// 1) get temporary credentials (cached per directory)
	tokenResp, err := d.getSTSCredentials(ctx, remoteDir)
	if err != nil {
		return nil, err
	}

	// 2) init upload; replace when overwriting an existing object, matching
	// the overwrite semantics the op layer signals via the exist flag
	initBody := base.Json{
		"host_id":   d.HostID,
		"path":      remoteDir,
		"file":      []base.Json{{"path": file.GetName(), "size": size}},
		"upload_id": "",
		"replace":   file.GetExist() != nil,
	}
	var initResp uploadInitResp
	_, err = d.newForestRequest(ctx, http.MethodPost, forestBase+"/v1/check/upload", initBody, &initResp)
	if err != nil {
		return nil, err
	}
	if initResp.UploadID == "" || len(initResp.File) == 0 {
		return nil, fmt.Errorf("upload init failed")
	}
	objectKey := path.Join(strings.TrimSuffix(initResp.Prefix, "/"), strings.TrimPrefix(initResp.File[0].Path, "/"))

	// 3) upload parts to S3
	cfg := &aws.Config{
		Credentials: credentials.NewStaticCredentials(tokenResp.AccessKeyID, tokenResp.SecretAccessKey, tokenResp.SessionToken),
		Region:      aws.String(initResp.Region),
	}
	sess, err := session.NewSession(cfg)
	if err != nil {
		return nil, err
	}
	s3Client := s3.New(sess)

	if size == 0 {
		// S3 multipart upload requires at least one part; upload empty
		// objects with a single PUT instead
		putInput := &s3.PutObjectInput{
			Bucket: aws.String(initResp.Bucket),
			Key:    aws.String(objectKey),
			Body:   bytes.NewReader(nil),
		}
		if mt := file.GetMimetype(); mt != "" {
			putInput.ContentType = aws.String(mt)
		}
		if _, err = s3Client.PutObjectWithContext(ctx, putInput); err != nil {
			return nil, err
		}
	} else {
		createInput := &s3.CreateMultipartUploadInput{
			Bucket: aws.String(initResp.Bucket),
			Key:    aws.String(objectKey),
		}
		if mt := file.GetMimetype(); mt != "" {
			createInput.ContentType = aws.String(mt)
		}
		createResp, err := s3Client.CreateMultipartUploadWithContext(ctx, createInput)
		if err != nil {
			return nil, err
		}
		if createResp.UploadId == nil || *createResp.UploadId == "" {
			return nil, fmt.Errorf("s3 upload id empty")
		}
		s3UploadID := *createResp.UploadId

		partSize := d.UploadPartSize
		if partSize < 5*1024*1024 {
			partSize = 5 * 1024 * 1024
		}
		// bound the configured part size so a misconfigured value cannot
		// allocate an enormous in-memory buffer
		if partSize > 1024*1024*1024 {
			partSize = 1024 * 1024 * 1024
		}
		// scale the part size up when needed so the upload stays within
		// the S3 10,000-part limit
		if maxParts := int64(s3MaxUploadParts); (size+partSize-1)/partSize > maxParts {
			partSize = (size + maxParts - 1) / maxParts
		}
		parts, err := d.uploadMultipart(ctx, s3Client, initResp.Bucket, objectKey, s3UploadID, reader, size, partSize, up)
		if err != nil {
			abortMultipartUpload(ctx, s3Client, initResp.Bucket, objectKey, s3UploadID)
			return nil, err
		}
		_, err = s3Client.CompleteMultipartUploadWithContext(ctx, &s3.CompleteMultipartUploadInput{
			Bucket:   aws.String(initResp.Bucket),
			Key:      aws.String(objectKey),
			UploadId: aws.String(s3UploadID),
			MultipartUpload: &s3.CompletedMultipartUpload{
				Parts: parts,
			},
		})
		if err != nil {
			abortMultipartUpload(ctx, s3Client, initResp.Bucket, objectKey, s3UploadID)
			return nil, err
		}
	}

	// 4) check status
	if err := d.waitCheckComplete(ctx, initResp.UploadID); err != nil {
		return nil, err
	}

	// 5) complete upload
	completeBody := base.Json{
		"host_id": d.HostID,
		"path":    remoteDir,
		"file":    []base.Json{{"path": initResp.File[0].Path, "size": size}},
		"state":   "complete",
	}
	_, err = d.newForestRequest(ctx, http.MethodPost, forestBase+"/v1/complete/upload", completeBody, nil)
	if err != nil {
		return nil, err
	}

	return &File{
		Object: model.Object{
			// normalize the server-returned path against remoteDir so the ID
			// of uploads into nested directories keeps its parent path,
			// consistently with parseList
			ID:       normalizeFilePath(remoteDir, initResp.File[0].Path),
			Path:     path.Join(dstDir.GetPath(), file.GetName()),
			Name:     file.GetName(),
			Size:     size,
			Modified: time.Now(),
		},
		VersionID:    initResp.File[0].VersionID,
		LastModified: initResp.File[0].LastModified,
	}, nil
}

func (d *RakutenDrive) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	body := base.Json{
		"host_id": d.HostID,
	}
	var resp struct {
		MaxSize   int64 `json:"max_size"`
		TotalSize int64 `json:"total_size"`
		UsageSize int64 `json:"usage_size"`
	}
	_, err := d.newForestRequest(ctx, http.MethodPost, forestBase+"/v1/capacities", body, &resp)
	if err != nil {
		return nil, err
	}
	total := resp.MaxSize
	if total == 0 {
		total = resp.TotalSize
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  resp.UsageSize,
		},
	}, nil
}

// parseList parses a page of the files API response. It returns the parsed
// objects and the raw number of entries on the page, which can differ when
// entries cannot be mapped to a path.
func (d *RakutenDrive) parseList(body []byte, remoteDir string) ([]model.Obj, int, error) {
	// nested data candidates must be tried before the bare "data" path:
	// when data is an object, its size is non-zero too
	paths := [][]interface{}{
		{"file"},
		{"files"},
		{"items"},
		{"data", "file"},
		{"data", "files"},
		{"data", "items"},
		{"data"},
	}
	var list jsoniter.Any
	for _, p := range paths {
		candidate := utils.Json.Get(body, p...)
		// only accept arrays; an object or scalar field of the same name
		// must not be mistaken for the list of entries
		if candidate.ValueType() == jsoniter.ArrayValue && candidate.Size() > 0 {
			list = candidate
			break
		}
	}
	if list == nil || list.Size() == 0 {
		return nil, 0, nil
	}
	objs := make([]model.Obj, 0, list.Size())
	for i := 0; i < list.Size(); i++ {
		item := list.Get(i)
		itemPath := item.Get("path").ToString()
		if itemPath == "" {
			itemPath = item.Get("Path").ToString()
		}
		itemName := item.Get("name").ToString()
		if itemName == "" {
			itemName = item.Get("Name").ToString()
		}
		if itemPath == "" {
			itemPath = itemName
		}
		remotePath := normalizeFilePath(remoteDir, itemPath)
		if remotePath == "" {
			continue
		}
		isFolder := d.detectIsFolder(item, itemPath)
		modified := parseTimeAny(item.Get("last_modified").ToString())
		if modified.IsZero() {
			modified = parseTimeAny(item.Get("LastModified").ToString())
		}
		if modified.IsZero() {
			modified = parseTimeAny(item.Get("modified").ToString())
		}
		if modified.IsZero() {
			modified = parseTimeAny(item.Get("mtime").ToInt64())
		}
		created := parseTimeAny(item.Get("created").ToString())
		if created.IsZero() {
			created = parseTimeAny(item.Get("Created").ToString())
		}
		name := path.Base(strings.TrimSuffix(remotePath, "/"))
		localPath := d.remotePath2LocalPath(remotePath)
		size := item.Get("size").ToInt64()
		if size == 0 {
			size = item.Get("Size").ToInt64()
		}
		versionID := item.Get("version_id").ToString()
		if versionID == "" {
			versionID = item.Get("VersionID").ToString()
		}
		lastModified := item.Get("last_modified").ToString()
		if lastModified == "" {
			lastModified = item.Get("LastModified").ToString()
		}
		fileObj := &File{
			Object: model.Object{
				ID:       remotePath,
				Path:     localPath,
				Name:     name,
				Size:     size,
				Modified: modified,
				Ctime:    created,
				IsFolder: isFolder,
			},
			VersionID:    versionID,
			LastModified: lastModified,
		}
		objs = append(objs, fileObj)
	}
	return objs, list.Size(), nil
}

func (d *RakutenDrive) getFile(obj model.Obj) (*File, bool) {
	if f, ok := obj.(*File); ok {
		return f, true
	}
	if uw, ok := obj.(model.ObjUnwrap); ok {
		if f, ok := uw.Unwrap().(*File); ok {
			return f, true
		}
	}
	return nil, false
}

// abortMultipartUpload aborts a stale multipart upload. It derives a bounded
// context that ignores cancellation of the caller's context, since the upload
// has often failed precisely because that context was canceled.
func abortMultipartUpload(ctx context.Context, client *s3.S3, bucket, key, uploadID string) {
	abortCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	_, _ = client.AbortMultipartUploadWithContext(abortCtx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(bucket),
		Key:      aws.String(key),
		UploadId: aws.String(uploadID),
	})
}

func (d *RakutenDrive) uploadMultipart(ctx context.Context, client *s3.S3, bucket, key, uploadID string, reader io.Reader, size, partSize int64, up driver.UpdateProgress) ([]*s3.CompletedPart, error) {
	if size <= 0 {
		return nil, fmt.Errorf("file size required for multipart upload")
	}
	buf := make([]byte, partSize)
	var (
		parts []*s3.CompletedPart
		total int64
		part  int64 = 1
	)
	for total < size {
		readSize := int64(len(buf))
		if size-total < readSize {
			readSize = size - total
		}
		n, err := io.ReadFull(reader, buf[:readSize])
		if err != nil {
			return nil, fmt.Errorf("upload short read: got %d of %d bytes: %w", total+int64(n), size, err)
		}
		body := bytes.NewReader(buf[:n])
		out, err := client.UploadPartWithContext(ctx, &s3.UploadPartInput{
			Bucket:        aws.String(bucket),
			Key:           aws.String(key),
			PartNumber:    aws.Int64(part),
			UploadId:      aws.String(uploadID),
			Body:          body,
			ContentLength: aws.Int64(int64(n)),
		})
		if err != nil {
			return nil, err
		}
		parts = append(parts, &s3.CompletedPart{
			ETag:       out.ETag,
			PartNumber: aws.Int64(part),
		})
		part++
		total += int64(n)
		if up != nil {
			up(float64(total) / float64(size) * 100)
		}
	}
	return parts, nil
}

// normalizePrefix extracts the directory prefix from a remote path and normalizes it
// by ensuring it ends with "/" (except for root or empty paths)
func (d *RakutenDrive) normalizePrefix(remotePath string) string {
	prefix := path.Dir(remotePath)
	if prefix == "." || prefix == "/" {
		prefix = ""
	} else if !strings.HasSuffix(prefix, "/") {
		prefix += "/"
	}
	return prefix
}

// waitCheckComplete polls files/check until the async operation reaches
// state "complete" or the retry budget is exhausted
func (d *RakutenDrive) waitCheckComplete(ctx context.Context, key string) error {
	body := base.Json{"key": key}
	var lastState string
	for i := 0; i < 30; i++ {
		var checkResp uploadCheckResp
		_, err := d.newForestRequest(ctx, http.MethodPost, forestBase+"/v3/files/check", body, &checkResp)
		if err != nil {
			return err
		}
		lastState = checkResp.State
		if strings.EqualFold(lastState, "complete") {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(1 * time.Second):
		}
	}
	return fmt.Errorf("operation %s did not complete in time (last state: %s)", key, lastState)
}

// detectIsFolder determines if an item represents a folder based on various attributes
// from the API response, including explicit folder flags, type fields, and path suffixes
func (d *RakutenDrive) detectIsFolder(item jsoniter.Any, itemPath string) bool {
	// Check various boolean folder indicators
	isFolder := item.Get("is_folder").ToBool() ||
		item.Get("isFolder").ToBool() ||
		item.Get("dir").ToBool() ||
		item.Get("folder").ToBool() ||
		item.Get("IsFolder").ToBool()

	if !isFolder {
		// Check type field as string
		switch strings.ToLower(item.Get("type").ToString()) {
		case "folder", "dir", "directory":
			isFolder = true
		}
		// Check type field as integer (1 = folder)
		if item.Get("type").ToInt64() == 1 {
			isFolder = true
		}
	}

	// Check if path ends with "/" (common folder indicator)
	if strings.HasSuffix(itemPath, "/") {
		isFolder = true
	}

	return isFolder
}

var _ driver.Driver = (*RakutenDrive)(nil)
