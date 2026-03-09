package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

const discogsBaseURL = "https://api.discogs.com"

// DiscogsScraper Discogs音乐刮削器
type DiscogsScraper struct {
	Token  string
	client *http.Client
}

// NewDiscogsScraper 创建Discogs刮削器
func NewDiscogsScraper(token string) *DiscogsScraper {
	return &DiscogsScraper{
		Token:  token,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

type discogsSearchResult struct {
	Results []struct {
		ID          int      `json:"id"`
		Title       string   `json:"title"`
		Type        string   `json:"type"`
		Year        string   `json:"year"`
		Thumb       string   `json:"thumb"`
		CoverImage  string   `json:"cover_image"`
		Genre       []string `json:"genre"`
		Style       []string `json:"style"`
		ResourceURL string   `json:"resource_url"`
	} `json:"results"`
}

type discogsReleaseDetail struct {
	ID          int    `json:"id"`
	Title       string `json:"title"`
	Year        int    `json:"year"`
	CoverImage  string `json:"cover_image"`
	Notes       string `json:"notes"`
	Artists     []struct {
		Name string `json:"name"`
	} `json:"artists"`
	Genres []string `json:"genres"`
	Styles []string `json:"styles"`
	Tracklist []struct {
		Position string `json:"position"`
		Title    string `json:"title"`
		Duration string `json:"duration"`
	} `json:"tracklist"`
	Community struct {
		Rating struct {
			Average float32 `json:"average"`
		} `json:"rating"`
	} `json:"community"`
}

// parseMusicFileName 从音乐文件名中提取搜索关键词列表
// 规则：去掉扩展名后，按" - "或"-"分割，返回各部分作为候选搜索词
// 例如：
//
//	"周杰伦 - 七里香.mp3"  -> ["周杰伦 - 七里香", "周杰伦", "七里香"]
//	"Coldplay-Yellow.flac" -> ["Coldplay-Yellow", "Coldplay", "Yellow"]
//	"七里香.mp3"           -> ["七里香"]
func parseMusicFileName(fileName string) []string {
	// 去掉扩展名
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		ext := strings.ToLower(fileName[idx:])
		if len(ext) <= 5 {
			fileName = fileName[:idx]
		}
	}
	fileName = strings.TrimSpace(fileName)
	if fileName == "" {
		return nil
	}

	var queries []string
	// 完整文件名作为第一候选
	queries = append(queries, fileName)

	// 按" - "（带空格）分割
	parts := strings.SplitN(fileName, " - ", 2)
	if len(parts) == 2 {
		before := strings.TrimSpace(parts[0])
		after := strings.TrimSpace(parts[1])
		if before != "" {
			queries = append(queries, before)
		}
		if after != "" {
			queries = append(queries, after)
		}
	} else {
		// 按"-"（不带空格）分割
		parts = strings.SplitN(fileName, "-", 2)
		if len(parts) == 2 {
			before := strings.TrimSpace(parts[0])
			after := strings.TrimSpace(parts[1])
			if before != "" {
				queries = append(queries, before)
			}
			if after != "" {
				queries = append(queries, after)
			}
		}
	}

	return queries
}

// doDiscogsSearch 执行单次Discogs搜索
func (s *DiscogsScraper) doDiscogsSearch(query string) (*discogsSearchResult, error) {
	searchURL := fmt.Sprintf("%s/database/search?q=%s&type=release&token=%s",
		discogsBaseURL, url.QueryEscape(query), s.Token)

	req, _ := http.NewRequest("GET", searchURL, nil)
	req.Header.Set("User-Agent", "OpenList/4.0 +https://github.com/OpenListTeam/OpenList")

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("Discogs搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result discogsSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("Discogs搜索结果解析失败: %w", err)
	}
	return &result, nil
}

// buildMusicQuery 根据 MediaItem 已有字段拼接 Discogs 搜索词
// 优先级：专辑名 > ScrapedName > 文件名解析
// 若有歌手或年份信息，则附加到搜索词中以提高精准度
func buildMusicQuery(base string, item *model.MediaItem) string {
	q := base
	// 附加歌手
	if item.AlbumArtist != "" {
		q = item.AlbumArtist + " " + q
	}
	// 附加年份（取 ReleaseDate 前4位）
	if len(item.ReleaseDate) >= 4 {
		q = q + " " + item.ReleaseDate[:4]
	}
	return strings.TrimSpace(q)
}

// ScrapeMusic 刮削音乐/专辑信息
func (s *DiscogsScraper) ScrapeMusic(item *model.MediaItem) error {
	if s.Token == "" {
		return fmt.Errorf("Discogs Token 未配置")
	}

	// 构建候选搜索词列表
	var queries []string
	if item.AlbumName != "" {
		queries = append(queries, buildMusicQuery(item.AlbumName, item))
		// 同时保留不带附加信息的纯专辑名作为降级候选
		if item.AlbumArtist != "" || len(item.ReleaseDate) >= 4 {
			queries = append(queries, item.AlbumName)
		}
	} else if item.ScrapedName != "" {
		queries = append(queries, buildMusicQuery(item.ScrapedName, item))
		if item.AlbumArtist != "" || len(item.ReleaseDate) >= 4 {
			queries = append(queries, item.ScrapedName)
		}
	} else {
		// 从文件名解析，按"-"分割分别搜索
		baseQueries := parseMusicFileName(item.FileName)
		for _, bq := range baseQueries {
			enhanced := buildMusicQuery(bq, item)
			if enhanced != bq {
				// 先放带附加信息的精准词，再放原始词作为降级
				queries = append(queries, enhanced)
			}
			queries = append(queries, bq)
		}
	}

	if len(queries) == 0 {
		return fmt.Errorf("无法从文件名中提取有效搜索词")
	}

	// 依次尝试各候选词，找到结果即停止（模糊匹配降级）
	var searchResult *discogsSearchResult
	var lastQuery string
	for _, q := range queries {
		result, err := s.doDiscogsSearch(q)
		if err != nil {
			return err
		}
		lastQuery = q
		if len(result.Results) > 0 {
			searchResult = result
			break
		}
	}

	if searchResult == nil || len(searchResult.Results) == 0 {
		return fmt.Errorf("Discogs未找到匹配结果: %s", lastQuery)
	}

	first := searchResult.Results[0]

	// 获取详情
	detailURL := fmt.Sprintf("%s/releases/%d?token=%s", discogsBaseURL, first.ID, s.Token)
	detailReq, _ := http.NewRequest("GET", detailURL, nil)
	detailReq.Header.Set("User-Agent", "OpenList/4.0 +https://github.com/OpenListTeam/OpenList")

	detailResp, err := s.client.Do(detailReq)
	if err != nil {
		return fmt.Errorf("Discogs详情请求失败: %w", err)
	}
	defer detailResp.Body.Close()

	detailBody, _ := io.ReadAll(detailResp.Body)
	var detail discogsReleaseDetail
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		return fmt.Errorf("Discogs详情解析失败: %w", err)
	}

	// 填充字段：已有值的字段不覆盖，仅补充空缺
	// Discogs 的 title 就是专辑名（不是 "Artist - Album" 格式）
	if detail.Title != "" {
		if item.AlbumName == "" {
			item.AlbumName = detail.Title
		}
		if item.ScrapedName == "" {
			item.ScrapedName = detail.Title
		}
	}

	if item.ReleaseDate == "" {
		if detail.Year > 0 {
			item.ReleaseDate = fmt.Sprintf("%d-01-01", detail.Year)
		} else if first.Year != "" {
			item.ReleaseDate = first.Year + "-01-01"
		}
	}

	item.Rating = detail.Community.Rating.Average
	item.Plot = detail.Notes
	item.ExternalID = fmt.Sprintf("discogs:%d", detail.ID)

	// 封面（存储 URL，前端直接展示）
	if item.Cover == "" {
		if detail.CoverImage != "" {
			item.Cover = detail.CoverImage
		} else if first.CoverImage != "" {
			item.Cover = first.CoverImage
		} else if first.Thumb != "" {
			item.Cover = first.Thumb
		}
	}

	// 类型
	if item.Genre == "" {
		genres := append(detail.Genres, detail.Styles...)
		item.Genre = strings.Join(genres, ",")
	}

	// 艺术家：仅在 Authors 和 AlbumArtist 均为空时才填充（ID3已有值则保留）
	artists := make([]string, 0, len(detail.Artists))
	for _, a := range detail.Artists {
		artists = append(artists, a.Name)
	}
	if len(artists) > 0 {
		if item.Authors == "" {
			authorsJSON, _ := json.Marshal(artists)
			item.Authors = string(authorsJSON)
		}
		if item.AlbumArtist == "" {
			item.AlbumArtist = artists[0]
		}
	}

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}
