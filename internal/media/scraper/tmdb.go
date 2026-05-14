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
	"unicode/utf8"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	log "github.com/sirupsen/logrus"
)

// 年份正则（匹配 1900-2099）
var yearRegexp = regexp.MustCompile(`\b((?:19|20)\d{2})\b`)

// chineseRegexp 匹配包含中文字符的片段
var chineseRegexp = regexp.MustCompile(`[\p{Han}]`)

// 噪声词正则：发布组、编码、分辨率、音轨等无意义片段
// 清洗中文标题尾部常见的污染词
var noiseTokenRegexp = regexp.MustCompile(`(?i)(双语字幕|中字|国英|国粤|粤语|国语|英语|日语|韩语|HDTV|HR-HDTV|BluRay|BDRip|WEB-?DL|HDRip|DVDRip|REMUX|x264|x265|h264|h265|HEVC|AVC|AAC|AC3|DTS|FLAC|10bit|8bit|HDR|SDR|4K|2160P|1080P|720P|480P|完整版|未删减版)`)

// 中文数字到阿拉伯数字的简单映射，用于「钢铁侠三」=>「钢铁侠3」类的归一化
var cnNumMap = map[string]string{
	"〇": "0", "零": "0", "一": "1", "二": "2", "三": "3", "四": "4",
	"五": "5", "六": "6", "七": "7", "八": "8", "九": "9", "十": "10",
}

