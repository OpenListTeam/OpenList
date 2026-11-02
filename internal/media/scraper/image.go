package scraper

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"io"
	"strings"
	"time"

	"github.com/disintegration/imaging"
	"github.com/rwcarlsen/goexif/exif"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// ImageScraper 图片刮削器
// 从图片 EXIF 中提取元数据，并生成缩略图写入 Cover 字段。
type ImageScraper struct {
	// StoreThumbnail 为 true 时生成缩略图写入 Cover；
	// 为 false 时直接用文件路径作为 Cover，节省数据库空间。
	StoreThumbnail bool
	// ThumbnailMode 缩略图存储方式："base64"（默认，存入数据库）或 "local"（存到本地文件）
	ThumbnailMode string
	// ThumbnailPath 本地存储路径，ThumbnailMode 为 "local" 时有效，默认 "/.thumbnail"
	ThumbnailPath string
}

// NewImageScraper 创建图片刮削器
// storeThumbnail 为 true 时生成缩略图写入 Cover。
func NewImageScraper(storeThumbnail bool) *ImageScraper {
	return &ImageScraper{
		StoreThumbnail: storeThumbnail,
		ThumbnailMode:  "base64",
		ThumbnailPath:  "/.thumbnail",
	}
}

// NewImageScraperWithConfig 创建带完整配置的图片刮削器
func NewImageScraperWithConfig(storeThumbnail bool, thumbnailMode, thumbnailPath string) *ImageScraper {
	if thumbnailMode == "" {
		thumbnailMode = "base64"
	}
	if thumbnailPath == "" {
		thumbnailPath = "/.thumbnail"
	}
	return &ImageScraper{
		StoreThumbnail: storeThumbnail,
		ThumbnailMode:  thumbnailMode,
		ThumbnailPath:  thumbnailPath,
	}
}

// ScrapeImage 刮削图片信息
// reader 为图片文件内容流，必须传入有效流才能生成缩略图。
// 当 reader 不为 nil 时：
//   - 从 EXIF 读取拍摄时间、GPS 地点、相机型号+参数、评分
//   - 生成 300px 宽缩略图，根据 ThumbnailMode 存储为 Base64 或本地文件
//
// 注意：绝不将文件路径作为 cover，reader 为 nil 或缩略图生成失败时 cover 保持为空。
func (s *ImageScraper) ScrapeImage(item *model.MediaItem, reader io.Reader) error {
	// 去掉扩展名作为展示名（如果尚未设置）
	if item.ScrapedName == "" {
		item.ScrapedName = trimExt(item.FileName)
	}

	if reader == nil {
		// 无文件流时无法生成缩略图，cover 保持为空，等待下次重试
		now := time.Now()
		item.ScrapedAt = &now
		return nil
	}

	// 将流读入内存（EXIF 解析和图像解码都需要完整数据）
	data, err := readAllLimited(reader, MaxImageFileBytes)
	if err != nil || len(data) == 0 {
		// 读取失败时 cover 保持为空
		now := time.Now()
		item.ScrapedAt = &now
		return nil
	}

	// ── 1. 解析 EXIF ──────────────────────────────────────────────
	s.parseEXIF(item, bytes.NewReader(data))

	// ── 2. 生成缩略图并按配置存储 ────────────────────────────────
	if thumb := s.generateThumbBytes(data); len(thumb) > 0 {
		fullPath := strings.TrimSuffix(item.FolderPath, "/") + "/" + item.FileName
		item.Cover = SaveThumb(context.Background(), s.ThumbnailMode, s.ThumbnailPath, fullPath, "image/jpeg", thumb)
	}

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}

// parseEXIF 从图片数据中解析 EXIF 元数据并写入 item
func (s *ImageScraper) parseEXIF(item *model.MediaItem, r io.Reader) {
	x, err := exif.Decode(r)
	if err != nil {
		return
	}

	// ── 拍摄时间 → ReleaseDate ────────────────────────────────────
	if item.ReleaseDate == "" {
		if t, err := x.DateTime(); err == nil {
			item.ReleaseDate = t.Format("2006-01-02")
		}
	}

	// ── GPS 地点 → Authors（借用作者字段存储地点信息）────────────
	if item.Authors == "" {
		if lat, lon, err := x.LatLong(); err == nil {
			item.Authors = fmt.Sprintf(`["GPS: %.6f, %.6f"]`, lat, lon)
		}
	}

	// ── 相机型号 + 参数 → Genre ───────────────────────────────────
	if item.Genre == "" {
		var parts []string
		if make, err := x.Get(exif.Make); err == nil {
			if v, err := make.StringVal(); err == nil && v != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
		}
		if model_, err := x.Get(exif.Model); err == nil {
			if v, err := model_.StringVal(); err == nil && v != "" {
				parts = append(parts, strings.TrimSpace(v))
			}
		}
		if fnum, err := x.Get(exif.FNumber); err == nil {
			if num, den, err := fnum.Rat2(0); err == nil && den != 0 {
				parts = append(parts, fmt.Sprintf("f/%.1f", float64(num)/float64(den)))
			}
		}
		if exp, err := x.Get(exif.ExposureTime); err == nil {
			if num, den, err := exp.Rat2(0); err == nil && den != 0 {
				if num == 1 {
					parts = append(parts, fmt.Sprintf("1/%ds", den))
				} else {
					parts = append(parts, fmt.Sprintf("%d/%ds", num, den))
				}
			}
		}
		if iso, err := x.Get(exif.ISOSpeedRatings); err == nil {
			if v, err := iso.Int(0); err == nil {
				parts = append(parts, fmt.Sprintf("ISO%d", v))
			}
		}
		if focal, err := x.Get(exif.FocalLength); err == nil {
			if num, den, err := focal.Rat2(0); err == nil && den != 0 {
				parts = append(parts, fmt.Sprintf("%.0fmm", float64(num)/float64(den)))
			}
		}
		if len(parts) > 0 {
			item.Genre = strings.Join(parts, ",")
		}
	}

	// ── EXIF Rating（0-5 星）→ Rating（0-10 分）──────────────────
	// Windows 资源管理器、Lightroom、Darktable 等软件会写入此标签
	if item.Rating == 0 {
		if ratingTag, err := x.Get(exif.FieldName("Rating")); err == nil {
			if v, err := ratingTag.Int(0); err == nil && v > 0 {
				// 1-5 星映射到 2-10 分
				item.Rating = float32(v) * 2
			}
		}
	}
}

// generateThumbBytes 将图片数据缩放为 300px 宽缩略图，返回 JPEG 字节
func (s *ImageScraper) generateThumbBytes(data []byte) []byte {
	img, err := decodeBounded(data)
	if err != nil {
		return nil
	}

	// 缩放到宽度 300px，高度等比
	thumb := imaging.Resize(img, 300, 0, imaging.Lanczos)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// trimExt 去掉文件名的扩展名
func trimExt(fileName string) string {
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		return fileName[:idx]
	}
	return fileName
}
