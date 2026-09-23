package modelscope

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

const (
	// lfsThreshold mirrors modelscope_hub: files larger than 1 MiB are forced
	// into LFS mode regardless of suffix.
	lfsThreshold = 1 * 1024 * 1024
)

// uploadFile uploads a single file into the repository at filePath.
//
// It chooses "normal" (inline base64) or "lfs" (blob upload + pointer) mode
// based on size/suffix, matching modelscope_hub's _upload_mode.
func (d *ModelScope) uploadFile(ctx context.Context, filePath string, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// Materialise the stream into a seekable file so we can compute SHA256 and
	// then re-read it for the actual upload without buffering twice.
	file, err := stream.CacheFullAndWriter(&up, nil)
	if err != nil {
		return nil, err
	}

	size := stream.GetSize()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	digest, err := utils.HashFile(utils.SHA256, file)
	if err != nil {
		return nil, err
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}

	mode := d.uploadMode(filePath, size)
	switch mode {
	case "lfs":
		if err := d.uploadLFSFile(ctx, filePath, file, size, digest, up); err != nil {
			return nil, err
		}
	default:
		if err := d.uploadNormalFile(ctx, filePath, file, size, up); err != nil {
			return nil, err
		}
	}

	return &model.Object{
		Name:     path.Base(filePath),
		Path:     filePath,
		ID:       filePath,
		Size:     size,
		HashInfo: utils.NewHashInfo(utils.SHA256, digest),
	}, nil
}

// uploadMode decides normal vs lfs, matching modelscope_hub._is_lfs.
func (d *ModelScope) uploadMode(filePath string, size int64) string {
	if size > lfsThreshold {
		return "lfs"
	}
	suffix := strings.ToLower(pathExt(filePath))
	if d.isLfsSuffix(suffix) {
		return "lfs"
	}
	return "normal"
}

// uploadNormalFile commits a small file inline as base64 content.
func (d *ModelScope) uploadNormalFile(ctx context.Context, filePath string, file model.File, size int64, up driver.UpdateProgress) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	data, err := io.ReadAll(file)
	if err != nil {
		return err
	}
	content := base64.StdEncoding.EncodeToString(data)
	action := commitAction{
		Action:   "create",
		Path:     filePath,
		Type:     "normal",
		Size:     size,
		Content:  content,
		Encoding: "base64",
	}
	return d.commit(ctx, []commitAction{action})
}

// uploadLFSFile uploads a large file via the LFS batch API and commits an LFS
// pointer.
func (d *ModelScope) uploadLFSFile(ctx context.Context, filePath string, file model.File, size int64, digest string, up driver.UpdateProgress) error {
	// Ask the server which blobs are missing.
	req := lfsBatchReq{
		Operation: "upload",
		Objects:   []lfsObject{{Oid: digest, Size: size}},
	}
	var batchResp lfsBatchResp
	res, err := base.NewRestyClient().R().
		SetHeaders(d.authHeaders()).
		SetContext(ctx).
		SetBody(req).
		Post(d.buildURL("repos/" + d.repoSegment() + "/" + d.RepoID + "/info/lfs/objects/batch"))
	if err != nil {
		return err
	}
	if err := checkErr(res); err != nil {
		return err
	}
	if err := jsonUnmarshal(res.Body(), &batchResp); err != nil {
		return err
	}

	// Find the upload href for our oid, if the server says we must upload.
	var uploadURL string
	for _, o := range batchResp.objects() {
		if o.Oid == digest {
			if a, ok := o.Actions["upload"]; ok {
				uploadURL = a.Href
			}
		}
	}

	if uploadURL != "" {
		if err := d.putBlob(ctx, uploadURL, file, size, up); err != nil {
			return err
		}
	}

	action := commitAction{
		Action: "create",
		Path:   filePath,
		Type:   "lfs",
		Size:   size,
		Sha256: digest,
	}
	return d.commit(ctx, []commitAction{action})
}

// putBlob PUTs the file content to a presigned LFS upload URL.
func (d *ModelScope) putBlob(ctx context.Context, uploadURL string, file model.File, size int64, up driver.UpdateProgress) error {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	reader := &driver.ReaderUpdatingProgress{
		Reader: &driver.SimpleReaderWithSize{
			Reader: file,
			Size:   size,
		},
		UpdateProgress: up,
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Length", fmt.Sprintf("%d", size))
	if t := strings.TrimSpace(d.Token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
		req.Header.Set("Cookie", "m_session_id="+t)
	}

	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.Errorf("lfs blob upload failed %s: %s", resp.Status, string(body))
	}
	return nil
}

