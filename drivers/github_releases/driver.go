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

		if !d.Addition.ShowAllVersion {
			// latest version mode
			release, err := d.getLatestRelease(point.Repo)
			if err != nil {
				log.Warnf("failed to request release for %s: %v", point.Repo, err)
				continue
			}
			if release == nil {
				continue
			}

			if point.Point == path {
				// 当前目录就是仓库挂载点
				files = append(files, releaseToFiles(point.Point, release)...)
				if d.Addition.ShowReadme {
					other, err := d.fetchRepoFiles(point.Repo)
					if err == nil {
						files = append(files, otherFiles(point.Point, other)...)
					} else {
						log.Warnf("failed to get other files for %s: %v", point.Repo, err)
					}
				}
				if d.Addition.ShowSourceCode {
					files = append(files, sourceCodeFiles(point.Point, release)...)
				}
			} else if strings.HasPrefix(point.Point, path) {
				// 仓库目录的父目录，需要聚合显示
				nextDir := GetNextDir(point.Point, path)
				if nextDir == "" {
					continue
				}

				hasSameDir := false
				for index := range files {
					if files[index].GetName() == nextDir {
						hasSameDir = true
						files[index].Size += releaseSize(release)
						break
					}
				}
				if !hasSameDir {
					files = append(files, File{
						Path:     stdpath.Join(path, nextDir),
						FileName: nextDir,
						Size:     releaseSize(release),
						UpdateAt: release.PublishedAt,
						CreateAt: release.CreatedAt,
						Type:     "dir",
						Url:      "",
					})
				}
			}
		} else {
			// all versions mode
			releases, err := d.getAllReleases(point.Repo)
			if err != nil {
				log.Warnf("failed to request releases for %s: %v", point.Repo, err)
				continue
			}
			if len(releases) == 0 {
				// no releases but may still have repo files (e.g. README)
				if point.Point == path && d.Addition.ShowReadme {
					other, err := d.fetchRepoFiles(point.Repo)
					if err == nil {
						files = append(files, otherFiles(point.Point, other)...)
					} else {
						log.Warnf("failed to get other files for %s: %v", point.Repo, err)
					}
				}
				continue
			}

			if point.Point == path {
				// 当前目录就是仓库挂载点
				files = append(files, releasesToVersionDirs(point.Point, releases)...)
				if d.Addition.ShowReadme {
					other, err := d.fetchRepoFiles(point.Repo)
					if err == nil {
						files = append(files, otherFiles(point.Point, other)...)
					} else {
						log.Warnf("failed to get other files for %s: %v", point.Repo, err)
					}
				}
			} else if strings.HasPrefix(point.Point, path) {
				// 仓库目录的父目录
				nextDir := GetNextDir(point.Point, path)
				if nextDir == "" {
					continue
				}

				hasSameDir := false
				for index := range files {
					if files[index].GetName() == nextDir {
						hasSameDir = true
						files[index].Size += releasesTotalSize(releases)
						break
					}
				}
				if !hasSameDir {
					files = append(files, File{
						FileName: nextDir,
						Path:     stdpath.Join(path, nextDir),
						Size:     releasesTotalSize(releases),
						UpdateAt: releases[0].PublishedAt,
						CreateAt: releases[0].CreatedAt,
						Type:     "dir",
						Url:      "",
					})
				}
			} else if strings.HasPrefix(path, point.Point) {
				// 仓库目录的子目录（某个版本）
				tagName := GetNextDir(path, point.Point)
				if tagName == "" {
					continue
				}

				files = append(files, releaseAssetsByTag(point.Point, tagName, releases)...)

				if d.Addition.ShowSourceCode {
					files = append(files, sourceCodeFilesByTag(point.Point, releases, tagName)...)
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
