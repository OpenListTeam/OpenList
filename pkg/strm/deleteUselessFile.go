package strm

import (
	"github.com/OpenListTeam/OpenList/internal/conf"
	"github.com/OpenListTeam/OpenList/pkg/utils"
	"os"
	"path/filepath"
	"strings"
)

func getLocalFiles(path string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(filepath.Join(conf.Conf.StrmRootDir, path), func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			files = append(files, path)
		}
		return nil
	})
	return files, err
}

func getPathWithFileNameWithoutExt(filePath string) string {
	dir := filepath.Dir(filePath)
	fileNameWithoutExt := strings.TrimSuffix(filepath.Base(filePath), filepath.Ext(filePath))
	return filepath.Join(dir, fileNameWithoutExt)
}

func DeleteExtraFiles(path string, alistFiles []string) []string {
	allFiles, _ := getLocalFiles(path)

	alistFileSet := make(map[string]struct{})
	for _, alistPath := range alistFiles {
		fullPath := filepath.Join(conf.Conf.StrmRootDir, alistPath)
		key := getPathWithFileNameWithoutExt(fullPath)
		alistFileSet[key] = struct{}{}
	}

	var extraFiles []string
	for _, localFile := range allFiles {
		localFileKey := getPathWithFileNameWithoutExt(localFile)
		if _, exists := alistFileSet[localFileKey]; !exists {
			extraFiles = append(extraFiles, localFile)
		}
	}

	var resFiles []string
	for _, file := range extraFiles {
		if strings.HasSuffix(file, ".nfo") {
			utils.Log.Debugf("Skipped file: %s\n", file)
			continue
		}
		err := os.Remove(file)
		if err != nil {
			utils.Log.Fatalf("Failed to delete file: %s, error: %v\n", file, err)
		} else {
			resFiles = append(resFiles, file)
			utils.Log.Infof("Deleted file: %s\n", file)
		}
	}
	return resFiles
}