// normalizeTitle 对标题做模糊匹配前的归一化处理
// - 去除括号及其中内容
// - 去除版本/编码等噪声词
// - 合并多余空白
func normalizeTitle(s string) string {
	if s == "" {
		return s
	}
	// 去掉中英文括号包裹的内容
	bracketRe := regexp.MustCompile(`[\(（\[【][^\)）\]】]*[\)）\]】]`)
	s = bracketRe.ReplaceAllString(s, " ")
	// 去掉常见噪声词
	s = noiseTokenRegexp.ReplaceAllString(s, " ")
	// 替换分隔符为空格
	s = strings.NewReplacer(".", " ", "_", " ", "-", " ", "+", " ").Replace(s)
	// 合并多余空白
	s = regexp.MustCompile(`\s+`).ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// cnNumToArabic 将标题中的中文数字归一化为阿拉伯数字（仅做轻量处理）
func cnNumToArabic(s string) string {
	if s == "" {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if v, ok := cnNumMap[string(r)]; ok {
			b.WriteString(v)
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// parsedVideoTitle 解析后的视频标题信息
type parsedVideoTitle struct {
	EnglishTitle string // 英文标题（第一个中文片段之前、年份之前的部分）
	ChineseTitle string // 中文标题（第一个含中文的片段）
	Year         string // 年份
}

// parseVideoFileName 从视频文件名中提取标题和年份
// 规则：
//   - 文件名中允许使用 . / 空格 / _ / 的分隔多个字段
//   - 第一个含中文的片段即为中文标题
//   - 中文标题之前的非中文、非年份片段拼接为英文标题
//   - 第一个 1900-2099 之间的 4 位数字识别为年份
//
// 例如：
//
//	"Inception.2010.盗梦空间.双语字幕.HR-HDTV.AC3.1024X576.X264-"  -> {English:"Inception", Chinese:"盗梦空间", Year:"2010"}
//	"Iron.Man.3.2013.钢铁侠3.国英音轨.双语字幕.HR-HDTV.AC3.x264-"   -> {English:"Iron Man 3", Chinese:"钢铁侠3", Year:"2013"}
//	"The.Dark.Knight.2008.1080p.BluRay"                            -> {English:"The Dark Knight", Chinese:"", Year:"2008"}
//	"盗梦空间.2010.1080p.BluRay"                                    -> {English:"", Chinese:"盗梦空间", Year:"2010"}
//	"钢铁侠3 (2013).mkv"                                            -> {English:"", Chinese:"钢铁侠3", Year:"2013"}
//	"群星-演唱会现场.mp4"                                            -> {English:"", Chinese:"群星", Year:""}
func parseVideoFileName(fileName string) parsedVideoTitle {
	// 去掉扩展名（.mkv .mp4 .avi 等，扩展名长度 <= 5）
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		ext := strings.ToLower(fileName[idx:])
		if len(ext) <= 5 {
			fileName = fileName[:idx]
		}
	}

	// 去除中英文括号包裹的内容（先把里面的年份提出来，避免「钢铁侠3 (2013)」这类丢失年份）
	// 括号内若有 4 位年份，先把年份替换到外面
	bracketYearRe := regexp.MustCompile(`[\(（\[【]\s*((?:19|20)\d{2})\s*[\)）\]】]`)
	fileName = bracketYearRe.ReplaceAllString(fileName, " $1 ")
	// 再把剩余括号块替换成空格
	bracketRe := regexp.MustCompile(`[\(（\[【][^\)）\]】]*[\)）\]】]`)
	fileName = bracketRe.ReplaceAllString(fileName, " ")

	// 把多种分隔符统一成 "."，方便后续按 "." 解析
	// 注意：中文之间通常没分隔符，应保留；这里只把 "." 之外的分隔符换成 "."
	replacer := strings.NewReplacer(
		"_", ".",
		"+", ".",
	)
	fileName = replacer.Replace(fileName)

	// 把空格、" - " 也作为分隔符（但只在 ASCII 段之间使用，避免破坏中文），
	// 简化处理：直接把它们也变成 "."
	fileName = regexp.MustCompile(`[\s]+`).ReplaceAllString(fileName, ".")
	fileName = strings.ReplaceAll(fileName, "-", ".")

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

	// 对中文标题做尾部噪声清洗（去除「钢铁侠3 国英音轨」末尾的污染词，但保留主标题）
	if result.ChineseTitle != "" {
		result.ChineseTitle = stripTrailingNoise(result.ChineseTitle)
	}
	return result
}

// stripTrailingNoise 去除中文标题中嵌入的噪声词，保留主标题部分
// 如「钢铁侠3 国英音轨」-> 「钢铁侠3」
func stripTrailingNoise(s string) string {
	cleaned := noiseTokenRegexp.ReplaceAllString(s, " ")
	cleaned = regexp.MustCompile(`\s+`).ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return strings.TrimSpace(s)
	}
	return cleaned
}

const tmdbDefaultBaseURL = "https://api.themoviedb.org"
const tmdbImageBase = "https://image.tmdb.org/t/p/w500"

// TMDBScraper TMDB视频刮削器
type TMDBScraper struct {
	APIKey  string
	BaseURL string // 含 /3 版本路径的最终请求地址，例如 https://api.themoviedb.org/3
	client  *http.Client
}

// NewTMDBScraper 创建TMDB刮削器
// baseURL 为可自定义的 TMDB API 服务地址，为空时使用默认 https://api.themoviedb.org/3
//
// 支持的填写形式：
//  1. 留空：使用默认 https://api.themoviedb.org/3
//  2. 官方主域名（不带路径）：如 api.themoviedb.org / https://api.themoviedb.org，自动补全协议和 /3 版本路径
//  3. 反代/自定义完整路径：如 https://eo-tmd.example.com/api/tmdb/3，会原样使用，不再追加 /3
func NewTMDBScraper(apiKey string, baseURL string) *TMDBScraper {
	base := strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if base == "" {
		base = tmdbDefaultBaseURL
	}
	// 1. 协议头容错：用户没填 http/https 时自动补 https://
	if !strings.HasPrefix(base, "http://") && !strings.HasPrefix(base, "https://") {
		base = "https://" + base
	}
	// 2. 版本号容错：约定前端要求填写「域名 + API 前缀」
	//    - 官方：api.themoviedb.org              -> https://api.themoviedb.org/3
	//    - 反代：example.edgeone.run/api/tmdb/   -> https://example.edgeone.run/api/tmdb/3
	//    - 反代：example.edgeone.run/api/tmdb    -> https://example.edgeone.run/api/tmdb/3
	//    - 用户已填到 /3：原样保留，不重复追加
	if !strings.HasSuffix(base, "/3") {
		base = base + "/3"
	}
	return &TMDBScraper{
		APIKey:  apiKey,
		BaseURL: base,
		client:  &http.Client{Timeout: 15 * time.Second},
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
//
// endpoint：
//   - movie：/search/movie，参数参考 https://developer.themoviedb.org/reference/search-movie
//     支持 query / language / primary_release_year / year / region / include_adult / page
//   - tv：/search/tv，参数参考 https://developer.themoviedb.org/reference/search-tv
//     支持 query / language / first_air_date_year / year / include_adult / page
//   - multi：/search/multi，参数参考 https://developer.themoviedb.org/reference/search-multi
//     支持 query / language / include_adult / page / region（不支持按年份过滤）
//
// language 取值：zh-CN / en-US / 空字符串（不限定语言，让 TMDB 用所有别名匹配，模糊兜底）
// 注意：search_type=ngram 是早期废弃参数；TMDB 当前默认即支持子串/模糊匹配
func (s *TMDBScraper) doTMDBSearch(endpoint, query, year, language string) (*tmdbSearchResult, error) {
	if endpoint == "" {
		endpoint = "movie"
	}
	if strings.TrimSpace(query) == "" {
		return &tmdbSearchResult{}, nil
	}

	params := url.Values{}
	params.Set("api_key", s.APIKey)
	params.Set("query", query)
	params.Set("include_adult", "true")
	params.Set("page", "1")
	if language != "" {
		params.Set("language", language)
	}
	if year != "" {
		switch endpoint {
		case "movie":
			// movie 同时设置 year 和 primary_release_year，TMDB 文档建议优先使用 primary_release_year
			params.Set("year", year)
			params.Set("primary_release_year", year)
		case "tv":
			// tv 接口使用 first_air_date_year
			params.Set("first_air_date_year", year)
			params.Set("year", year)
		case "multi":
			// /search/multi 不支持按年份过滤；忽略 year 参数避免被反代/官方判为非法参数
		}
	}

	searchURL := fmt.Sprintf("%s/search/%s?%s", s.BaseURL, endpoint, params.Encode())

	// 输出脱敏后的请求 URL，便于排查刮削失败问题
	// （把 api_key 替换为 ***，避免日志泄漏密钥）
	log.Infof("[TMDB] 请求: %s", maskAPIKey(searchURL))

	resp, err := s.client.Get(searchURL)
	if err != nil {
		log.Warnf("[TMDB] 请求失败 url=%s err=%v", maskAPIKey(searchURL), err)
		return nil, fmt.Errorf("TMDB搜索请求失败: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	var result tmdbSearchResult
	if err := json.Unmarshal(body, &result); err != nil {
		// 附带响应状态码和响应体片段，便于排查 API 地址错误、被代理拦截等问题
		snippet := string(body)
		if len(snippet) > 200 {
			snippet = snippet[:200]
		}
		log.Warnf("[TMDB] 解析失败 status=%d url=%s body=%s", resp.StatusCode, maskAPIKey(searchURL), snippet)
		return nil, fmt.Errorf("TMDB搜索结果解析失败(status=%d, url=%s): %w, body=%s",
			resp.StatusCode, searchURL, err, snippet)
	}
	// 输出响应概要
	log.Infof("[TMDB] 响应 status=%d hits=%d url=%s", resp.StatusCode, len(result.Results), maskAPIKey(searchURL))

	// /search/movie /search/tv 返回的结果不带 media_type，需要根据 endpoint 补齐
	if endpoint == "movie" || endpoint == "tv" {
		for i := range result.Results {
			if result.Results[i].MediaType == "" {
				result.Results[i].MediaType = endpoint
			}
		}
	}
	return &result, nil
}

// maskAPIKey 把 URL 中的 api_key 参数遮蔽为 ***，避免日志输出真实密钥
var apiKeyMaskRe = regexp.MustCompile(`(api_key=)[^&]*`)

func maskAPIKey(rawURL string) string {
	return apiKeyMaskRe.ReplaceAllString(rawURL, "${1}***")
}

// searchAttempt 单次搜索尝试参数
type searchAttempt struct {
	endpoint string // multi / movie / tv
	query    string
	year     string
	language string
}

// buildTitleCandidates 根据原始标题构造一组候选搜索词（按优先级返回）
// 候选生成策略，从精确到模糊：
//  1. 原始标题（保留所有信息）
//  2. 归一化标题（去括号/噪声词/分隔符 -> 空格合并）
//  3. 中文数字 -> 阿拉伯数字（钢铁侠三 -> 钢铁侠3）
//  4. 阿拉伯数字 -> 中文数字（钢铁侠3 -> 钢铁侠三，少数 TMDB 别名用中文数字）
//  5. 去除尾部数字/罗马数字（钢铁侠3 -> 钢铁侠，盗梦空间2 -> 盗梦空间）
//  6. 拆分多个词，每个非短词作为独立候选
func buildTitleCandidates(title string) []string {
	if title == "" {
		return nil
	}
	seen := make(map[string]bool)
	var out []string
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			return
		}
		seen[s] = true
		out = append(out, s)
	}

	add(title)
	norm := normalizeTitle(title)
	add(norm)

	// 中文数字 <-> 阿拉伯数字 双向归一化
	arabic := cnNumToArabic(norm)
	add(arabic)
	add(arabicToCnNum(norm))

	// 去除尾部数字（系列编号），帮助匹配主作品
	// 例：「钢铁侠3」-> 「钢铁侠」、「Iron Man 3」-> 「Iron Man」
	trailingNumRe := regexp.MustCompile(`[\s]*[0-9０-９]+\s*$`)
	add(trailingNumRe.ReplaceAllString(arabic, ""))
	add(trailingNumRe.ReplaceAllString(norm, ""))

	// 去除尾部罗马数字（II / III / IV 等）
	trailingRomanRe := regexp.MustCompile(`(?i)\s+(?:I{1,3}|IV|V|VI{0,3}|IX|X)$`)
	add(trailingRomanRe.ReplaceAllString(norm, ""))

	// 若中文标题里夹杂了空格分隔的多个词，把每个非短词单独作为候选
	for _, w := range strings.Fields(norm) {
		if utf8.RuneCountInString(w) >= 2 {
			add(w)
		}
	}
	// 阿拉伯数字归一化版本同样拆词
	for _, w := range strings.Fields(arabic) {
		if utf8.RuneCountInString(w) >= 2 {
			add(w)
		}
	}
	return out
}

// arabicToCnNum 将标题中的单个阿拉伯数字归一化为中文数字（仅 0-9 的简单替换，10 以上不处理）
// 用于「钢铁侠3」-> 「钢铁侠三」，提升 TMDB 中文别名命中率
func arabicToCnNum(s string) string {
	if s == "" {
		return s
	}
	rev := map[rune]string{
		'0': "零", '1': "一", '2': "二", '3': "三", '4': "四",
		'5': "五", '6': "六", '7': "七", '8': "八", '9': "九",
	}
	// 仅当字符串包含中文且数字是单个字符（前后非数字）时才转换，避免 "2010" 年份被错误处理
	if !chineseRegexp.MatchString(s) {
		return s
	}
	runes := []rune(s)
	var b strings.Builder
	for i, r := range runes {
		if r >= '0' && r <= '9' {
			// 检查是否是孤立的单个数字（前后不是数字）
			prevDigit := i > 0 && runes[i-1] >= '0' && runes[i-1] <= '9'
			nextDigit := i+1 < len(runes) && runes[i+1] >= '0' && runes[i+1] <= '9'
			if !prevDigit && !nextDigit {
				b.WriteString(rev[r])
				continue
			}
		}
		b.WriteRune(r)
	}
	return b.String()
}

// searchWithFallback 带降级重试的TMDB搜索
//
// 端点优先级：movie → tv → multi（按 TMDB 官方文档，专用接口对中英文支持都更好）
// 整体策略（命中即停）：
//  1. 同一标题先按 movie/tv 精搜（带年份），再 movie/tv 不带年份，最后 multi 兜底
//  2. 中文标题（zh-CN）优先，英文标题（en-US）次之
//  3. 最后还有一次「不指定 language」的 multi 兜底，处理 TMDB 别名只在某语言下注册的情况
//
// 这样设计的好处：
//   - movie/tv 接口对 query 有更精确的字段匹配（title/original_title/name 等），命中率高
//   - movie 带 primary_release_year，能避免同名作品/系列误匹配
//   - multi 兜底覆盖电影/电视剧之外的边角情况
func (s *TMDBScraper) searchWithFallback(parsed parsedVideoTitle) (*tmdbSearchResult, error) {
	// 入口日志：输出解析出的标题和年份，方便排查"为什么没搜到"
	log.Infof("[TMDB] 开始搜索 chinese=%q english=%q year=%q",
		parsed.ChineseTitle, parsed.EnglishTitle, parsed.Year)

	var attempts []searchAttempt

	addGroup := func(title, lang string) {
		if title == "" {
			return
		}
		cands := buildTitleCandidates(title)
		// 1) movie + tv 带年份精搜（年份是最强的去歧义信号）
		if parsed.Year != "" {
			for _, q := range cands {
				attempts = append(attempts,
					searchAttempt{"movie", q, parsed.Year, lang},
					searchAttempt{"tv", q, parsed.Year, lang},
				)
			}
		}
		// 2) movie + tv 不带年份模糊搜
		for _, q := range cands {
			attempts = append(attempts,
				searchAttempt{"movie", q, "", lang},
				searchAttempt{"tv", q, "", lang},
			)
		}
		// 3) multi 兜底（不带 year，TMDB multi 不支持年份过滤）
		for _, q := range cands {
			attempts = append(attempts, searchAttempt{"multi", q, "", lang})
		}
	}

	// 中文标题优先（zh-CN）
	addGroup(parsed.ChineseTitle, "zh-CN")
	// 英文标题兜底（en-US）
	addGroup(parsed.EnglishTitle, "en-US")

	// 最后一轮：不指定 language，对中文标题做多语言别名兜底
	// 这能匹配上 TMDB 中只在原始语言下注册了别名的作品
	if parsed.ChineseTitle != "" {
		for _, q := range buildTitleCandidates(parsed.ChineseTitle) {
			attempts = append(attempts,
				searchAttempt{"movie", q, "", ""},
				searchAttempt{"tv", q, "", ""},
				searchAttempt{"multi", q, "", ""},
			)
		}
	}
	if parsed.EnglishTitle != "" {
		for _, q := range buildTitleCandidates(parsed.EnglishTitle) {
			attempts = append(attempts,
				searchAttempt{"movie", q, "", ""},
				searchAttempt{"tv", q, "", ""},
				searchAttempt{"multi", q, "", ""},
			)
		}
	}

	if len(attempts) == 0 {
		return nil, fmt.Errorf("无法从文件名中提取有效标题")
	}

	// 去重，避免重复请求
	type key struct{ ep, q, y, l string }
	done := make(map[key]bool)

	var lastErr error
	for _, attempt := range attempts {
		k := key{attempt.endpoint, attempt.query, attempt.year, attempt.language}
		if done[k] {
			continue
		}
		done[k] = true

		result, err := s.doTMDBSearch(attempt.endpoint, attempt.query, attempt.year, attempt.language)
		if err != nil {
			// 网络/解析错误不立即终止，记录后继续尝试下一个候选
			lastErr = err
			continue
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
	if lastErr != nil {
		return nil, fmt.Errorf("TMDB未找到匹配结果: %s (last err: %v)", titleInfo, lastErr)
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
	log.Infof("[TMDB] 开始刮削 file=%q parsed={chinese=%q english=%q year=%q} baseURL=%s",
		item.FileName, parsed.ChineseTitle, parsed.EnglishTitle, parsed.Year, s.BaseURL)

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
		s.BaseURL, mediaType, first.ID, s.APIKey)
	log.Infof("[TMDB] 详情请求: %s", maskAPIKey(detailURL))

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