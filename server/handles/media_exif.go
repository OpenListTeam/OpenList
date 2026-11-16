package handles

import (
	"bytes"
	"context"
	"fmt"
	"image"
	// 注册解码器：image.DecodeConfig 依赖它们读取图片宽高
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/rwcarlsen/goexif/exif"
)

// exifHeadBytes 读取文件头的上限。EXIF（APP1）通常在 64KB 以内，
// 但不少相机/手机把缩略图也塞在里面，1MB 足够覆盖绝大多数情况。
const exifHeadBytes = 1 << 20

// mediaExifResp 图片 EXIF 摘要。字段都省略空值，前端按有无渲染。
type mediaExifResp struct {
	Available bool `json:"available"`
	// 基本信息
	Format string `json:"format,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// 拍摄设备
	Camera   string `json:"camera,omitempty"`
	Lens     string `json:"lens,omitempty"`
	Software string `json:"software,omitempty"`
	// 拍摄参数
	Exposure string `json:"exposure,omitempty"`
	Aperture string `json:"aperture,omitempty"`
	ISO      string `json:"iso,omitempty"`
	Focal    string `json:"focal_length,omitempty"`
	TakenAt  string `json:"taken_at,omitempty"`
	// 位置
	Latitude  *float64 `json:"latitude,omitempty"`
	Longitude *float64 `json:"longitude,omitempty"`
}

func (r *mediaExifResp) hasGPS() bool {
	return r.Latitude != nil && r.Longitude != nil
}

type cachedExif struct {
	size    int64
	updated time.Time
	data    *mediaExifResp
}

const maxExifCacheEntries = 512

var (
	exifCacheMu sync.RWMutex
	exifCache   = make(map[uint]cachedExif)
)

// PublicGetMediaExif 返回图片文件的 EXIF 信息（拍摄设备 / 参数 / GPS）。
// 解析失败不算错误：很多图片（截图、PNG）本来就没有 EXIF，返回 available=false。
func PublicGetMediaExif(c *gin.Context) {
	id, err := strconv.ParseUint(c.Query("id"), 10, 64)
	if err != nil {
		common.ErrorStrResp(c, "无效的ID", 400)
		return
	}
	item, err := db.GetMediaItemByID(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 404)
		return
	}
	user := mediaUser(c)
	if item.Hidden || !CanUserAccessItem(user, item) {
		common.ErrorStrResp(c, "资源不存在", 404)
		return
	}
	if item.IsFolder || item.MediaType != model.MediaTypeImage {
		common.ErrorStrResp(c, "只有图片文件支持 EXIF 信息", 400)
		return
	}
	common.SuccessResp(c, loadImageExif(c.Request.Context(), user, item))
}

func loadImageExif(ctx context.Context, user *model.User, item *model.MediaItem) *mediaExifResp {
	// 同一张图反复打开时不必每次都读文件头
	exifCacheMu.RLock()
	hit, ok := exifCache[item.ID]
	exifCacheMu.RUnlock()
	if ok && hit.size == item.FileSize && hit.updated.Equal(item.UpdatedAt) {
		return hit.data
	}

	out := &mediaExifResp{}
	head, err := readMediaHead(ctx, user, item, exifHeadBytes)
	if err != nil {
		return out
	}
	if cfg, format, err := image.DecodeConfig(bytes.NewReader(head)); err == nil {
		out.Format = format
		out.Width, out.Height = cfg.Width, cfg.Height
	}
	if x, err := exif.Decode(bytes.NewReader(head)); err == nil {
		out.Camera = joinNonEmpty(" ", exifString(x, exif.Make), exifString(x, exif.Model))
		out.Lens = exifString(x, exif.LensModel)
		out.Software = exifString(x, exif.Software)
		out.Exposure = formatExposure(x)
		out.Aperture = formatAperture(x)
		out.ISO = exifString(x, exif.ISOSpeedRatings)
		out.Focal = formatFocal(x)
		out.TakenAt = formatTakenAt(exifString(x, exif.DateTimeOriginal))
		if lat, lng, err := x.LatLong(); err == nil {
			out.Latitude, out.Longitude = &lat, &lng
		}
		// 有些图片只有 EXIF 头（尺寸/方向），没有拍摄信息，
		// 这种情况按「没有 EXIF」处理，前端才显示提示而不是一个空面板
		out.Available = out.Camera != "" || out.Lens != "" ||
			out.Exposure != "" || out.Aperture != "" || out.ISO != "" ||
			out.TakenAt != "" || out.hasGPS()
	}

	exifCacheMu.Lock()
	if len(exifCache) >= maxExifCacheEntries {
		// 简单兜底：满了整体清空，EXIF 只是展示用途，重新解析代价很低
		exifCache = make(map[uint]cachedExif)
	}
	exifCache[item.ID] = cachedExif{size: item.FileSize, updated: item.UpdatedAt, data: out}
	exifCacheMu.Unlock()
	return out
}

// readMediaHead 读取媒体文件开头最多 limit 字节
func readMediaHead(ctx context.Context, user *model.User, item *model.MediaItem, limit int64) ([]byte, error) {
	fullPath := MediaItemFilePath(item)
	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return nil, err
	}
	obj, err := op.Get(ctx, storage, actualPath)
	if err != nil {
		return nil, err
	}
	if obj.IsDir() {
		return nil, fmt.Errorf("not a file: %s", fullPath)
	}
	length := obj.GetSize()
	if length <= 0 || length > limit {
		length = limit
	}
	link, _, err := op.Link(ctx, storage, actualPath, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	rangeReader, err := stream.GetRangeReaderFromLink(obj.GetSize(), link)
	if err != nil {
		return nil, err
	}
	rc, err := rangeReader.RangeRead(ctx, http_range.Range{Length: length})
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	// 小于 limit 的文件会提前 EOF，读到的部分照样可以解析
	data, err := io.ReadAll(io.LimitReader(rc, length))
	if err != nil && len(data) == 0 {
		return nil, err
	}
	return data, nil
}

func exifString(x *exif.Exif, name exif.FieldName) string {
	tag, err := x.Get(name)
	if err != nil || tag == nil {
		return ""
	}
	s, err := tag.StringVal()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(s)
}

func joinNonEmpty(sep string, parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if s := strings.TrimSpace(p); s != "" {
			out = append(out, s)
		}
	}
	return strings.Join(out, sep)
}

func exifTagRat(x *exif.Exif, name exif.FieldName) (num, den int64, ok bool) {
	tag, err := x.Get(name)
	if err != nil || tag == nil {
		return 0, 0, false
	}
	n, d, err := tag.Rat2(0)
	if err != nil || d == 0 {
		return 0, 0, false
	}
	return n, d, true
}

// formatExposure 1/250 s、2 s 这种常见写法
func formatExposure(x *exif.Exif) string {
	num, den, ok := exifTagRat(x, exif.ExposureTime)
	if !ok || num == 0 {
		return ""
	}
	if num >= den {
		return trimFloat(float64(num)/float64(den)) + " s"
	}
	return fmt.Sprintf("1/%d s", den/num)
}

func formatAperture(x *exif.Exif) string {
	num, den, ok := exifTagRat(x, exif.FNumber)
	if !ok || num == 0 {
		return ""
	}
	return "f/" + trimFloat(float64(num)/float64(den))
}

func formatFocal(x *exif.Exif) string {
	num, den, ok := exifTagRat(x, exif.FocalLength)
	if !ok || num == 0 {
		return ""
	}
	return trimFloat(float64(num)/float64(den)) + "mm"
}

func trimFloat(v float64) string {
	s := strconv.FormatFloat(v, 'f', 1, 64)
	return strings.TrimSuffix(s, ".0")
}

// formatTakenAt 把 "2019:05:01 12:00:00" 规整成 "2019-05-01 12:00:00"
func formatTakenAt(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) < 19 {
		return raw
	}
	date := strings.ReplaceAll(raw[:10], ":", "-")
	return date + raw[10:19]
}
