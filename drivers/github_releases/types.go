package github_releases

import (
	"path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// MountPoint 表示一个仓库挂载点
type MountPoint struct {
	Point string // 挂载点路径
	Repo  string // 仓库名 owner/repo
}

// Release 转为 File 列表
func releaseToFiles(point string, release *Release) []File {
	if release == nil {
		return nil
	}
	files := make([]File, 0, len(release.Assets))
	for _, asset := range release.Assets {
		files = append(files, File{
			Path:     path.Join(point, asset.Name),
			FileName: asset.Name,
			Size:     asset.Size,
			Type:     "file",
			UpdateAt: asset.UpdatedAt,
			CreateAt: asset.CreatedAt,
			Url:      asset.BrowserDownloadUrl,
		})
	}
	return files
}

// 计算 release 的 asset 总大小
func releaseSize(release *Release) int64 {
	if release == nil {
		return 0
	}
	size := int64(0)
	for _, asset := range release.Assets {
		size += asset.Size
	}
	return size
}

// Releases 列表转为版本目录 File 列表
func releasesToVersionDirs(point string, releases []Release) []File {
	files := make([]File, 0, len(releases))
	for _, release := range releases {
		files = append(files, File{
			Path:     path.Join(point, release.TagName),
			FileName: release.TagName,
			Size:     releaseSize(&release),
			Type:     "dir",
			UpdateAt: release.PublishedAt,
			CreateAt: release.CreatedAt,
			Url:      release.HtmlUrl,
		})
	}
	return files
}

// 根据 tagName 查找 release 的 asset 文件列表
func releaseAssetsByTag(point, tagName string, releases []Release) []File {
	for _, item := range releases {
		if item.TagName == tagName {
			files := make([]File, 0, len(item.Assets))
			for _, asset := range item.Assets {
				files = append(files, File{
					Path:     path.Join(point, tagName, asset.Name),
					FileName: asset.Name,
					Size:     asset.Size,
					Type:     "file",
					UpdateAt: asset.UpdatedAt,
					CreateAt: asset.CreatedAt,
					Url:      asset.BrowserDownloadUrl,
				})
			}
			return files
		}
	}
	return nil
}

// 根据 tagName 计算 asset 总大小
func releasesSizeByTag(releases []Release, tagName string) int64 {
	for _, item := range releases {
		if item.TagName == tagName {
			return releaseSize(&item)
		}
	}
	return 0
}

// 计算所有 releases 的 asset 总大小
func releasesTotalSize(releases []Release) int64 {
	size := int64(0)
	for _, release := range releases {
		size += releaseSize(&release)
	}
	return size
}

// Source code 文件
func sourceCodeFiles(point string, release *Release) []File {
	if release == nil {
		return nil
	}
	return []File{
		{
			Path:     path.Join(point, "Source code (zip)"),
			FileName: "Source code (zip)",
			Size:     1,
			Type:     "file",
			UpdateAt: release.CreatedAt,
			CreateAt: release.CreatedAt,
			Url:      release.ZipballUrl,
		},
		{
			Path:     path.Join(point, "Source code (tar.gz)"),
			FileName: "Source code (tar.gz)",
			Size:     1,
			Type:     "file",
			UpdateAt: release.CreatedAt,
			CreateAt: release.CreatedAt,
			Url:      release.TarballUrl,
		},
	}
}

// 根据 tagName 获取 Source Code 文件
func sourceCodeFilesByTag(point string, releases []Release, tagName string) []File {
	for _, item := range releases {
		if item.TagName == tagName {
			return sourceCodeFiles(point, &item)
		}
	}
	return nil
}

// 仓库根目录下的 README/LICENSE 文件
func otherFiles(point string, fileInfos []FileInfo) []File {
	files := make([]File, 0)
	defaultTime := "1970-01-01T00:00:00Z"
	for _, file := range fileInfos {
		if file.Type == "dir" {
			continue
		}
		name := file.Name
		if strings.EqualFold(name, "README.md") || strings.HasPrefix(name, "LICENSE") {
			files = append(files, File{
				Path:     path.Join(point, file.Name),
				FileName: file.Name,
				Size:     file.Size,
				Type:     "file",
				UpdateAt: defaultTime,
				CreateAt: defaultTime,
				Url:      file.DownloadUrl,
			})
		}
	}
	return files
}

type File struct {
	Path     string // 文件路径
	FileName string // 文件名
	Size     int64  // 文件大小
	Type     string // 文件类型
	UpdateAt string // 更新时间 eg:"2025-01-27T16:10:16Z"
	CreateAt string // 创建时间
	Url      string // 下载链接
}

func (f File) GetHash() utils.HashInfo {
	return utils.HashInfo{}
}

func (f File) GetPath() string {
	return f.Path
}

func (f File) GetSize() int64 {
	return f.Size
}

func (f File) GetName() string {
	return f.FileName
}

func (f File) ModTime() time.Time {
	t, _ := time.Parse(time.RFC3339, f.CreateAt)
	return t
}

func (f File) CreateTime() time.Time {
	t, _ := time.Parse(time.RFC3339, f.CreateAt)
	return t
}

func (f File) IsDir() bool {
	return f.Type == "dir"
}

func (f File) GetID() string {
	return f.Url
}
