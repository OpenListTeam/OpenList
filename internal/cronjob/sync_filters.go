package cronjob

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/bmatcuk/doublestar/v4"
	"github.com/dlclark/regexp2"
)

// syncFilters 保存编译后的过滤规则。
// 编译一次后再递归遍历目录，可以避免每处理一个文件都重复解析正则或 glob。
type syncFilters struct {
	// args 是原始过滤参数，供大小、时间等简单比较使用。
	args SyncArgs
	// maxAge 与 minAge 是已经转换成 Go duration 的年龄阈值；0 表示不启用。
	maxAge time.Duration
	// minAge 见 maxAge。
	minAge time.Duration
	// globs 是 glob 过滤规则；这里使用 doublestar 支持 `**`。
	globs []string
	// goRegexps 是 Go 标准库正则。
	goRegexps []*regexp.Regexp
	// regexp2Regexps 是 .NET 风格正则。
	regexp2Regexps []*regexp2.Regexp
}

// newSyncFilters 编译并校验过滤参数。
func newSyncFilters(args SyncArgs) (*syncFilters, error) {
	filters := &syncFilters{args: args}

	if args.MaxAge != "" {
		duration, err := parseSyncAge(args.MaxAge)
		if err != nil {
			return nil, fmt.Errorf("parse max_age: %w", err)
		}
		filters.maxAge = duration
	}
	if args.MinAge != "" {
		duration, err := parseSyncAge(args.MinAge)
		if err != nil {
			return nil, fmt.Errorf("parse min_age: %w", err)
		}
		filters.minAge = duration
	}

	// glob 支持逐行输入，前端可以用多行文本框编辑。
	for _, pattern := range args.Exclude {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		// 用一个空字符串测试语法；doublestar.Match 只在语法错误时返回错误。
		if _, err := doublestar.Match(pattern, ""); err != nil {
			return nil, fmt.Errorf("invalid exclude glob %q: %w", pattern, err)
		}
		filters.globs = append(filters.globs, pattern)
	}

	// Go 标准库正则也支持逐行输入。
	for _, pattern := range args.ExcludeRegexp {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		compiled, err := regexp.Compile(pattern)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude_regexp %q: %w", pattern, err)
		}
		filters.goRegexps = append(filters.goRegexps, compiled)
	}

	// regexp2 支持 lookaround 等高级语法；项目已有该依赖，无需重复引入。
	for _, pattern := range args.ExcludeRegexp2 {
		pattern = strings.TrimSpace(pattern)
		if pattern == "" {
			continue
		}
		compiled, err := regexp2.Compile(pattern, regexp2.None)
		if err != nil {
			return nil, fmt.Errorf("invalid exclude_regexp2 %q: %w", pattern, err)
		}
		filters.regexp2Regexps = append(filters.regexp2Regexps, compiled)
	}

	return filters, nil
}

// excluded 判断一个对象是否被过滤规则排除。
// relPath 是相对于同步源根目录的路径，目录名与文件路径都会参与匹配。
func (f *syncFilters) excluded(relPath string) bool {
	relPath = strings.TrimPrefix(relPath, "/")
	for _, pattern := range f.globs {
		if matched, _ := doublestar.Match(pattern, relPath); matched {
			return true
		}
		// 常见用法是写 `*.tmp` 但也想排除子目录里的 `.tmp` 文件，
		// 因此同时匹配最后一部分路径，减少用户配置误差。
		if matched, _ := doublestar.Match(pattern, baseName(relPath)); matched {
			return true
		}
	}
	for _, reg := range f.goRegexps {
		if reg.MatchString(relPath) {
			return true
		}
	}
	for _, reg := range f.regexp2Regexps {
		if match, _ := reg.FindStringMatch(relPath); match != nil {
			return true
		}
	}
	return false
}

// ageExcluded 根据源对象的修改时间判断是否超过 age 过滤边界。
func (f *syncFilters) ageExcluded(srcObj model.Obj) bool {
	// 网盘返回的时间可能为零值；为零时不做年龄过滤，避免误跳过全部文件。
	modified := srcObj.ModTime()
	if modified.IsZero() {
		return false
	}
	age := time.Since(modified)
	if f.maxAge > 0 && age >= f.maxAge {
		return true
	}
	if f.minAge > 0 && age <= f.minAge {
		return true
	}
	return false
}

// sizeExcluded 根据源文件大小判断是否超过 size 过滤边界。
func (f *syncFilters) sizeExcluded(srcObj model.Obj) bool {
	size := srcObj.GetSize()
	if f.args.MaxSize > 0 && size >= f.args.MaxSize {
		return true
	}
	if f.args.MinSize > 0 && size <= f.args.MinSize {
		return true
	}
	return false
}

// baseName 返回路径的最后一部分；与标准库 path.Base 行为一致，但只服务同步过滤。
func baseName(path string) string {
	if idx := strings.LastIndex(path, "/"); idx >= 0 {
		return path[idx+1:]
	}
	return path
}
