package weiyun_open

import (
	"context"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func (d *WeiYunOpen) finalizeUpload(
	ctx context.Context,
	folder *Folder,
	resp *uploadResponse,
	fallbackName string,
	size int64,
) (model.Obj, error) {
	if resp.FileID != "" {
		return uploadResultFile(folder.DirKey, resp, fallbackName, size), nil
	}
	file, err := d.findUploadedFile(ctx, folder, uploadCandidateNames(resp.FileName, fallbackName), size)
	if err == nil {
		return file, nil
	}
	return nil, fmt.Errorf(
		"weiyun upload finished without file_id: state=%d, file_exist=%t, filename=%q: %w",
		resp.UploadState, resp.FileExist, uploadFileName(resp.FileName, fallbackName), err,
	)
}

func (d *WeiYunOpen) findUploadedFile(
	ctx context.Context,
	folder *Folder,
	names []string,
	size int64,
) (*File, error) {
	offset := 0
	candidates := make([]*File, 0)
	for {
		page, err := d.listPage(ctx, folder, offset)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, matchedUploadFiles(page, names)...)
		if page.FinishFlag {
			return pickUploadedFile(candidates, size)
		}
		pageCount := len(page.DirList) + len(page.FileList)
		if pageCount == 0 {
			return nil, fmt.Errorf("weiyun list returned empty page before finish")
		}
		offset += pageCount
	}
}

func matchedUploadFiles(page *listResponse, names []string) []*File {
	files := make([]*File, 0)
	for _, item := range page.FileList {
		if !containsName(names, item.FileName) {
			continue
		}
		files = append(files, newFile(page.PdirKey, item))
	}
	return files
}

func pickUploadedFile(files []*File, size int64) (*File, error) {
	if len(files) == 0 {
		return nil, fmt.Errorf("uploaded file not found in target directory")
	}
	best := files[0]
	bestScore := uploadCandidateScore(best, size)
	for i := 1; i < len(files); i++ {
		score := uploadCandidateScore(files[i], size)
		if score > bestScore || (score == bestScore && files[i].FileMTime > best.FileMTime) {
			best = files[i]
			bestScore = score
		}
	}
	return best, nil
}

func uploadCandidateScore(file *File, size int64) int {
	score := 0
	if file.FileSize == size {
		score += 2
	}
	if file.FileMTime > 0 {
		score++
	}
	return score
}

func uploadCandidateNames(primary string, fallback string) []string {
	if primary == "" || primary == fallback {
		return []string{fallback}
	}
	return []string{primary, fallback}
}

func uploadFileName(primary string, fallback string) string {
	if primary != "" {
		return primary
	}
	return fallback
}

func containsName(names []string, name string) bool {
	for _, candidate := range names {
		if candidate == name {
			return true
		}
	}
	return false
}

func uploadResultFile(parentKey string, resp *uploadResponse, fallbackName string, size int64) *File {
	file := fileFromUpload(parentKey, resp, size)
	file.FileName = uploadFileName(resp.FileName, fallbackName)
	return file
}
