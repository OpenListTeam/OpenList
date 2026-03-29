package http

import (
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type SimpleHttp struct {
	client http.Client
}

func (s SimpleHttp) Name() string {
	return "SimpleHttp"
}

func (s SimpleHttp) Items() []model.SettingItem {
	return nil
}

func (s SimpleHttp) Init() (string, error) {
	return "ok", nil
}

func (s SimpleHttp) IsReady() bool {
	return true
}

func (s SimpleHttp) AddURL(args *tool.AddUrlArgs) (string, error) {
	panic("should not be called")
}

func (s SimpleHttp) Remove(task *tool.DownloadTask) error {
	panic("should not be called")
}

func (s SimpleHttp) Status(task *tool.DownloadTask) (*tool.Status, error) {
	panic("should not be called")
}

func (s SimpleHttp) Run(task *tool.DownloadTask) error {
	streamPut := task.DeletePolicy == tool.UploadDownloadStream
	method := http.MethodGet
	if streamPut {
		method = http.MethodHead
	}
	req, err := http.NewRequestWithContext(task.Ctx(), method, task.Url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", base.UserAgent)
	if streamPut {
		req.Header.Set("Range", "bytes=0-")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return fmt.Errorf("http status code %d", resp.StatusCode)
	}
	filename, err := parseFilenameFromContentDisposition(resp.Header.Get("Content-Disposition"))
	if err != nil {
		filename = path.Base(resp.Request.URL.Path)
	}
	filename = strings.Trim(filename, "/")
	if len(filename) == 0 {
		filename = fmt.Sprintf("%s-%d-%x", strings.ReplaceAll(req.URL.Host, ".", "_"), time.Now().UnixMilli(), rand.Uint32())
	}
	fileSize := resp.ContentLength
	if streamPut {
		if fileSize == 0 {
			start, end, _ := http_range.ParseContentRange(resp.Header.Get("Content-Range"))
			fileSize = start + end
		}
		task.SetTotalBytes(fileSize)
		task.TempDir = filename
		return nil
	}
	task.SetTotalBytes(fileSize)
	// save to temp dir
	_ = os.MkdirAll(task.TempDir, os.ModePerm)
	filePath := filepath.Join(task.TempDir, filename)
	file, err := os.Create(filePath)
	if err != nil {
		return err
	}
	defer file.Close()
	err = utils.CopyWithCtx(task.Ctx(), file, resp.Body, fileSize, task.SetProgress)
	return err
}

func init() {
	tool.Tools.Add(&SimpleHttp{})
}
