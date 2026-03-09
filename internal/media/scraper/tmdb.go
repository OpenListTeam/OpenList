package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// 年份正则（匹配 1900-2099）
var yearRegexp = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)

// chineseRegexp 匹配包含中文字符的片段
var chineseRegexp = regexp.MustCompile(`[\p{Han}]`)

// parsedVideoTitle 解析后的视频标题信息
type parsedVideoTitle struct {
	EnglishTitle string // 英文标题（第一个中文片段之前、年份之前的部分）
	ChineseTitle string // 中文标题（第一个含中文的片段）
	Year         string // 年份
}

// parseVideoFileName 从视频文件名中提取标题和年份
// 规则：按"."分割，第一个中文之前的是英文，第一个中文是中文标题，后面都是参数，中文标题之前是年份
// 例如：
//
//	"Inception.2010.盗梦空间.双语字幕.HR-HDTV.AC3.1024X576.X264-" -> {English:"Inception", Chinese:"盗梦空间", Year:"2010"}
//	"Iron.Man.3.2013.钢铁侠3.国英音轨.双语字幕.HR-HDTV.AC3.1024X576.x264-" -> {English:"Iron Man 3", Chinese:"钢铁侠3", Year:"2013"}
//	"The.Dark.Knight.2008.1080p.BluRay" -> {English:"The Dark Knight", Chinese:"", Year:"2008"}
func parseVideoFileName(fileName string) parsedVideoTitle {
	// 去掉扩展名（.mkv .mp4 .avi 等，扩展名长度 <= 5）
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		ext := strings.ToLower(fileName[idx:])
		if len(ext) <= 5 {
			fileName = fileName[:idx]
		}
	}

	// 按"."分割各字段
	parts := strings.Split(fileName, ".")

	var result parsedVideoTitle
	var englishParts []string
	foundChinese := false
	foundYear := false

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// 已找到中文标题后，后面都是参数，跳过
		if foundChinese {
			continue
		}

		// 检测是否含中文
		if chineseRegexp.MatchString(p) {
			// 第一个中文片段即为中文标题
			result.ChineseTitle = p
			foundChinese = true
			continue
		}

		// 检测是否为年份（1900-2099）
		if yearRegexp.MatchString(p) && !foundYear {
			result.Year = yearRegexp.FindString(p)
			foundYear = true
			// 年份本身不加入英文标题
			continue
		}

		// 年份之前的非中文片段加入英文标题
		if !foundYear {
			englishParts = append(englishParts, p)
		}
		// 年份之后、中文之前的片段（如果有）忽略，通常是噪音
	}

	result.EnglishTitle = strings.Join(englishParts, " ")
	return result
}

const tmdbBaseURL = "https://api.themoviedb.org/3"
const tmdbImageBase = "https://image.tmdb.org/t/p/w500"

// TMDBScraper TMDB视频刮削器
type TMDBScraper struct {
	APIKey string
	client *http.Client
}

