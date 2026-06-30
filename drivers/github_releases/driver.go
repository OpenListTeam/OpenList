package github_releases

import (
	"context"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

type GithubReleases struct {
	model.Storage
	Addition

	points []MountPoint
}

func (d *GithubReleases) Config() driver.Config {
	return config
}

func (d *GithubReleases) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *GithubReleases) Init(ctx context.Context) error {
	d.ParseRepos(d.Addition.RepoStructure)
	return nil
}

func (d *GithubReleases) Drop(ctx context.Context) error {
	return nil
}

func (d *GithubReleases) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files := make([]File, 0)
	path := fmt.Sprintf("/%s", strings.Trim(dir.GetPath(), "/"))

	for i := range d.points {
		point := &d.points[i]

		if !d.Addition.ShowAllVersion { // latest
			err := point.RequestRelease(d.GetRequest, args.Refresh)
			if err != nil {
				log.Warnf("failed to request release for %s: %v", point.Repo, err)
			}

			if point.Point == path { // 与仓库路径相同
				if point.Release == nil {
					if err != nil {
						return nil, fmt.Errorf("failed to get release for %s: %w", point.Repo, err)
					}
					return nil, fmt.Errorf("failed to get release for %s: unknown error", point.Repo)
				}
				files = append(files, point.GetLatestRelease()...)
				if d.Addition.ShowReadme {
					otherFiles, err := point.GetOtherFile(d.GetRequest, args.Refresh)
					if err != nil {
						return nil, fmt.Errorf("failed to get other files for %s: %w", point.Repo, err)
					}
					files = append(files, otherFiles...)
				}
				if d.Addition.ShowSourceCode {
					files = append(files, point.GetSourceCode()...)
				}
			} else if strings.HasPrefix(point.Point, path) { // 仓库目录的父目录
				nextDir := GetNextDir(point.Point, path)
				if nextDir == "" {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("failed to get release for %s: %w", point.Repo, err)
				}

				hasSameDir := false
				for index := range files {
					if files[index].GetName() == nextDir {
						hasSameDir = true
						files[index].Size += point.GetLatestSize()
						break
					}
				}
				if !hasSameDir {
					var updateAt, createAt string
					if point.Release != nil {
						updateAt = point.Release.PublishedAt
						createAt = point.Release.CreatedAt
					}
					files = append(files, File{
						Path:     stdpath.Join(path, nextDir),
						FileName: nextDir,
						Size:     point.GetLatestSize(),
						UpdateAt: updateAt,
						CreateAt: createAt,
						Type:     "dir",
						Url:      "",
					})
				}
			}
		} else { // all version
			err := point.RequestReleases(d.GetRequest, args.Refresh)
			if err != nil {
				log.Warnf("failed to request releases for %s: %v", point.Repo, err)
			}

			if point.Point == path { // 与仓库路径相同
				if point.Releases == nil {
					if err != nil {
						return nil, fmt.Errorf("failed to get releases for %s: %w", point.Repo, err)
					}
					return nil, fmt.Errorf("failed to get releases for %s: unknown error", point.Repo)
				}
				files = append(files, point.GetAllVersion()...)
				if d.Addition.ShowReadme {
					otherFiles, err := point.GetOtherFile(d.GetRequest, args.Refresh)
					if err != nil {
						return nil, fmt.Errorf("failed to get other files for %s: %w", point.Repo, err)
					}
					files = append(files, otherFiles...)
				}
			} else if strings.HasPrefix(point.Point, path) { // 仓库目录的父目录
				nextDir := GetNextDir(point.Point, path)
				if nextDir == "" {
					continue
				}
				if err != nil {
					return nil, fmt.Errorf("failed to get releases for %s: %w", point.Repo, err)
				}

				hasSameDir := false
				for index := range files {
					if files[index].GetName() == nextDir {
						hasSameDir = true
						files[index].Size += point.GetAllVersionSize()
						break
					}
				}
				if !hasSameDir {
					var updateAt, createAt string
					if point.Releases != nil && len(*point.Releases) > 0 {
						updateAt = (*point.Releases)[0].PublishedAt
						createAt = (*point.Releases)[0].CreatedAt
					}
					files = append(files, File{
						FileName: nextDir,
						Path:     stdpath.Join(path, nextDir),
						Size:     point.GetAllVersionSize(),
						UpdateAt: updateAt,
						CreateAt: createAt,
						Type:     "dir",
						Url:      "",
					})
				}
			} else if strings.HasPrefix(path, point.Point) { // 仓库目录的子目录
				tagName := GetNextDir(path, point.Point)
				if tagName == "" {
					continue
				}
				if point.Releases == nil {
					if err != nil {
						return nil, fmt.Errorf("failed to get releases for %s: %w", point.Repo, err)
					}
					return nil, fmt.Errorf("failed to get releases for %s: unknown error", point.Repo)
				}

				files = append(files, point.GetReleaseByTagName(tagName)...)

				if d.Addition.ShowSourceCode {
					files = append(files, point.GetSourceCodeByTagName(tagName)...)
				}
			}
		}
	}

	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return src, nil
	})
}

func (d *GithubReleases) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	url := file.GetID()
	gh_proxy := strings.TrimSpace(d.Addition.GitHubProxy)

	if gh_proxy != "" {
		url = strings.Replace(url, "https://github.com", gh_proxy, 1)
	}

	link := model.Link{
		URL:    url,
		Header: http.Header{},
	}
	return &link, nil
}

func (d *GithubReleases) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	// TODO create folder, optional
	return nil, errs.NotImplement
}

func (d *GithubReleases) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// TODO move obj, optional
	return nil, errs.NotImplement
}

func (d *GithubReleases) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	// TODO rename obj, optional
	return nil, errs.NotImplement
}

func (d *GithubReleases) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// TODO copy obj, optional
	return nil, errs.NotImplement
}

func (d *GithubReleases) Remove(ctx context.Context, obj model.Obj) error {
	// TODO remove obj, optional
	return errs.NotImplement
}
