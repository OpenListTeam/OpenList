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
//
// 解析时除了主中文标题/英文标题之外，可能还会从文件名中识别到其他中文片段
// （比如同时存在「[钢铁侠]」和「Iron Man」，或多段中文），这些都作为兜底候选保留。
// 主中文/英文标题用于「常规」搜索路径；额外候选会在主候选搜不到时再尝试。
type parsedVideoTitle struct {
	EnglishTitle       string   // 英文标题（第一个中文片段之前、年份之前的部分）
	ChineseTitle       string   // 中文标题（最优中文片段）
	ExtraChineseTitles []string // 其他中文片段，作为兜底候选（如方括号外又出现的中文标题、副标题等）
	Year               string   // 年份
}

// leadingSerialSpaceRe 匹配开头是 "数字 + 空格"（如 "01 [钢铁侠]"、"19 [复仇者联盟3：无限战争]"）
// 数字 + 空格 几乎可以确定是序号（合法片名极少以"数字 + 空格"开头）
// 只匹配 1-3 位数字，避免把年份(2008) 或 4 位以上数字当成序号
var leadingSerialSpaceRe = regexp.MustCompile(`^\s*\d{1,3}\s+`)

// leadingSerialDotChineseRe 匹配开头是 "数字 + 点/-/_/、 + 中文" 的形式
// 这种形式既可能是"序号 + 中文片名"，也可能是"数字片名 + 中文别名"，比如：
//
//	"169.谍影重重3"           -> 序号 + 中文片名
//	"1.漫威短片.47号物品"     -> 序号 + 中文片名
//	"300.斯巴达勇士..."        -> 数字片名 + 中文别名（300 是片名核心）
//	"36总局" 这种中文里就含数字的也算
//
// 启发式判定：
//   - 1-2 位数字（1-99）：序号概率高，删掉数字
//   - 3 位数字（100-999）：片名概率高，保留数字
//   - 4 位以上：在外层正则就被 \d{1,3} 排除
var leadingSerialDotChineseRe = regexp.MustCompile(`^\s*(\d{1,3})\s*[.\-_、]\s*(\p{Han})`)

// stripLeadingSerial 去除文件名开头的序号前缀，返回剥离后的文件名
//
// 处理两种序号情况：
//  1. "数字 + 空格"：如 "01 [钢铁侠]Iron.Man..." -> "[钢铁侠]Iron.Man..."
//  2. "1-2 位数字 + 点/-/_/、 + 中文"：如 "1.漫威短片..." -> "漫威短片..."、"169.谍影重重3" -> "谍影重重3"
//
// 不视为序号（保留原样）：
//   - "30.Days.Of.Night..."（数字 + 点 + 英文，数字是片名一部分）
//   - "300.Rise.Of.An.Empire..."（同上）
//   - "300.斯巴达勇士..."（3 位数字 + 点 + 中文，可能是片名 "300"）
//   - "3096.Days.2013..."（4 位数字直接被排除）
//
// 被剥离的序号本身不是片名关键词，不需要回填。
func stripLeadingSerial(s string) string {
	// 1) "数字 + 空格"：稳定删除（序号信号最强）
	if loc := leadingSerialSpaceRe.FindStringIndex(s); loc != nil && loc[0] == 0 {
		return strings.TrimSpace(s[loc[1]:])
	}
	// 2) "1-2 位数字 + 分隔符 + 中文"：视为序号删除；3 位数字保守保留
	if m := leadingSerialDotChineseRe.FindStringSubmatchIndex(s); m != nil && m[0] == 0 {
		// m[2]:m[3] 是数字捕获组的索引；m[4]:m[5] 是中文字符
		numStr := s[m[2]:m[3]]
		// 3 位数字保守保留（避免删除 "300.斯巴达勇士" 中的 "300"）
		if len(numStr) >= 3 {
			return s
		}
		// 1-2 位数字视为序号删除，从中文字符位置开始保留
		return strings.TrimSpace(s[m[4]:])
	}
	return s
}