// NewTMDBScraper 创建TMDB刮削器
func NewTMDBScraper(apiKey string) *TMDBScraper {
	return &TMDBScraper{
		APIKey: apiKey,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// tmdbSearchResult TMDB搜索结果
type tmdbSearchResult struct {
	Results []struct {
		ID           int     `json:"id"`
		Title        string  `json:"title"`
		Name         string  `json:"name"` // 电视剧用name
		Overview     string  `json:"overview"`
		PosterPath   string  `json:"poster_path"`
		ReleaseDate  string  `json:"release_date"`
		FirstAirDate string  `json:"first_air_date"` // 电视剧
		VoteAverage  float32 `json:"vote_average"`
		MediaType    string  `json:"media_type"`
		GenreIDs     []int   `json:"genre_ids"`
	} `json:"results"`
}

// tmdbMovieDetail TMDB电影详情
type tmdbMovieDetail struct {
	ID          int     `json:"id"`
	Title       string  `json:"title"`
	Overview    string  `json:"overview"`
	PosterPath  string  `json:"poster_path"`
	ReleaseDate string  `json:"release_date"`
	VoteAverage float32 `json:"vote_average"`
	Genres      []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	} `json:"genres"`
	Credits struct {
		Cast []struct {
			Name string `json:"name"`
		} `json:"cast"`
	} `json:"credits"`
}

// doTMDBSearch 执行一次TMDB搜索请求
func (s *TMDBScraper) doTMDBSearch(query, year string) (*tmdbSearchResult, error) {
	searchURL := fmt.Sprintf("%s/search/multi?api_key=%s&query=%s&language=zh-CN&search_type=ngram",
		tmdbBaseURL, s.APIKey, url.QueryEscape(query))
	if year != "" {
		searchURL += "&year=" + year
	}

	resp, err := s.client.Get(searchURL)
	if err != nil {
		return nil, fmt.Errorf("TMDB搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result tmdbSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		return nil, fmt.Errorf("TMDB搜索结果解析失败: %w", err)
	}
	return &result, nil
}

// searchWithFallback 带降级重试的TMDB搜索
// 策略：
//  1. 有中文标题时，先用中文标题 + 年份搜索，再用中文标题不带年份搜索
//  2. 有英文标题时，用英文标题 + 年份搜索，再用英文标题不带年份搜索
//  3. 全部搜索失败才返回错误
func (s *TMDBScraper) searchWithFallback(parsed parsedVideoTitle) (*tmdbSearchResult, error) {
	type searchAttempt struct {
		query string
		year  string
	}

	var attempts []searchAttempt

	// 中文标题优先
	if parsed.ChineseTitle != "" {
		if parsed.Year != "" {
			attempts = append(attempts, searchAttempt{parsed.ChineseTitle, parsed.Year})
		}
		attempts = append(attempts, searchAttempt{parsed.ChineseTitle, ""})
	}

	// 英文标题兜底
	if parsed.EnglishTitle != "" {
		if parsed.Year != "" {
			attempts = append(attempts, searchAttempt{parsed.EnglishTitle, parsed.Year})
		}
		attempts = append(attempts, searchAttempt{parsed.EnglishTitle, ""})
	}

	if len(attempts) == 0 {
		return nil, fmt.Errorf("无法从文件名中提取有效标题")
	}

	for _, attempt := range attempts {
		result, err := s.doTMDBSearch(attempt.query, attempt.year)
		if err != nil {
			return nil, err
		}
		if len(result.Results) > 0 {
			return result, nil
		}
	}

	// 构造友好的错误信息
	titleInfo := parsed.ChineseTitle
	if titleInfo == "" {
		titleInfo = parsed.EnglishTitle
	}
	return nil, fmt.Errorf("TMDB未找到匹配结果: %s", titleInfo)
}

// ScrapeVideo 刮削视频信息
func (s *TMDBScraper) ScrapeVideo(item *model.MediaItem) error {
	if s.APIKey == "" {
		return fmt.Errorf("TMDB API Key 未配置")
	}

	// 始终从文件名中解析出标题和年份（ScrapedName 是刮削结果字段，不作为搜索输入）
	parsed := parseVideoFileName(item.FileName)

	// 搜索策略：中文标题优先，英文标题兜底，都搜不到才失败
	searchResult, err := s.searchWithFallback(parsed)
	if err != nil {
		return err
	}

	// 取第一个结果
	first := searchResult.Results[0]
	mediaType := first.MediaType
	if mediaType == "" {
		mediaType = "movie"
	}

	// 获取详情
	detailURL := fmt.Sprintf("%s/%s/%d?api_key=%s&language=zh-CN&append_to_response=credits",
		tmdbBaseURL, mediaType, first.ID, s.APIKey)

	detailResp, err := s.client.Get(detailURL)
	if err != nil {
		return fmt.Errorf("TMDB详情请求失败: %w", err)
	}
	defer detailResp.Body.Close()

	detailBody, _ := io.ReadAll(detailResp.Body)
	var detail tmdbMovieDetail
	if err := json.Unmarshal(detailBody, &detail); err != nil {
		return fmt.Errorf("TMDB详情解析失败: %w", err)
	}

	// 填充字段
	title := detail.Title
	if title == "" {
		title = first.Name
	}
	item.ScrapedName = title
	item.Plot = detail.Overview
	item.Rating = detail.VoteAverage
	item.ExternalID = fmt.Sprintf("tmdb:%d", detail.ID)
	item.VideoType = mediaType

	releaseDate := detail.ReleaseDate
	if releaseDate == "" {
		releaseDate = first.FirstAirDate
	}
	item.ReleaseDate = releaseDate

	if detail.PosterPath != "" {
		item.Cover = tmdbImageBase + detail.PosterPath
	}

	// 类型
	genres := make([]string, 0, len(detail.Genres))
	for _, g := range detail.Genres {
		genres = append(genres, g.Name)
	}
	item.Genre = strings.Join(genres, ",")

	// 演员（取前10个）
	actors := make([]string, 0)
	for i, cast := range detail.Credits.Cast {
		if i >= 10 {
			break
		}
		actors = append(actors, cast.Name)
	}
	authorsJSON, _ := json.Marshal(actors)
	item.Authors = string(authorsJSON)

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}