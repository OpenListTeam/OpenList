package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	stdpath "path"
	"strings"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

var apiRootHolder atomic.Value

// SetAPIRoot 记录本次运行使用的站点地址（由 HTTP 层注入，供异步任务复用）
func SetAPIRoot(u string) {
	if u == "" {
		return
	}
	apiRootHolder.Store(strings.TrimSuffix(u, "/"))
}

// GetAPIRoot 返回当前站点地址
func GetAPIRoot() string {
	if v, ok := apiRootHolder.Load().(string); ok && v != "" {
		return v
	}
	return strings.TrimSuffix(conf.Conf.SiteURL, "/")
}

// StrmOptions STRM 生成参数
type StrmOptions struct {
	APIRoot    string // 站点地址前缀，例如 https://example.com
	Template   string // 内容模板
	Overwrite  bool   // 是否覆盖已存在文件
	SameDir    bool   // 是否与媒体文件同目录（同名 .strm）
	OutputPath string // 输出目录（VFS 路径，仅 SameDir=false 时使用）
}

// StrmTarget 一个待生成的 .strm 目标
type StrmTarget struct {
	RelPath     string // 相对输出目录的路径
	RawPath     string // 对应媒体文件的 VFS 路径
	SameDirPath string // 与媒体文件同目录时的 .strm 路径
}

// strmSibling 计算与媒体文件同目录、同名的 .strm 路径
func strmSibling(rawPath string, isDir bool) string {
	if isDir {
		return stdpath.Join(rawPath, stdpath.Base(rawPath)+".strm")
	}
	ext := stdpath.Ext(rawPath)
	if ext == "" {
		return rawPath + ".strm"
	}
	return strings.TrimSuffix(rawPath, ext) + ".strm"
}

// DefaultStrmOptions 从设置中读取 STRM 参数
func DefaultStrmOptions(apiRoot string) StrmOptions {
	return StrmOptions{
		APIRoot:    strings.TrimSuffix(apiRoot, "/"),
		Template:   setting.GetStr(conf.MediaStrmTemplate, "{site_url}/d{path}?sign={sign}"),
		Overwrite:  setting.GetBool(conf.MediaStrmOverwrite),
		SameDir:    setting.GetBool(conf.MediaStrmSameDir),
		OutputPath: setting.GetStr(conf.MediaStrmPath, "/strm"),
	}
}

// RenderStrmContent 渲染 .strm 内容（单行 URL）
func RenderStrmContent(tpl, apiRoot, rawPath string) string {
	if tpl == "" {
		tpl = "{site_url}/d{path}?sign={sign}"
	}
	apiRoot = strings.TrimSuffix(apiRoot, "/")
	p := rawPath
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	replaced := strings.NewReplacer(
		"{site_url}", apiRoot,
		"{path}", utils.EncodePath(p),
		"{sign}", sign.Sign(rawPath),
		"{raw_path}", p,
		"{name}", stdpath.Base(p),
	).Replace(tpl)
	return strings.TrimSpace(replaced)
}

// sanitizeName 清洗文件/目录名中的非法字符
func sanitizeName(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "Unknown"
	}
	replacer := strings.NewReplacer(
		"/", "_", "\\", "_", ":", "_", "*", "_", "?", "_",
		"\"", "_", "<", "_", ">", "_", "|", "_",
		"\n", " ", "\r", " ",
	)
	s = replacer.Replace(s)
	if runes := []rune(s); len(runes) > 120 {
		s = string(runes[:120])
	}
	return strings.TrimSpace(s)
}

func itemTitle(item *model.MediaItem) string {
	if item.ScrapedName != "" {
		return item.ScrapedName
	}
	name := item.FileName
	if !item.IsFolder {
		name = strings.TrimSuffix(name, stdpath.Ext(name))
	}
	return name
}

func itemYear(item *model.MediaItem) string {
	if len(item.ReleaseDate) >= 4 {
		return item.ReleaseDate[:4]
	}
	return ""
}

func parseEpisodes(item *model.MediaItem) []model.EpisodeInfo {
	if item.Episodes == "" {
		return nil
	}
	var eps []model.EpisodeInfo
	if err := json.Unmarshal([]byte(item.Episodes), &eps); err != nil {
		return nil
	}
	return eps
}

// MediaItemFolder 条目所在目录
func MediaItemFolder(item *model.MediaItem) string {
	if item == nil {
		return "/"
	}
	folder := strings.TrimSuffix(item.FolderPath, "/")
	if folder == "" {
		folder = "/"
	}
	return folder
}

