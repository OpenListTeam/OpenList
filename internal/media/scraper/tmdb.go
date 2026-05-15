package scraper

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"sync/atomic"
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
// 注意：这里只用于「子串匹配」清洗，不要求整段命中
var noiseTokenRegexp = regexp.MustCompile(`(?i)(双语字幕|双语|中字|中英字幕|中英双字|中英双语|国英双语|国英双轨|国粤双语|粤语中字|国语中字|英语中字|日语中字|韩语中字|国英|国粤|粤语|国语|英语|日语|韩语|内封字幕|外挂字幕|HDTV|HR-HDTV|BluRay|BDRip|WEB-?DL|HDRip|DVDRip|REMUX|x264|x265|h264|h265|HEVC|AVC|AAC|AC3|DTS|FLAC|10bit|8bit|HDR|SDR|4K|2160P|1080P|720P|480P|完整版|未删减版|加长版|导演剪辑版|蓝光版|高清版)`)

// chineseReleaseGroupRe 中文字幕组/压制组/影视站名的整段匹配正则
// 这些词出现在文件名中通常是发布信息，绝不应该被当成片名候选
// 例：「-人人影视制作」「.YYeTs.」「-飞鸟影视」
var chineseReleaseGroupRe = regexp.MustCompile(`^(?:` +
	`人人影视制作|人人影视|人人字幕|人人字幕组|YYeTs|YYETs|FRDS|` +
	`飞鸟影视|飞鸟影院|风行网|风行影视|远鉴字幕组|远鉴|深影字幕组|深影|` +
	`破烂熊|圣城家园|TLF|HDS|MNHD|WiKi|CMCT|RARBG|YIFY|YTS|` +
	`蓝色狂想|蓝色狂想字幕组|肥羊字幕组|衣柜字幕组|喵萌奶茶屋|` +
	`众乐字幕组|猪猪乐园|猪猪字幕组|流鸣字幕|动漫国字幕组|诸神字幕组|` +
	`字幕组|压制组|发布组|官方版本|高清影视|高清电影|蓝光原盘` +
	`)$`)