// noiseTokenWholeRe 判断一个完整片段是否为“纯噪声词”（完全匹配，不包含其他字符）
// 覆盖常见发布组/字幕组/编码/音轨/分辨率/语言标记等。被识别为噪声的片段不会进入 EnglishTitle。
// 例："HR-HDTV"、"AC3"、"x264"、"X264"、"1024x576"、"2160p"、"BOBO"(发布组) 等
var noiseTokenWholeRe = regexp.MustCompile(`(?i)^(?:` +
	// 字幕/语言标记
	`双语字幕|中字|国英|国粤|粤语|国语|英语|日语|韩语|国英双轨|国粤英双轨|中英双字|` +
	`English|Chinese|Cantonese|Mandarin|Japanese|Korean|CHS|CHT|ENG|JPN|KOR|CHS-ENG|CHS-JPN|CHT-ENG|` +
	// 片源/容器
	`HDTV|HR-HDTV|HR\.HDTV|BluRay|Blu-Ray|BDRip|BDMV|WEB-?DL|WEBRip|HDRip|DVDRip|DVD|REMUX|UHD|RAW|TS|TC|CAM|HC|TVRip|` +
	// 视频编码
	`x264|x265|h264|h265|HEVC|AVC|XviD|DivX|VP9|AV1|10bit|8bit|HDR10\+?|HDR|SDR|DV|DolbyVision|` +
	// 音频编码
	`AAC|AAC2\.0|AC3|EAC3|DD5\.1|DD7\.1|DD\+|DDP|DTS|DTS-HD|DTS-X|DTSX|DTSHD|FLAC|MP3|TrueHD|Atmos|MA|` +
	// 分辨率 / 画质
	`4K|2K|8K|2160p|2160P|1080p|1080P|720p|720P|480p|480P|\d{3,4}[xX×]\d{3,4}|` +
	// 中文描述词
	`完整版|未删减版|院线版|导演剪辑版|加长版|年度佳作|` +
	// 常见发布组名（可能不完全，但覆盖示例中出现过的）
	`BOBO|SWTYBLZ|CMCT|HDS|FRDS|MNHD|WiKi|TLF|RARBG|YIFY|YTS|EVO|SPARKS` +
	`)$`)

// isNoiseToken 判断一个片段是否是完全的噪声词。
// 同时覆盖一些“数字.数字”组合被 "." 拆出后的碎片，如“5”、“1”、“7”（来自 5.1/7.1 音轨拆分）
func isNoiseToken(p string) bool {
	if p == "" {
		return true
	}
	// 微小碎片（单个或两个数字）但不是年份，一般是被"."拆分开的 5.1/7.1/2.0 碎片。
	// 为避免误伤「4 Months 3 Weeks And 2 Days」这种中间字随意出现的数字，不在这里过滤，
	// 仅依靠 noiseTokenWholeRe 覆盖。
	return noiseTokenWholeRe.MatchString(p)
}

// extractBracketChinese 从原始文件名中提取出方括号/中文括号里的中文片段
//
//	"01 [钢铁侠]Iron.Man.2008..."          -> ["钢铁侠"]
//	"19 [复仇者联盟3：无限战争]Avengers..." -> ["复仇者联盟3：无限战争"]
//	"[Pixar][玩具总动员]Toy.Story.1995..."  -> ["玩具总动员"]
//
// 仅返回包含中文的括号内容，避免把发布组、字幕组等英文括号信息当成标题。
func extractBracketChinese(fileName string) []string {
	bracketRe := regexp.MustCompile(`[\(（\[【]([^\)）\]】]*)[\)）\]】]`)
	matches := bracketRe.FindAllStringSubmatch(fileName, -1)
	var out []string
	for _, m := range matches {
		if len(m) < 2 {
			continue
		}
		inner := strings.TrimSpace(m[1])
		if inner == "" {
			continue
		}
		// 必须包含中文字符才视为中文标题候选
		if chineseRegexp.MatchString(inner) {
			out = append(out, inner)
		}
	}
	return out
}