// StrmTargets 计算条目对应的 STRM 目标列表。
// 图片/书籍返回空列表（不生成 STRM）。
func StrmTargets(item *model.MediaItem) []StrmTarget {
	if item == nil {
		return nil
	}
	title := sanitizeName(itemTitle(item))
	year := itemYear(item)
	folder := MediaItemFolder(item)

	switch item.MediaType {
	case model.MediaTypeVideo:
		if item.VideoType == "tv" {
			series := title
			season := item.Season
			if season <= 0 {
				season = 1
			}
			var out []StrmTarget
			if item.IsFolder {
				for i, ep := range parseEpisodes(item) {
					num := ep.Index
					if num <= 0 {
						num = i + 1
					}
					epTitle := sanitizeName(ep.Title)
					epRaw := stdpath.Join(folder, item.FileName, ep.FileName)
					out = append(out, StrmTarget{
						RelPath: stdpath.Join(series, fmt.Sprintf("Season %02d", season),
							fmt.Sprintf("%s S%02dE%02d - %s.strm", series, season, num, epTitle)),
						RawPath:     epRaw,
						SameDirPath: strmSibling(epRaw, false),
					})
				}
				return out
			}
			num := item.Episode
			if num <= 0 {
				num = 1
			}
			raw := stdpath.Join(folder, item.FileName)
			return []StrmTarget{{
				RelPath: stdpath.Join(series, fmt.Sprintf("Season %02d", season),
					fmt.Sprintf("%s S%02dE%02d.strm", series, season, num)),
				RawPath:     raw,
				SameDirPath: strmSibling(raw, false),
			}}
		}
		name := title
		if year != "" {
			name = fmt.Sprintf("%s (%s)", title, year)
		}
		raw := stdpath.Join(folder, item.FileName)
		return []StrmTarget{{
			RelPath:     stdpath.Join(name, name+".strm"),
			RawPath:     raw,
			SameDirPath: strmSibling(raw, item.IsFolder),
		}}
	case model.MediaTypeMusic:
		artist := sanitizeName(item.AlbumArtist)
		if artist == "" {
			artist = "Unknown Artist"
		}
		album := sanitizeName(item.AlbumName)
		if album == "" {
			album = "Unknown Album"
		}
		track := item.TrackNumber
		if track <= 0 {
			track = 1
		}
		raw := stdpath.Join(folder, item.FileName)
		return []StrmTarget{{
			RelPath:     stdpath.Join(artist, album, fmt.Sprintf("%02d - %s.strm", track, title)),
			RawPath:     raw,
			SameDirPath: strmSibling(raw, false),
		}}
	default:
		return nil
	}
}

// GenerateStrmForItems 为一组条目生成 STRM 文件，返回写入数量
func GenerateStrmForItems(ctx context.Context, items []model.MediaItem, opts StrmOptions) (int, error) {
	if !opts.SameDir && opts.OutputPath == "" {
		return 0, fmt.Errorf("strm output path is empty")
	}
	// 归一输出根目录，后续用它校验每个目标文件都在根目录之内
	outRoot := ""
	if !opts.SameDir {
		outRoot = stdpath.Clean("/" + strings.TrimSpace(opts.OutputPath))
	}
	written := 0
	for i := range items {
		item := &items[i]
		if item.Hidden {
			continue
		}
		for _, target := range StrmTargets(item) {
			dst := target.SameDirPath
			if !opts.SameDir {
				dst = stdpath.Join(outRoot, target.RelPath)
				// RelPath 含刮削得到的艺术家/专辑名等外部数据，必须保证不会借 ../ 逃出输出目录
				if dst != outRoot && !strings.HasPrefix(dst, strings.TrimSuffix(outRoot, "/")+"/") {
					continue
				}
			}
			if dst == "" {
				continue
			}
			if !opts.Overwrite {
				if _, err := fs.Get(ctx, dst, &fs.GetArgs{NoLog: true}); err == nil {
					continue
				}
			}
			data := []byte(RenderStrmContent(opts.Template, opts.APIRoot, target.RawPath))
			file := &stream.FileStream{
				Ctx: ctx,
				Obj: &model.Object{
					Name:     stdpath.Base(dst),
					Size:     int64(len(data)),
					Modified: time.Now(),
				},
				Reader:   bytes.NewReader(data),
				Mimetype: "text/plain",
			}
			if err := fs.PutDirectly(ctx, stdpath.Dir(dst), file, true); err != nil {
				return written, fmt.Errorf("write %s: %w", dst, err)
			}
			written++
		}
	}
	return written, nil
}

// GenerateStrmForType 按媒体类型（可选扫描路径）批量生成 STRM
func GenerateStrmForType(ctx context.Context, mediaType model.MediaType, scanPathID uint, opts StrmOptions) (int, error) {
	var items []model.MediaItem
	var err error
	if scanPathID > 0 {
		items, err = db.ListMediaItemsByScanPath(scanPathID)
	} else {
		items, err = db.ListAllMediaItems(mediaType)
	}
	if err != nil {
		return 0, err
	}
	if mediaType != "" {
		filtered := make([]model.MediaItem, 0, len(items))
		for _, it := range items {
			if it.MediaType == mediaType {
				filtered = append(filtered, it)
			}
		}
		items = filtered
	}
	return GenerateStrmForItems(ctx, items, opts)
}

// TriggerStrmOnScan 扫描结束后按需异步生成 STRM
func TriggerStrmOnScan(mediaType model.MediaType, apiRoot string) {
	if !setting.GetBool(conf.MediaStrmOnScan) {
		return
	}
	opts := DefaultStrmOptions(apiRoot)
	if !opts.SameDir && opts.OutputPath == "" {
		return
	}
	go func() {
		n, err := GenerateStrmForType(context.Background(), mediaType, 0, opts)
		if err != nil {
			log.Errorf("[media] generate strm for %s failed: %+v", mediaType, err)
			return
		}
		log.Infof("[media] generated %d strm files for %s", n, mediaType)
	}()
}