// chineseGarbageContainsRe 包含即视为发布组信息的中文片段（用于片段中嵌有这些字眼时整体丢弃）
// 例：「人人影视」、「天天美剧」即使附带其他字符，整段都不太可能是片名
var chineseGarbageContainsRe = regexp.MustCompile(`人人影视|人人字幕|YYeTs|字幕组|压制组|发布组`)

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

	// isChineseReleaseGroup 判断中文片段是否为字幕组/压制组名
	// 这类片段绝不应该被当成片名候选（如 "人人影视"、"YYeTs"、"飞鸟影视"）
	isChineseReleaseGroup := func(s string) bool {
		s = strings.TrimSpace(s)
		if s == "" {
			return false
		}
		if chineseReleaseGroupRe.MatchString(s) {
			return true
		}
		if chineseGarbageContainsRe.MatchString(s) {
			return true
		}
		return false
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
			// 字幕组/压制组中文名（如 "人人影视"、"飞鸟影院"）也直接跳过
			if isChineseReleaseGroup(p) {
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
// 同时剖除末尾的 (1) (2) V2 这类重复/版本后缀、字幕组名等
func stripTrailingNoise(s string) string {
	cleaned := noiseTokenRegexp.ReplaceAllString(s, " ")
	// 剖除「-人人影视」「.YYeTs」这种跟在中文后面的字幕组名
	cleaned = regexp.MustCompile(`[\s\-・]*` +
		`(?:人人影视制作|人人影视|人人字幕|YYeTs|YYETs|FRDS|飞鸟影视|飞鸟影院|风行网|深影|远鉴|字幕组|压制组)[s]?` +
		`(?:、|。|\.|,)?\s*$`).ReplaceAllString(cleaned, " ")
	// 剖除末尾的 (1)（重复文件标记）、V2、v3 这类版本后缀
	cleaned = regexp.MustCompile(`(?i)\s*[\(（]\s*\d+\s*[\)）]\s*$`).ReplaceAllString(cleaned, " ")
	cleaned = regexp.MustCompile(`(?i)\s*[Vv]\s*\d+\s*$`).ReplaceAllString(cleaned, " ")
	cleaned = regexp.MustCompile(`\s+`).ReplaceAllString(cleaned, " ")
	cleaned = strings.TrimSpace(cleaned)
	if cleaned == "" {
		return strings.TrimSpace(s)
	}
	return cleaned
}

const tmdbDefaultBaseURL = "https://api.themoviedb.org"
const tmdbImageBase = "https://image.tmdb.org/t/p/w500"

// TMDBStats TMDB 刮削统计（线程安全：原子计数）
// 仅记录一次进程级别的累积值；如需更精细可按 ScraperID 分桶
type TMDBStats struct {
	TotalScraped       int64 // 总刮削请求次数（每次 ScrapeVideo 调用 +1）
	HitCount           int64 // 成功刮到结果的次数（搜索阶段命中即停）
	NoMatchCount       int64 // 完全 0 命中的次数
	LowConfidenceCount int64 // 命中但低于阈值，使用了「最佳兜底」结果的次数
	TotalAttempts      int64 // 累计 TMDB 搜索请求次数（不含详情）
	TotalSearchMillis  int64 // 累计搜索耗时（毫秒）
}

// 全局统计实例
var globalTMDBStats = &TMDBStats{}

// GetTMDBStats 返回当前进程的 TMDB 刮削统计快照
func GetTMDBStats() TMDBStats {
	return *globalTMDBStats
}

// ResetTMDBStats 重置统计（仅用于测试或运维触发）
func ResetTMDBStats() {
	*globalTMDBStats = TMDBStats{}
}

// matchConfidenceThreshold 命中可信度阈值。
// scoreMatch >= 该阈值视为「足够可信」，立即返回；
// 介于 fallbackThreshold 和该阈值之间则记为「最佳兜底」；
// 低于 fallbackThreshold 进入「降级重试」阶段；都失败才报错。
const (
	matchConfidenceThreshold float32 = 0.60
	fallbackThreshold        float32 = 0.30
	// desperateThreshold 「降级重试」阶段的阈值：当主流程完全无任何 score>=0.30 的命中时，
	// 采用更宽的阈值（0.20）接受 score 在 [0.20, 0.30) 之间的结果作为最后的兜底。
	desperateThreshold float32 = 0.20
)

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
	log.Debugf("[TMDB] 请求: %s", maskAPIKey(searchURL))

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
	log.Debugf("[TMDB] 响应 status=%d hits=%d url=%s", resp.StatusCode, len(result.Results), maskAPIKey(searchURL))

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

// TitleCandidate 单个搜索候选
// 用于把文件名解析出来的多组标题/语言/年份/置信度统一成一条「线索」，
// 然后按 Confidence 排序后，逐条尝试 TMDB 搜索。
type TitleCandidate struct {
	Name       string  // 待搜索的标题字符串
	Lang       string  // "zh-CN" / "en-US" / ""（不指定）
	Year       string  // 候选年份（可空）
	Confidence float32 // 0-1，越高越优先；用于排序与「命中可信度」校验
	Source     string  // 来源标签：bracket-cn / main-cn / merged-cn / sub-cn / extra-cn / main-en / degenerate-cn / degenerate-en
}

// tvHintRe 检测文件名是否带电视剧特征（SxxExx、季、集、Episode）
// 命中则只走 /search/tv 端点，否则只走 /search/movie，节约一半请求量
var tvHintRe = regexp.MustCompile(`(?i)(s\d{1,2}e\d{1,3}|season\s*\d+|episode\s*\d+|第\s*\d+\s*季|第\s*\d+\s*集)`)

// pickEndpoint 根据原始文件名启发式判断走 movie 还是 tv
func pickEndpoint(rawFileName string) string {
	if tvHintRe.MatchString(rawFileName) {
		return "tv"
	}
	return "movie"
}

// englishStopwords 英文冠词/介词等无信息量小词，提取关键词时可丢弃
// 注意：仅丢弃整段完全等于这些词的部分，不做子串匹配，避免误伤 "and" 在 "Andromeda" 里
var englishStopwords = map[string]bool{
	"a": true, "an": true, "the": true,
	"of": true, "in": true, "on": true, "at": true, "by": true, "to": true, "for": true,
	"and": true, "or": true, "but": true,
	"is": true, "are": true, "was": true, "were": true,
	"with": true, "from": true, "into": true, "onto": true,
	"vs": true, "v": true,
}

// extractEnglishKeywords 从英文标题中提取最有信息量的关键词
//
// 规则：
//  1. 按空格分词，去掉 stopwords 与长度 < 3 的词
//  2. 取剩余词的前 3 个，用空格拼接（更长的标题往往是 TMDB 上没有的精确组合）
//  3. 如果剔除后没有剩余词，返回空字符串（让调用方跳过）
//
// 例：
//
//	"And Soon The Darkness"        -> "Soon Darkness"
//	"Alvin and the Chipmunks"      -> "Alvin Chipmunks"
//	"30 Days Of Night Dark Days"   -> "30 Days Night"
//	"Iron Man"                     -> "Iron Man"（原样保留，关键词都够长）
//	"The Dark Knight"              -> "Dark Knight"
func extractEnglishKeywords(s string) string {
	if s == "" {
		return ""
	}
	words := strings.Fields(s)
	keepers := make([]string, 0, len(words))
	for _, w := range words {
		lw := strings.ToLower(strings.Trim(w, ".,;:!?'\""))
		if englishStopwords[lw] {
			continue
		}
		if utf8.RuneCountInString(lw) < 3 && !isAllDigits(lw) {
			// 长度 < 3 且不是纯数字的词丢弃（数字往往是片名一部分如"300"）
			continue
		}
		keepers = append(keepers, w)
	}
	if len(keepers) == 0 {
		return ""
	}
	if len(keepers) > 3 {
		keepers = keepers[:3]
	}
	return strings.Join(keepers, " ")
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// titleSimilarity 简易标题相似度（0-1）
// 规则：
//   - 完全相等 -> 1.0
//   - 一个字符串包含另一个 -> 0.85
//   - 归一化（去空格、大小写）后相等 -> 0.95
//   - 归一化后一个包含另一个 -> 0.7
//   - 否则用「公共字符占比」粗算（避免引入完整编辑距离的开销）
func titleSimilarity(a, b string) float32 {
	if a == "" || b == "" {
		return 0
	}
	if a == b {
		return 1.0
	}
	la := strings.ToLower(strings.TrimSpace(a))
	lb := strings.ToLower(strings.TrimSpace(b))
	if la == lb {
		return 0.95
	}
	if strings.Contains(la, lb) || strings.Contains(lb, la) {
		return 0.85
	}
	// 进一步归一化：去掉所有空白与标点，再比一次包含关系
	normA := normalizeForCompare(la)
	normB := normalizeForCompare(lb)
	if normA == normB && normA != "" {
		return 0.92
	}
	if normA != "" && normB != "" && (strings.Contains(normA, normB) || strings.Contains(normB, normA)) {
		return 0.7
	}
	// 兜底：公共字符比例（仅取较短字符串里的字符在较长字符串中出现的比例）
	short, long := normA, normB
	if len(long) < len(short) {
		short, long = long, short
	}
	if short == "" {
		return 0
	}
	hit := 0
	for _, r := range short {
		if strings.ContainsRune(long, r) {
			hit++
		}
	}
	ratio := float32(hit) / float32(utf8.RuneCountInString(short))
	// 公共字符比例最高只贡献 0.5，避免冷门误判
	if ratio > 0.5 {
		ratio = 0.5
	}
	return ratio
}

// normalizeForCompare 归一化字符串以做相似度比较：
// 去掉所有空白、标点（含中文标点），转小写。
func normalizeForCompare(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == ' ' || r == '\t' || r == '\n':
		case r == '.' || r == ',' || r == '-' || r == '_' || r == ':' || r == ';' || r == '!' || r == '?':
		case r == '：' || r == '，' || r == '。' || r == '、' || r == '！' || r == '？':
		case r == '(' || r == ')' || r == '[' || r == ']' || r == '（' || r == '）' || r == '【' || r == '】':
		default:
			b.WriteRune(r)
		}
	}
	return strings.ToLower(b.String())
}

// tmdbHit 从 tmdbSearchResult.Results 提取出来的单个命中项（仅用于内部传递）
type tmdbHit = struct {
	ID           int     `json:"id"`
	Title        string  `json:"title"`
	Name         string  `json:"name"`
	Overview     string  `json:"overview"`
	PosterPath   string  `json:"poster_path"`
	ReleaseDate  string  `json:"release_date"`
	FirstAirDate string  `json:"first_air_date"`
	VoteAverage  float32 `json:"vote_average"`
	MediaType    string  `json:"media_type"`
	GenreIDs     []int   `json:"genre_ids"`
}

// scoreMatch 给一个 TMDB 搜索结果打分，作为「这个 hit 是否可信」的依据。
// score = 0.5*titleSim + 0.3*yearScore + 0.2*voteScore  (范围 0-1)
func scoreMatch(query string, year string, hit tmdbHit) float32 {
	hitTitle := hit.Title
	if hitTitle == "" {
		hitTitle = hit.Name
	}
	hitYear := ""
	if len(hit.ReleaseDate) >= 4 {
		hitYear = hit.ReleaseDate[:4]
	} else if len(hit.FirstAirDate) >= 4 {
		hitYear = hit.FirstAirDate[:4]
	}

	titleSim := titleSimilarity(query, hitTitle)

	var yearScore float32
	if year == "" {
		// 无年份信号时给一个中性分（不奖励、不惩罚）
		yearScore = 0.4
	} else if hitYear == "" {
		// 文件名有年份但 TMDB 没填，给较低分
		yearScore = 0.2
	} else if hitYear == year {
		yearScore = 1.0
	} else {
		// 容忍 ±1 年（部分电影上映日期记录差一年）
		diff := absInt(parseIntSafe(hitYear) - parseIntSafe(year))
		switch {
		case diff == 1:
			yearScore = 0.5
		case diff == 2:
			yearScore = 0.2
		default:
			yearScore = 0.0
		}
	}

	// vote_average 0-10，用作冷门片名的最弱信号
	voteScore := hit.VoteAverage / 10.0
	if voteScore > 1 {
		voteScore = 1
	}

	return 0.5*titleSim + 0.3*yearScore + 0.2*voteScore
}

func absInt(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

func parseIntSafe(s string) int {
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	return n
}

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
	// 「异形1」-> 「异形」、「三十极夜2」-> 「三十极夜」
	// 这条对 title / norm / arabic 三者都做，提升 TMDB 中文别名不带后缀作品的命中率
	trailingNumRe := regexp.MustCompile(`[\s]*[0-9０-９]+\s*$`)
	add(trailingNumRe.ReplaceAllString(title, ""))
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

// extractCandidates 把 parsedVideoTitle 转换成有序的 TitleCandidate 列表
//
// 顺序原则：中文优先 -> 英文兜底；高置信度优先 -> 低置信度退化候选
// 来源标签 (Source)：
//   - bracket-cn  : 来自方括号内的中文，置信度最高（发布组明确标注）
//   - main-cn     : 文件名中第一个中文片段
//   - main-en     : 主英文标题
//   - merged-cn   : 多个中文片段合并（300勇士：帝国崛起）
//   - sub-cn      : 中文标题的副标题拆分（无限战争）
//   - extra-cn    : 其它中文片段
//   - degenerate-cn / degenerate-en : 去尾数/去括号等退化形式
func extractCandidates(parsed parsedVideoTitle, bracketChinese []string) []TitleCandidate {
	var list []TitleCandidate
	seen := make(map[string]bool)
	add := func(name, lang, source string, conf float32) {
		name = strings.TrimSpace(name)
		if name == "" || utf8.RuneCountInString(name) < 1 {
			return
		}
		key := lang + "|" + name
		if seen[key] {
			return
		}
		seen[key] = true
		list = append(list, TitleCandidate{
			Name:       name,
			Lang:       lang,
			Year:       parsed.Year,
			Confidence: conf,
			Source:     source,
		})
	}

	// 1) 方括号中文：发布组明确标注的片名，最可信
	for _, b := range bracketChinese {
		add(b, "zh-CN", "bracket-cn", 0.95)
	}

	// 2) 主中文标题
	if parsed.ChineseTitle != "" {
		add(parsed.ChineseTitle, "zh-CN", "main-cn", 0.90)
		// 副标题拆分：「复仇者联盟3：无限战争」-> 「复仇者联盟3」+「无限战争」
		parts := subtitleSepRe.Split(parsed.ChineseTitle, -1)
		if len(parts) > 1 {
			for _, p := range parts {
				add(p, "zh-CN", "sub-cn", 0.55)
			}
		}
	}

	// 3) 主英文标题
	if parsed.EnglishTitle != "" {
		add(parsed.EnglishTitle, "en-US", "main-en", 0.80)
	}

	// 4) 额外中文候选（解析出多个中文片段时；包括 merged 合并版本）
	for _, t := range parsed.ExtraChineseTitles {
		source := "extra-cn"
		conf := float32(0.50)
		if strings.Contains(t, "：") {
			source = "merged-cn"
			conf = 0.65
		}
		add(t, "zh-CN", source, conf)
	}

	// 5) 退化候选：对中文/英文都做「去尾部数字」「归一化」
	trailingNumRe := regexp.MustCompile(`[\s]*[0-9０-９]+\s*$`)
	addDegenerate := func(src, lang, sourceTag string) {
		if src == "" {
			return
		}
		norm := normalizeTitle(src)
		if norm != src {
			add(norm, lang, sourceTag, 0.35)
		}
		if stripped := trailingNumRe.ReplaceAllString(src, ""); stripped != src {
			add(stripped, lang, sourceTag, 0.30)
		}
		if stripped := trailingNumRe.ReplaceAllString(norm, ""); stripped != norm {
			add(stripped, lang, sourceTag, 0.30)
		}
		// 中文数字 ⇄ 阿拉伯数字
		if arabic := cnNumToArabic(src); arabic != src {
			add(arabic, lang, sourceTag, 0.35)
		}
	}
	addDegenerate(parsed.ChineseTitle, "zh-CN", "degenerate-cn")
	addDegenerate(parsed.EnglishTitle, "en-US", "degenerate-en")

	return list
}

// searchWithFallback 带命中校验的多候选搜索
//
// 流程：
//  1. 把候选按 Confidence 排序（中文高 > 中文低 > 英文 > 退化）
//  2. 对每个候选，最多发 2 次请求：带年份精搜 + 不带年份模糊搜
//  3. 命中即用 scoreMatch 评分：
//     - score >= matchConfidenceThreshold (0.60) → 立即返回
//     - score >= fallbackThreshold (0.30) → 暂存为「最佳兜底」继续尝试
//     - 其它 → 丢弃
//  4. 全部尝试完仍无强命中 → 返回最佳兜底；都没有则报错
//
// 端点选择：根据原始文件名启发式（含 SxxExx/季/集 -> tv，否则 movie），不再 movie/tv 互打
func (s *TMDBScraper) searchWithFallback(parsed parsedVideoTitle, rawFileName string, bracketChinese []string) (*tmdbSearchResult, error) {
	candidates := extractCandidates(parsed, bracketChinese)
	// 按 Confidence 倒序，置信度高的先尝试
	sort.SliceStable(candidates, func(i, j int) bool {
		return candidates[i].Confidence > candidates[j].Confidence
	})

	endpoint := pickEndpoint(rawFileName)

	// Debug 输出所有待尝试的候选
	log.Debugf("[TMDB] 候选生成 endpoint=%s year=%q candidates(%d):", endpoint, parsed.Year, len(candidates))
	for i, c := range candidates {
		log.Debugf("[TMDB]   #%d source=%-13s lang=%-5s conf=%.2f name=%q", i+1, c.Source, c.Lang, c.Confidence, c.Name)
	}

	if len(candidates) == 0 {
		return nil, fmt.Errorf("无法从文件名中提取有效标题")
	}

	// 去重
	type attemptKey struct{ ep, q, y, l string }
	done := make(map[attemptKey]bool)
	attempts := int64(0)

	var bestFallback *tmdbSearchResult // 暂存的「分数过得去但不够强」的结果
	var bestScore float32
	var bestQuery string
	var lastErr error

	tryOne := func(ep, q, year, lang, source string, candConf float32) (*tmdbSearchResult, float32, bool) {
		k := attemptKey{ep, q, year, lang}
		if done[k] {
			return nil, 0, false
		}
		done[k] = true
		attempts++

		result, err := s.doTMDBSearch(ep, q, year, lang)
		if err != nil {
			lastErr = err
			log.Debugf("[TMDB] try [%s] q=%q year=%q lang=%q -> 请求失败: %v", source, q, year, lang, err)
			return nil, 0, false
		}
		if len(result.Results) == 0 {
			log.Debugf("[TMDB] try [%s] q=%q year=%q lang=%q -> 0 hits", source, q, year, lang)
			return nil, 0, false
		}
		first := result.Results[0]
		score := scoreMatch(q, year, first)
		hitTitle := first.Title
		if hitTitle == "" {
			hitTitle = first.Name
		}
		hitYear := ""
		if len(first.ReleaseDate) >= 4 {
			hitYear = first.ReleaseDate[:4]
		} else if len(first.FirstAirDate) >= 4 {
			hitYear = first.FirstAirDate[:4]
		}
		log.Debugf("[TMDB] try [%s] q=%q year=%q lang=%q -> hit{id=%d, title=%q, year=%s, vote=%.1f} score=%.2f",
			source, q, year, lang, first.ID, hitTitle, hitYear, first.VoteAverage, score)
		return result, score, true
	}

	startTs := time.Now()

	for _, c := range candidates {
		// 5.1 带年份精搜（如果有年份）
		if c.Year != "" {
			if res, score, ok := tryOne(endpoint, c.Name, c.Year, c.Lang, c.Source+"+y", c.Confidence); ok {
				if score >= matchConfidenceThreshold {
					recordHit(attempts, time.Since(startTs))
					log.Debugf("[TMDB] ✅ 强命中 (%.2f >= %.2f) source=%s q=%q", score, matchConfidenceThreshold, c.Source, c.Name)
					return res, nil
				}
				if score >= fallbackThreshold && score > bestScore {
					bestFallback = res
					bestScore = score
					bestQuery = c.Name
				}
			}
		}
		// 5.2 不带年份模糊搜（兜底）
		if res, score, ok := tryOne(endpoint, c.Name, "", c.Lang, c.Source, c.Confidence); ok {
			if score >= matchConfidenceThreshold {
				recordHit(attempts, time.Since(startTs))
				log.Debugf("[TMDB] ✅ 强命中 (%.2f >= %.2f) source=%s q=%q", score, matchConfidenceThreshold, c.Source, c.Name)
				return res, nil
			}
			if score >= fallbackThreshold && score > bestScore {
				bestFallback = res
				bestScore = score
				bestQuery = c.Name
			}
		}
	}

	// 没有强命中：使用 best fallback（可能仍然是错的，但至少有数据）
	if bestFallback != nil {
		recordLowConfidence(attempts, time.Since(startTs))
		log.Debugf("[TMDB] ⚠️ 使用低置信度兜底命中 score=%.2f q=%q (low confidence < %.2f)",
			bestScore, bestQuery, matchConfidenceThreshold)
		return bestFallback, nil
	}

	// 主流程完全无命中 → 进入「降级重试」阶段
	// 这一阶段主动放宽限制，尝试更“街头魔法”的查询方式：
	//   L2: 主候选换 multi 端点 + 不指定语言 → 匹配 TMDB 全语言别名
	//   L3: 英文标题去冠词/取关键词重新搜
	//   L4: 阈值降到 desperateThreshold (0.20)，换 multi 端点接纳「还有点像」的结果
	log.Debugf("[TMDB] 主流程无命中，进入降级重试阶段 attempts=%d", attempts)

	// L2: 主候选换 multi 端点 + 不指定语言
	for i, c := range candidates {
		if i >= 3 { // 只对前 3 个高置信度候选走 multi，避免请求爆炸
			break
		}
		if res, score, ok := tryOne("multi", c.Name, "", "", c.Source+"@multi", c.Confidence); ok {
			if score >= matchConfidenceThreshold {
				recordHit(attempts, time.Since(startTs))
				log.Debugf("[TMDB] ✅ L2 multi 端点强命中 (%.2f >= %.2f) q=%q", score, matchConfidenceThreshold, c.Name)
				return res, nil
			}
			if score >= fallbackThreshold && score > bestScore {
				bestFallback = res
				bestScore = score
				bestQuery = c.Name
			}
		}
	}
	if bestFallback != nil {
		recordLowConfidence(attempts, time.Since(startTs))
		log.Debugf("[TMDB] ⚠️ L2 低置信度兜底命中 score=%.2f q=%q", bestScore, bestQuery)
		return bestFallback, nil
	}

	// L3: 英文标题提取关键词（去冠词及过短词）重新搜索
	if kw := extractEnglishKeywords(parsed.EnglishTitle); kw != "" && kw != parsed.EnglishTitle {
		log.Debugf("[TMDB] L3 英文关键词提取: %q -> %q", parsed.EnglishTitle, kw)
		for _, ep := range []string{endpoint, "multi"} {
			if res, score, ok := tryOne(ep, kw, "", "", "keywords-en", 0.4); ok {
				if score >= matchConfidenceThreshold {
					recordHit(attempts, time.Since(startTs))
					log.Debugf("[TMDB] ✅ L3 关键词强命中 (%.2f >= %.2f) q=%q", score, matchConfidenceThreshold, kw)
					return res, nil
				}
				if score >= fallbackThreshold && score > bestScore {
					bestFallback = res
					bestScore = score
					bestQuery = kw
				}
			}
		}
		if bestFallback != nil {
			recordLowConfidence(attempts, time.Since(startTs))
			log.Debugf("[TMDB] ⚠️ L3 低置信度兜底命中 score=%.2f q=%q", bestScore, bestQuery)
			return bestFallback, nil
		}
	}

	// L4: 「绝望式」阈值——陈障足以阁，重跳主候选但阈值降到 0.20
	// 说明：部分冷门片 vote_average=0、不同语言别名不及时补录，会被 0.30 阈值误杀，
	// 这里使用更宽的 0.20 阈值接纳（阅读者可手动修正错误结果，总比空手好）。
	log.Debugf("[TMDB] L4 启用绝望式阈值 %.2f 重试主候选", desperateThreshold)
	for i, c := range candidates {
		if i >= 3 {
			break
		}
		for _, ep := range []string{endpoint, "multi"} {
			if res, score, ok := tryOne(ep, c.Name, "", "", c.Source+"@desperate", c.Confidence); ok {
				if score >= desperateThreshold && score > bestScore {
					bestFallback = res
					bestScore = score
					bestQuery = c.Name
				}
			}
		}
	}
	if bestFallback != nil {
		recordLowConfidence(attempts, time.Since(startTs))
		log.Debugf("[TMDB] ⚠️ L4 绝望式兜底命中 score=%.2f q=%q (阈值仅 %.2f，结果可能不准)",
			bestScore, bestQuery, desperateThreshold)
		return bestFallback, nil
	}

	recordNoMatch(attempts, time.Since(startTs))
	titleInfo := parsed.ChineseTitle
	if titleInfo == "" {
		titleInfo = parsed.EnglishTitle
	}
	if lastErr != nil {
		return nil, fmt.Errorf("TMDB未找到匹配结果: %s (last err: %v)", titleInfo, lastErr)
	}
	return nil, fmt.Errorf("TMDB未找到匹配结果: %s", titleInfo)
}

// 统计辅助函数（使用 atomic 保证并发安全）
func recordHit(attempts int64, elapsed time.Duration) {
	atomic.AddInt64(&globalTMDBStats.HitCount, 1)
	atomic.AddInt64(&globalTMDBStats.TotalAttempts, attempts)
	atomic.AddInt64(&globalTMDBStats.TotalSearchMillis, elapsed.Milliseconds())
}

func recordLowConfidence(attempts int64, elapsed time.Duration) {
	atomic.AddInt64(&globalTMDBStats.LowConfidenceCount, 1)
	atomic.AddInt64(&globalTMDBStats.TotalAttempts, attempts)
	atomic.AddInt64(&globalTMDBStats.TotalSearchMillis, elapsed.Milliseconds())
}

func recordNoMatch(attempts int64, elapsed time.Duration) {
	atomic.AddInt64(&globalTMDBStats.NoMatchCount, 1)
	atomic.AddInt64(&globalTMDBStats.TotalAttempts, attempts)
	atomic.AddInt64(&globalTMDBStats.TotalSearchMillis, elapsed.Milliseconds())
}

// ScrapeVideo 刮削视频信息
func (s *TMDBScraper) ScrapeVideo(item *model.MediaItem) error {
	if s.APIKey == "" {
		return fmt.Errorf("TMDB API Key 未配置")
	}

	atomic.AddInt64(&globalTMDBStats.TotalScraped, 1)

	// 始终从文件名中解析出标题和年份（ScrapedName 是刮削结果字段，不作为搜索输入）
	parsed := parseVideoFileName(item.FileName)
	// 在剥离括号前再次抽一次方括号中文，作为最高置信度候选源
	bracketChinese := extractBracketChinese(item.FileName)
	log.Debugf("[TMDB] 开始刮削 file=%q parsed={chinese=%q english=%q year=%q extras=%v bracket=%v} baseURL=%s",
		item.FileName, parsed.ChineseTitle, parsed.EnglishTitle, parsed.Year, parsed.ExtraChineseTitles, bracketChinese, s.BaseURL)

	// 搜索策略：中文标题优先，英文标题兜底，都搜不到才失败
	searchResult, err := s.searchWithFallback(parsed, item.FileName, bracketChinese)
	if err != nil {
		log.Debugf("[TMDB] ❌ 刮削失败 file=%q err=%v", item.FileName, err)
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
	log.Debugf("[TMDB] 详情请求: %s", maskAPIKey(detailURL))

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
	hitTitle := title
	hitYear := ""
	if len(detail.ReleaseDate) >= 4 {
		hitYear = detail.ReleaseDate[:4]
	}
	log.Debugf("[TMDB] ✅ 刮削完成 file=%q -> tmdb_id=%d, title=%q, year=%s, mediaType=%s",
		item.FileName, detail.ID, hitTitle, hitYear, mediaType)
	return nil
}