// parseVideoFileName 从视频文件名中提取标题和年份
// 规则：
//   - 自动去除文件名开头的序号（如 "01 "、"1."、"169."），避免污染英文标题
//   - 优先把方括号 [中文] 内的中文片段作为中文标题候选
//   - 文件名中允许使用 . / 空格 / _ / 的分隔多个字段
//   - 第一个含中文的片段即为中文标题（若上一步括号内未找到中文）
//   - 中文标题之前的非中文、非年份片段拼接为英文标题
//   - 第一个 1900-2099 之间的 4 位数字识别为年份
//
// 例如：
//
//	"Inception.2010.盗梦空间.双语字幕.HR-HDTV.AC3.1024X576.X264-"      -> {English:"Inception", Chinese:"盗梦空间", Year:"2010"}
//	"Iron.Man.3.2013.钢铁侠3.国英音轨.双语字幕.HR-HDTV.AC3.x264-"        -> {English:"Iron Man 3", Chinese:"钢铁侠3", Year:"2013"}
//	"The.Dark.Knight.2008.1080p.BluRay"                                  -> {English:"The Dark Knight", Chinese:"", Year:"2008"}
//	"盗梦空间.2010.1080p.BluRay"                                          -> {English:"", Chinese:"盗梦空间", Year:"2010"}
//	"钢铁侠3 (2013).mkv"                                                  -> {English:"", Chinese:"钢铁侠3", Year:"2013"}
//	"群星-演唱会现场.mp4"                                                  -> {English:"", Chinese:"群星", Year:""}
//	"01 [钢铁侠]Iron.Man.2008.2160p.HDR.BluRay..."                        -> {English:"Iron Man", Chinese:"钢铁侠", Year:"2008"}
//	"19 [复仇者联盟3：无限战争]Avengers.Infinity.War.2018.2160p..."       -> {English:"Avengers Infinity War", Chinese:"复仇者联盟3：无限战争", Year:"2018"}
//	"169.谍影重重3"                                                       -> {English:"", Chinese:"谍影重重3", Year:""}
//	"1.漫威短片.47号物品"                                                  -> {English:"", Chinese:"漫威短片", Year:""}（额外候选含 "47号物品"）
func parseVideoFileName(fileName string) parsedVideoTitle {
	// 去掉扩展名（.mkv .mp4 .avi 等，扩展名长度 <= 5）
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		ext := strings.ToLower(fileName[idx:])
		if len(ext) <= 5 {
			fileName = fileName[:idx]
		}
	}

	// 1) 先把开头的序号 "01 "、"1."、"169." 去掉，避免它们污染英文标题
	//    仅在能明确判定为序号的场景才剥离，避免误伤 "30.Days..."、"300.斯巴达勇士" 这类数字作为片名一部分的情况
	fileName = stripLeadingSerial(fileName)

	// 2) 在剥离括号之前，先尝试从方括号 [中文] 中抽取中文标题候选
	//    很多发布组习惯是 "01 [中文标题]English.Title.Year..."，这里要保留 "中文标题"
	bracketChinese := extractBracketChinese(fileName)

	// 3) 提取括号中的年份（如 "钢铁侠3 (2013).mkv"），把年份替换到括号外
	bracketYearRe := regexp.MustCompile(`[\(（\[【]\s*((?:19|20)\d{2})\s*[\)）\]】]`)
	fileName = bracketYearRe.ReplaceAllString(fileName, " $1 ")
	// 4) 再把剩余括号块替换成空格（其中的中文标题已在第 2 步保存到 bracketChinese）
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
	var chineseParts []string // 收集所有中文片段，便于兜底
	foundYear := false
	foundChinese := false // 一旦出现中文片段，后面的非中文片段都不再加入英文标题

	// pureNoise 判断字符串经过噪声词清洗后是否为空（即整体都是噪声词）
	// 用于过滤掉"双语字幕"、"国英音轨" 这种纯噪声片段，避免被当成中文标题
	pureNoise := func(s string) bool {
		cleaned := noiseTokenRegexp.ReplaceAllString(s, "")
		cleaned = strings.TrimSpace(cleaned)
		return cleaned == ""
	}

	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}

		// 检测是否含中文
		if chineseRegexp.MatchString(p) {
			// 整体被识别为噪声词的中文片段（如 "双语字幕"、"国英音轨"）直接跳过
			if pureNoise(p) {
				continue
			}
			chineseParts = append(chineseParts, p)
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

		// 后续非中文片段加入英文标题的判定：
		//   - 必须在年份出现之前（年份后的 ASCII 全是发布信息噪声）
		//   - 必须在中文出现之前（中文后的 ASCII 也是发布信息噪声，避免 "斯巴达勇士 HR HDTV AC3 X264" 这种污染）
		//   - 不能是完全的噪声词（如 HR-HDTV、AC3、x264、分辨率）
		if foundYear || foundChinese {
			continue
		}
		if isNoiseToken(p) {
			continue
		}
		englishParts = append(englishParts, p)
	}

	result.EnglishTitle = strings.Join(englishParts, " ")

	// 选定中文标题：
	//   - 优先使用方括号内的中文（最可靠的发布组标记）
	//   - 否则使用解析出的中文片段中第一个
	//   - 同时把所有中文候选去重保留到 ExtraChineseTitles 里，留作兜底
	allChinese := append([]string{}, bracketChinese...)
	allChinese = append(allChinese, chineseParts...)
	allChinese = dedupStrings(allChinese)

	if len(allChinese) > 0 {
		result.ChineseTitle = allChinese[0]
		if len(allChinese) > 1 {
			result.ExtraChineseTitles = allChinese[1:]
		}
	}

	// 当解析出多个连续中文片段时，它们很可能是「主标题.副标题」结构（如 "300勇士.帝国崛起"
	// 实际是「300勇士：帝国崛起」），把合并后的候选也加入 ExtraChineseTitles 用于兜底搜索。
	if len(chineseParts) > 1 {
		merged := strings.Join(chineseParts, "：")
		// 避免与已有候选重复
		exists := merged == result.ChineseTitle
		for _, t := range result.ExtraChineseTitles {
			if t == merged {
				exists = true
				break
			}
		}
		if !exists {
			result.ExtraChineseTitles = append(result.ExtraChineseTitles, merged)
		}
	}

	// 对中文标题做尾部噪声清洗（去除「钢铁侠3 国英音轨」末尾的污染词，但保留主标题）
	if result.ChineseTitle != "" {
		result.ChineseTitle = stripTrailingNoise(result.ChineseTitle)
	}
	for i, t := range result.ExtraChineseTitles {
		result.ExtraChineseTitles[i] = stripTrailingNoise(t)
	}
	return result
}

