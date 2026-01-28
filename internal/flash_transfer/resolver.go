package flash_transfer

import (
	"errors"
	"fmt"
	"math"
	"math/rand"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
)

type UserSelection struct {
	FilesetID string `json:"fileset_id"`

	// 基础信息
	FileID     string `json:"file_id"`
	PhysicalID string `json:"physical_id"`
	Name       string `json:"name"`
	FileSize   int64  `json:"file_size"`

	Path  string `json:"path"`
	IsDir bool   `json:"is_dir"`

	IsZipContent bool   `json:"is_zip_content"`
	ZipFileID    string `json:"zip_file_id"`
}

type DownloadTask struct {
	Name         string `json:"name"`
	PhysicalID   string `json:"physical_id"`
	RelativePath string `json:"relative_path"`
	DownloadURL  string `json:"download_url"`
	FileSize     int64  `json:"file_size"`
	FilesetID    string `json:"fileset_id"`
}

type Resolver struct {
	client      *FlashClient
	concurrency int
	semaphore   chan struct{}
	wg          sync.WaitGroup
	results     []DownloadTask
	mu          sync.Mutex
	errors      []error
}

func (c *FlashClient) ResolveDownloads(selections []UserSelection) ([]DownloadTask, error) {
	r := &Resolver{
		client:      c,
		concurrency: 5,
		semaphore:   make(chan struct{}, 5),
		results:     make([]DownloadTask, 0),
		errors:      make([]error, 0),
	}

	rootPath := r.findCommonPrefix(selections)
	rootPath = strings.TrimSuffix(rootPath, "/")

	for _, item := range selections {
		r.wg.Add(1)

		relPath := strings.TrimPrefix(item.Path, rootPath)
		relPath = strings.TrimPrefix(relPath, "/")

		go r.processItem(item, relPath)
	}

	r.wg.Wait()

	if len(r.errors) > 0 {
		return r.results, fmt.Errorf("errors occurred, sample: %v", r.errors[0])
	}
	return r.results, nil
}

func (r *Resolver) processItem(item UserSelection, currentRelPath string) {
	defer r.wg.Done()

	r.semaphore <- struct{}{}
	defer func() { <-r.semaphore }()

	if item.IsDir {
		isInsideZip := item.IsZipContent

		r.walkFolder(item.FilesetID, item.FileID, currentRelPath, isInsideZip, item.ZipFileID)
		return
	}

	if item.PhysicalID == "" {
		r.addError(fmt.Errorf("missing PhysicalID for file: %s", item.Path))
		return
	}

	r.addDownloadTask(item.Name, item.PhysicalID, item.FileSize, currentRelPath, item.FilesetID)
}

func (r *Resolver) walkFolder(filesetID, parentID, relDir string, isInsideZip bool, rootZipID string) {
	var nodes []FileInfo
	var err error

	err = r.retryWithBackoff(func() error {
		if isInsideZip {
			resp, e := r.client.GetCompressedFileFolder(filesetID, rootZipID, parentID)
			if e != nil {
				return e
			}
			if len(resp.Data.FileLists) > 0 {
				nodes = resp.Data.FileLists[0].FileList
			}
			return nil
		}

		resp, e := r.client.GetFileFolder(filesetID, parentID)
		if e != nil {
			return e
		}
		if len(resp.Data.FileLists) > 0 {
			nodes = resp.Data.FileLists[0].FileList
		}
		return nil
	})

	if err != nil {
		r.addError(fmt.Errorf("list dir error %s: %v", relDir, err))
		return
	}

	for _, node := range nodes {
		if node.ParentId != parentID && parentID != "" && !isInsideZip {
			continue
		}

		childRelPath := path.Join(relDir, node.Name)

		if node.IsDir {
			r.wg.Add(1)
			go func(n FileInfo, p string) {
				defer r.wg.Done()
				r.semaphore <- struct{}{}
				defer func() { <-r.semaphore }()

				r.walkFolder(filesetID, n.SrvFileid, p, isInsideZip, rootZipID)
			}(node, childRelPath)

		} else if isZipFile(node.Name) && !isInsideZip {
			r.addDownloadTask(node.Name, node.Physical.Id, parseSize(node.FileSize), childRelPath, filesetID)
		} else {
			r.addDownloadTask(node.Name, node.Physical.Id, parseSize(node.FileSize), childRelPath, filesetID)
		}
	}
}

func (r *Resolver) addDownloadTask(name, physicalID string, fileSize int64, relPath, filesetID string) {
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		r.semaphore <- struct{}{}
		defer func() { <-r.semaphore }()

		var downloadUrl string

		if physicalID == "" {
			r.addError(fmt.Errorf("no physical id for %s", relPath))
			return
		}

		err := r.retryWithBackoff(func() error {
			resp, e := r.client.GetDownloadUrl(physicalID, physicalID)
			if e != nil {
				return e
			}
			if len(resp.Data.DownloadRsp) == 0 {
				return errors.New("no url response")
			}

			downloadUrl = resp.Data.DownloadRsp[0].Url

			if strings.HasPrefix(downloadUrl, "http://") {
				downloadUrl = strings.Replace(downloadUrl, "http://", "https://", 1)
			}
			return nil
		})

		if err != nil {
			r.addError(fmt.Errorf("failed to get url for %s: %v", relPath, err))
			return
		}

		task := DownloadTask{
			Name:         name,
			PhysicalID:   physicalID,
			RelativePath: relPath,
			DownloadURL:  downloadUrl,
			FileSize:     fileSize,
			FilesetID:    filesetID,
		}

		r.mu.Lock()
		r.results = append(r.results, task)
		r.mu.Unlock()
	}()
}

func (r *Resolver) retryWithBackoff(op func() error) error {
	maxRetries := 4
	baseDelay := 200 * time.Millisecond
	var err error
	for i := 0; i < maxRetries; i++ {
		err = op()
		if err == nil {
			return nil
		}

		delay := baseDelay * time.Duration(math.Pow(2, float64(i)))
		jitter := time.Duration(rand.Int63n(int64(delay / 2)))
		time.Sleep(delay + jitter)
	}
	return err
}

func (r *Resolver) addError(err error) {
	r.mu.Lock()
	r.errors = append(r.errors, err)
	r.mu.Unlock()
}

func (r *Resolver) findCommonPrefix(items []UserSelection) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) == 1 {
		return path.Dir(strings.TrimSuffix(items[0].Path, "/"))
	}

	paths := make([]string, len(items))
	for i, v := range items {
		paths[i] = v.Path
	}
	sort.Strings(paths)

	first, last := paths[0], paths[len(paths)-1]
	i := 0
	for i < len(first) && i < len(last) && first[i] == last[i] {
		i++
	}
	common := first[:i]
	if idx := strings.LastIndex(common, "/"); idx != -1 {
		return common[:idx+1]
	}
	return ""
}

func isZipFile(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, ".zip") ||
		strings.HasSuffix(lower, ".rar") ||
		strings.HasSuffix(lower, ".7z")
}

func parseSize(s string) int64 {
	var size int64
	_, err := fmt.Sscanf(s, "%d", &size)
	if err != nil {
		return 0
	}
	return size
}
