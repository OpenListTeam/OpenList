package github_releases

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
)

// 发送 GET 请求
func (d *GithubReleases) GetRequest(url string) (*resty.Response, error) {
	req := base.RestyClient.R()
	req.SetHeader("Accept", "application/vnd.github+json")
	req.SetHeader("X-GitHub-Api-Version", "2022-11-28")
	if d.Addition.Token != "" {
		req.SetHeader("Authorization", fmt.Sprintf("Bearer %s", d.Addition.Token))
	}
	res, err := req.Get(url)
	if err != nil {
		return nil, err
	}
	if res.StatusCode() != 200 {
		return nil, fmt.Errorf("github api error: status %d", res.StatusCode())
	}
	return res, nil
}

// 解析挂载结构
func (d *GithubReleases) ParseRepos(text string) ([]MountPoint, error) {
	lines := strings.Split(text, "\n")
	points := make([]MountPoint, 0)
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, ":")
		path, repo := "", ""
		if len(parts) == 1 {
			path = "/"
			repo = parts[0]
		} else if len(parts) == 2 {
			path = fmt.Sprintf("/%s", strings.Trim(parts[0], "/"))
			repo = parts[1]
		} else {
			return nil, fmt.Errorf("invalid format: %s", line)
		}

		points = append(points, MountPoint{
			Point: path,
			Repo:  repo,
		})
	}
	d.points = points
	return points, nil
}

// 获取下一级目录
func GetNextDir(wholePath string, basePath string) string {
	basePath = fmt.Sprintf("%s/", strings.TrimRight(basePath, "/"))
	if !strings.HasPrefix(wholePath, basePath) {
		return ""
	}
	remainingPath := strings.TrimLeft(strings.TrimPrefix(wholePath, basePath), "/")
	if remainingPath != "" {
		parts := strings.Split(remainingPath, "/")
		nextDir := parts[0]
		if strings.HasPrefix(wholePath, strings.TrimRight(basePath, "/")+"/"+nextDir) {
			return nextDir
		}
	}
	return ""
}

// getLatestRelease 获取最新 release
func (d *GithubReleases) getLatestRelease(repo string) (*Release, error) {
	resp, err := d.GetRequest("https://api.github.com/repos/" + repo + "/releases/latest")
	if err != nil {
		return nil, err
	}
	release := new(Release)
	if err := json.Unmarshal(resp.Body(), release); err != nil {
		return nil, err
	}
	return release, nil
}

// getAllReleases 获取所有 releases（支持自动翻页）
func (d *GithubReleases) getAllReleases(repo string) ([]Release, error) {
	perPage := d.Addition.PerPage
	if perPage < 1 {
		perPage = 30
	} else if perPage > 100 {
		perPage = 100
	}

	maxPage := d.Addition.MaxPage
	if maxPage < 0 {
		maxPage = 0
	}

	allReleases := make([]Release, 0)
	page := 1

	for {
		url := fmt.Sprintf("https://api.github.com/repos/%s/releases?per_page=%d&page=%d", repo, perPage, page)
		resp, err := d.GetRequest(url)
		if err != nil {
			return nil, err
		}

		releases := make([]Release, 0)
		if err := json.Unmarshal(resp.Body(), &releases); err != nil {
			return nil, err
		}

		if len(releases) == 0 {
			break
		}

		allReleases = append(allReleases, releases...)

		// 达到最大页数限制
		if maxPage > 0 && page >= maxPage {
			break
		}

		// 如果返回数量小于 perPage，说明是最后一页
		if len(releases) < perPage {
			break
		}

		page++
	}

	return allReleases, nil
}

// fetchRepoFiles 获取仓库根目录文件列表
func (d *GithubReleases) fetchRepoFiles(repo string) ([]FileInfo, error) {
	resp, err := d.GetRequest("https://api.github.com/repos/" + repo + "/contents")
	if err != nil {
		return nil, err
	}
	files := make([]FileInfo, 0)
	if err := json.Unmarshal(resp.Body(), &files); err != nil {
		return nil, err
	}
	return files, nil
}