// commit posts a set of file operations to the commit endpoint.
func (d *ModelScope) commit(ctx context.Context, actions []commitAction) error {
	if len(actions) == 0 {
		return nil
	}
	body := commitReq{
		CommitMessage: d.CommitMessage,
		Actions:       actions,
	}
	res, err := base.NewRestyClient().R().
		SetHeaders(d.authHeaders()).
		SetContext(ctx).
		SetBody(body).
		Post(d.buildURL("repos/" + d.repoSegment() + "/" + d.RepoID + "/commit/" + d.Revision))
	if err != nil {
		return err
	}
	return checkErr(res)
}

// deleteFiles removes one or more files via a single commit with delete actions.
func (d *ModelScope) deleteFiles(ctx context.Context, paths []string) error {
	actions := make([]commitAction, 0, len(paths))
	for _, p := range paths {
		actions = append(actions, commitAction{
			Action: "delete",
			Path:   p,
		})
	}
	return d.commit(ctx, actions)
}

// movePath moves a file or directory by committing a create/delete pair.
//
// LFS files can be re-pointed by sha256 without re-downloading. Normal files
// must carry their base64 content inline, so we download them and re-upload.
func (d *ModelScope) movePath(ctx context.Context, src, dst string, isDir bool) (model.Obj, error) {
	if isDir {
		entries, err := d.getFileList(ctx, src, true)
		if err != nil {
			return nil, err
		}
		prefix := utils.PathAddSeparatorSuffix(src)
		var actions []commitAction
		for _, e := range entries {
			ep := normalizePath(e.getPath())
			if !strings.HasPrefix(ep, prefix) {
				continue
			}
			if e.isDir() {
				continue
			}
			rel := strings.TrimPrefix(ep, prefix)
			newPath := joinRepoPath(dst, rel)
			sha := e.Sha256
			if sha == "" {
				sha = e.Sha256Snake
			}
			if sha != "" {
				// LFS file: re-point the blob.
				actions = append(actions, commitAction{Action: "create", Path: newPath, Type: "lfs", Size: e.getSize(), Sha256: sha})
			} else {
				// Normal file: download content and inline it.
				content, err := d.fetchFileContent(ctx, ep)
				if err != nil {
					return nil, err
				}
				actions = append(actions, commitAction{
					Action:   "create",
					Path:     newPath,
					Type:     "normal",
					Size:     e.getSize(),
					Content:  base64.StdEncoding.EncodeToString(content),
					Encoding: "base64",
				})
			}
			actions = append(actions, commitAction{Action: "delete", Path: ep})
		}
		if len(actions) > 0 {
			if err := d.commit(ctx, actions); err != nil {
				return nil, err
			}
		}
		return &model.Object{
			Name:     path.Base(dst),
			Path:     dst,
			ID:       dst,
			IsFolder: true,
		}, nil
	}

	// Single file move.
	link, err := d.downloadLinkForPath(ctx, src)
	if err != nil {
		return nil, err
	}
	// Download and re-upload.
	newObj, err := d.copyFileFromURL(ctx, src, dst, link)
	if err != nil {
		return nil, err
	}
	if err := d.deleteFiles(ctx, []string{src}); err != nil {
		return nil, err
	}
	return newObj, nil
}

// fetchFileContent downloads a file's raw bytes for inline (normal-file) moves.
func (d *ModelScope) fetchFileContent(ctx context.Context, repoPath string) ([]byte, error) {
	link := &model.Link{URL: d.downloadURL(repoPath), Header: d.downloadHeaders()}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range link.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, errors.Errorf("download failed %s: %s", resp.Status, string(body))
	}
	return io.ReadAll(resp.Body)
}

// downloadLinkForPath returns a Link for the given repo path.
func (d *ModelScope) downloadLinkForPath(ctx context.Context, repoPath string) (*model.Link, error) {
	return &model.Link{
		URL:    d.downloadURL(repoPath),
		Header: d.downloadHeaders(),
	}, nil
}