// dedupStrings 去除字符串切片中的空白与重复项，保留首次出现顺序
func dedupStrings(in []string) []string {
	seen := make(map[string]bool)
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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

// subtitleSepRe 副标题分隔符：「：」「:」「-」前后空白
// 用于把「复仇者联盟3：无限战争」拆成「复仇者联盟3」和「无限战争」
var subtitleSepRe = regexp.MustCompile(`\s*[：:\-—]\s*`)

// buildTitleCandidates 根据原始标题构造一组候选搜索词（按优先级返回）
// 候选生成策略，从精确到模糊：
//  1. 原始标题（保留所有信息）
//  2. 归一化标题（去括号/噪声词/分隔符 -> 空格合并）
//  3. 中文数字 -> 阿拉伯数字（钢铁侠三 -> 钢铁侠3）
//  4. 阿拉伯数字 -> 中文数字（钢铁侠3 -> 钢铁侠三，少数 TMDB 别名用中文数字）
//  5. 去除尾部数字/罗马数字（钢铁侠3 -> 钢铁侠，盗梦空间2 -> 盗梦空间）
//  6. 拆分多个词，每个非短词作为独立候选
//  7. 副标题拆分（「复仇者联盟3：无限战争」 -> 「复仇者联盟3」、「无限战争」）
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

	// 副标题拆分：以「：」「:」「-」分隔后得到多个候选
	// 例：「复仇者联盟3：无限战争」-> 主标题 "复仇者联盟3"、副标题 "无限战争"
	//     「美国队长2：冬日战士」  -> 主标题 "美国队长2"、副标题 "冬日战士"
	//     「雷神2：黑暗世界」      -> 主标题 "雷神2"、副标题 "黑暗世界"
	subtitleParts := subtitleSepRe.Split(title, -1)
	if len(subtitleParts) > 1 {
		for _, p := range subtitleParts {
			add(p)
		}
	}

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

	// 数字 + 中文 / 中文 + 数字 的组合标题，把中文部分单独提取作为候选
	// 例：「300勇士」     -> 候选包含 "勇士"
	//     「3096天」      -> 候选包含 "天"（虽然过短）
	//     「36总局」      -> 候选包含 "总局"
	//     「47号物品」    -> 候选包含 "号物品" 较弱，跳过
	// 同时把开头数字单独作为候选，匹配「300」「3096」这类纯数字片名
	digitChineseRe := regexp.MustCompile(`^(\d+)([\p{Han}].*)$`)
	chineseDigitRe := regexp.MustCompile(`^([\p{Han}].*?)(\d+)$`)
	for _, s := range []string{title, norm} {
		if m := digitChineseRe.FindStringSubmatch(s); m != nil {
			// 数字部分（如 "300"）
			add(m[1])
			// 中文部分（如 "勇士"），仅当中文长度 >= 2 才有用
			if utf8.RuneCountInString(m[2]) >= 2 {
				add(m[2])
			}
		}
		if m := chineseDigitRe.FindStringSubmatch(s); m != nil {
			// 中文部分（去掉尾部数字）
			if utf8.RuneCountInString(m[1]) >= 2 {
				add(m[1])
			}
			// 数字部分单独作为候选
			add(m[2])
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
	log.Infof("[TMDB] 开始搜索 chinese=%q english=%q year=%q extras=%v",
		parsed.ChineseTitle, parsed.EnglishTitle, parsed.Year, parsed.ExtraChineseTitles)

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
	// 额外的中文候选（如解析出多个中文片段，或方括号内中文标题之外又出现了其他中文）
	for _, t := range parsed.ExtraChineseTitles {
		addGroup(t, "zh-CN")
	}
	// 英文标题兜底（en-US）
	addGroup(parsed.EnglishTitle, "en-US")

	// 最后一轮：不指定 language，对中文标题做多语言别名兜底
	// 这能匹配上 TMDB 中只在原始语言下注册了别名的作品
	noLangFallback := func(title string) {
		if title == "" {
			return
		}
		for _, q := range buildTitleCandidates(title) {
			attempts = append(attempts,
				searchAttempt{"movie", q, "", ""},
				searchAttempt{"tv", q, "", ""},
				searchAttempt{"multi", q, "", ""},
			)
		}
	}
	noLangFallback(parsed.ChineseTitle)
	for _, t := range parsed.ExtraChineseTitles {
		noLangFallback(t)
	}
	noLangFallback(parsed.EnglishTitle)

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
	log.Infof("[TMDB] 开始刮削 file=%q parsed={chinese=%q english=%q year=%q extras=%v} baseURL=%s",
		item.FileName, parsed.ChineseTitle, parsed.EnglishTitle, parsed.Year, parsed.ExtraChineseTitles, s.BaseURL)

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

	// 填充字段（仅填空字段，已有值不覆盖；ScrapedAt 由调用方统一设置）
	title := detail.Title
	if title == "" {
		title = first.Name
	}
	if item.ScrapedName == "" {
		item.ScrapedName = title
	}
	if item.Plot == "" {
		item.Plot = detail.Overview
	}
	if item.Rating == 0 {
		item.Rating = detail.VoteAverage
	}
	if item.ExternalID == "" {
		item.ExternalID = fmt.Sprintf("tmdb:%d", detail.ID)
	}
	if item.VideoType == "" {
		item.VideoType = mediaType
	}

	releaseDate := detail.ReleaseDate
	if releaseDate == "" {
		releaseDate = first.FirstAirDate
	}
	if item.ReleaseDate == "" {
		item.ReleaseDate = releaseDate
	}

	if item.Cover == "" && detail.PosterPath != "" {
		item.Cover = tmdbImageBase + detail.PosterPath
	}

	// 类型
	if item.Genre == "" {
		genres := make([]string, 0, len(detail.Genres))
		for _, g := range detail.Genres {
			genres = append(genres, g.Name)
		}
		item.Genre = strings.Join(genres, ",")
	}

	// 演员（取前10个）
	if item.Authors == "" {
		actors := make([]string, 0)
		for i, cast := range detail.Credits.Cast {
			if i >= 10 {
				break
			}
			actors = append(actors, cast.Name)
		}
		authorsJSON, _ := json.Marshal(actors)
		item.Authors = string(authorsJSON)
	}

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}