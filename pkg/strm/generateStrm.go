package strm

import (
	"fmt"
	"github.com/OpenListTeam/OpenList/internal/conf"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

var (
	supportSuffix = map[string]struct{}{
		// video
		"mp4":  {},
		"mkv":  {},
		"flv":  {},
		"avi":  {},
		"wmv":  {},
		"ts":   {},
		"rmvb": {},
		"webm": {},
		// audio
		"mp3":  {},
		"flac": {},
		"aac":  {},
		"wav":  {},
		"ogg":  {},
		"m4a":  {},
		"wma":  {},
		"alac": {},
	}
)

type WriteResult struct {
	Path    string
	Success bool
	Error   error
}

func getLocalFilePath(filePath string) string {
	fileNameWithoutExt := strings.TrimSuffix(filePath, filepath.Ext(filePath))
	if strings.HasPrefix(fileNameWithoutExt, "/") {
		return filepath.Join(conf.Conf.StrmRootDir, fileNameWithoutExt+".strm")
	}
	return filepath.Join(conf.Conf.StrmRootDir, fileNameWithoutExt+".strm")
}

func getFileContent(filePath string) string {
	encodedPath := url.PathEscape(filePath)
	return strings.TrimRight(conf.Conf.SiteURL, "/") + "/d" + encodedPath
}

func WriteFile(filePath string) WriteResult {
	result := WriteResult{}

	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(filePath), "."))
	if _, ok := supportSuffix[ext]; !ok {
		result.Success = false
		result.Error = fmt.Errorf("FileTypeNotSupported: %s", ext)
		return result
	}

	localFilePath := getLocalFilePath(filePath)
	result.Path = localFilePath

	localDir := filepath.Dir(localFilePath)
	if err := os.MkdirAll(localDir, os.ModePerm); err != nil {
		result.Success = false
		result.Error = fmt.Errorf("FailedToCreateADirectory: %v", err)
		return result
	}

	content := getFileContent(filePath)
	if err := os.WriteFile(localFilePath, []byte(content), 0644); err != nil {
		result.Success = false
		result.Error = fmt.Errorf("FailedToWriteToFile: %v", err)
	} else {
		result.Success = true
	}
	return result
}

func WriteFiles(filePaths []string) map[string]interface{} {
	var wg sync.WaitGroup
	results := make(chan WriteResult, len(filePaths))
	for _, filePath := range filePaths {
		wg.Add(1)
		go func(path string) {
			defer wg.Done()
			results <- WriteFile(path)
		}(filePath)
	}
	wg.Wait()
	close(results)

	var successPaths, failedPaths []string
	for result := range results {
		if result.Success {
			successPaths = append(successPaths, result.Path)
		} else {
			failedPaths = append(failedPaths, result.Path+"\n"+result.Error.Error())
		}
	}
	resMap := make(map[string]interface{})
	resMap["successPaths"] = successPaths
	resMap["failedPaths"] = failedPaths
	return resMap
}