// copyFileFromURL downloads the file at src (via link) and re-uploads it to dst.
func (d *ModelScope) copyFileFromURL(ctx context.Context, src, dst string, link *model.Link) (model.Obj, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
	if err != nil {
		return nil, err
	}
	for k, vs := range link.Header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, errors.Errorf("download failed %s: %s", resp.Status, string(body))
	}

	h := sha256.New()
	data, err := io.ReadAll(io.TeeReader(resp.Body, h))
	if err != nil {
		return nil, err
	}
	digest := hex.EncodeToString(h.Sum(nil))
	size := int64(len(data))

	obj := &model.Object{
		Name:     path.Base(dst),
		Path:     dst,
		ID:       dst,
		Size:     size,
		HashInfo: utils.NewHashInfo(utils.SHA256, digest),
	}

	mode := d.uploadMode(dst, size)
	if mode == "lfs" {
		// Upload via LFS.
		req := lfsBatchReq{Operation: "upload", Objects: []lfsObject{{Oid: digest, Size: size}}}
		var batchResp lfsBatchResp
		res, err := base.NewRestyClient().R().
			SetHeaders(d.authHeaders()).
			SetContext(ctx).
			SetBody(req).
			Post(d.buildURL("repos/" + d.repoSegment() + "/" + d.RepoID + "/info/lfs/objects/batch"))
		if err != nil {
			return nil, err
		}
		if err := checkErr(res); err != nil {
			return nil, err
		}
		if err := jsonUnmarshal(res.Body(), &batchResp); err != nil {
			return nil, err
		}
		var uploadURL string
		for _, o := range batchResp.objects() {
			if o.Oid == digest {
				if a, ok := o.Actions["upload"]; ok {
					uploadURL = a.Href
				}
			}
		}
		if uploadURL != "" {
			if err := d.putBytes(ctx, uploadURL, data, size); err != nil {
				return nil, err
			}
		}
		return obj, d.commit(ctx, []commitAction{{Action: "create", Path: dst, Type: "lfs", Size: size, Sha256: digest}})
	}

	// Normal inline upload.
	content := base64.StdEncoding.EncodeToString(data)
	return obj, d.commit(ctx, []commitAction{{Action: "create", Path: dst, Type: "normal", Size: size, Content: content, Encoding: "base64"}})
}

// putBytes uploads in-memory bytes to a presigned LFS URL.
func (d *ModelScope) putBytes(ctx context.Context, uploadURL string, data []byte, size int64) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, uploadURL, strings.NewReader(string(data)))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Length", fmt.Sprintf("%d", size))
	if t := strings.TrimSpace(d.Token); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
		req.Header.Set("Cookie", "m_session_id="+t)
	}
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return errors.Errorf("lfs blob upload failed %s: %s", resp.Status, string(body))
	}
	return nil
}

// putSmallFile writes an empty/near-empty file (used for .gitkeep placeholders).
func (d *ModelScope) putSmallFile(ctx context.Context, filePath string, _ []byte) error {
	return d.commit(ctx, []commitAction{{
		Action:   "create",
		Path:     filePath,
		Type:     "normal",
		Size:     0,
		Content:  "",
		Encoding: "base64",
	}})
}

// pathExt returns the lowercased file extension (without dot).
func pathExt(p string) string {
	return strings.ToLower(utils.Ext(p))
}

// isLfsSuffix reports whether a file suffix forces LFS mode for this repo type.
func (d *ModelScope) isLfsSuffix(suffix string) bool {
	// These lists mirror modelscope_hub's MODEL_LFS_SUFFIX / DATASET_LFS_SUFFIX.
	common := map[string]bool{
		".7z": true, ".bin": true, ".bz2": true, ".gz": true, ".h5": true,
		".msgpack": true, ".npy": true, ".npz": true, ".ot": true, ".parquet": true,
		".pb": true, ".pickle": true, ".pkl": true, ".rar": true, ".tar": true,
		".tgz": true, ".wasm": true, ".zip": true, ".zst": true, ".joblib": true,
		".ftz": true,
	}
	if common["."+suffix] {
		return true
	}
	if d.RepoType == "model" {
		modelOnly := map[string]bool{
			".ckpt": true, ".mlmodel": true, ".model": true, ".onnx": true,
			".pt": true, ".pth": true, ".safetensors": true, ".tflite": true,
			".xz": true, ".arrow": true,
		}
		return modelOnly["."+suffix]
	}
	// dataset
	datasetOnly := map[string]bool{
		".aac": true, ".audio": true, ".bmp": true, ".flac": true, ".gif": true,
		".jack": true, ".jpeg": true, ".jpg": true, ".jsonl": true, ".lz4": true,
		".pcm": true, ".raw": true, ".sam": true, ".wav": true, ".webm": true,
		".webp": true, ".tiff": true, ".mp3": true, ".mp4": true, ".ogg": true,
		".arrow": true, ".png": true,
	}
	return datasetOnly["."+suffix]
}

var (
	_ driver.PutResult    = (*ModelScope)(nil)
	_ driver.MkdirResult  = (*ModelScope)(nil)
	_ driver.Remove       = (*ModelScope)(nil)
	_ driver.MoveResult   = (*ModelScope)(nil)
	_ driver.RenameResult = (*ModelScope)(nil)
	_ driver.CopyResult   = (*ModelScope)(nil)
	_ driver.GetRooter    = (*ModelScope)(nil)
)
