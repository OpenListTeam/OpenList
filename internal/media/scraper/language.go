package scraper

import (
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
)

// ScrapeLanguage 返回首选刮削语言（BCP-47，如 zh-CN / en-US），未配置时默认中文。
func ScrapeLanguage() string {
	lang := strings.TrimSpace(setting.GetStr(conf.MediaScrapeLanguage, "zh-CN"))
	if lang == "" {
		return "zh-CN"
	}
	return lang
}

// ScrapeLanguagePrimary 返回 ISO 639-1 主语言代码（如 zh / en），
// 供只接受两位语言码的接口使用（Google Books 的 langRestrict）。
func ScrapeLanguagePrimary() string {
	lang := ScrapeLanguage()
	if idx := strings.IndexAny(lang, "-_"); idx > 0 {
		lang = lang[:idx]
	}
	return strings.ToLower(lang)
}
