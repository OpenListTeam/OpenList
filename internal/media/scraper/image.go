package scraper

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
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
	data, err := io.ReadAll(reader)
	if err != nil || len(data) == 0 {
		// 读取失败时 cover 保持为空
		now := time.Now()
		item.ScrapedAt = &now
		return nil
	}

	// ── 1. 解析 EXIF ──────────────────────────────────────────────
	s.parseEXIF(item, bytes.NewReader(data))

	// ── 2. 生成缩略图并存储 ──────────────────────────────────────
	if s.ThumbnailMode == "local" {
		// 本地存储模式：将缩略图保存到指定目录，Cover 存储本地路径
		if localPath := s.saveThumbnailLocal(item.FolderPath, data); localPath != "" {
			item.Cover = localPath
		}
		// 本地存储失败时 cover 保持为空，不降级为文件路径
	} else {
		// Base64 模式（默认）：生成缩略图 Base64 写入 Cover
		if thumb := s.generateThumbnail(data); thumb != "" {
			item.Cover = thumb
		}
		// 缩略图生成失败时 cover 保持为空，不降级为文件路径
	}

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}

// saveThumbnailLocal 将缩略图保存到本地文件系统，返回可访问的路径
// 文件保存在 ThumbnailPath 目录下，文件名为原文件路径的 hash + .jpg
func (s *ImageScraper) saveThumbnailLocal(filePath string, data []byte) string {
	// 生成缩略图数据
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}
	thumb := imaging.Resize(img, 300, 0, imaging.Lanczos)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return ""
	}

	// 构建本地存储路径：ThumbnailPath/原文件路径.jpg
	// 将文件路径中的 / 替换为 _ 避免目录嵌套问题
	safeFileName := strings.ReplaceAll(strings.TrimPrefix(filePath, "/"), "/", "_") + ".jpg"
	localDir := s.ThumbnailPath
	if !filepath.IsAbs(localDir) {
		// 相对路径转为绝对路径（相对于工作目录）
		if wd, err := os.Getwd(); err == nil {
			localDir = filepath.Join(wd, localDir)
		}
	}

	// 确保目录存在
	if err := os.MkdirAll(localDir, 0755); err != nil {
		return ""
	}

	localFilePath := filepath.Join(localDir, safeFileName)
	if err := os.WriteFile(localFilePath, buf.Bytes(), 0644); err != nil {
		return ""
	}

	// 返回内部访问路径（通过 /d/ 前缀访问）
	// 缩略图路径格式：ThumbnailPath/safeFileName
	thumbVFSPath := strings.TrimSuffix(s.ThumbnailPath, "/") + "/" + safeFileName
	return buildInternalDownloadPath(thumbVFSPath)
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

// generateThumbnail 将图片数据缩放为 300px 宽缩略图，返回 data URI Base64 字符串
func (s *ImageScraper) generateThumbnail(data []byte) string {
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return ""
	}

	// 缩放到宽度 300px，高度等比
	thumb := imaging.Resize(img, 300, 0, imaging.Lanczos)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return ""
	}

	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// buildInternalDownloadPath 将 VFS 文件路径转为内部代理下载路径
// 格式：/d/path/to/file（前端通过此路径访问文件内容）
func buildInternalDownloadPath(filePath string) string {
	if !strings.HasPrefix(filePath, "/") {
		filePath = "/" + filePath
	}
	return "/d" + filePath
}

// trimExt 去掉文件名的扩展名
func trimExt(fileName string) string {
	if idx := strings.LastIndex(fileName, "."); idx > 0 {
		return fileName[:idx]
	}
	return fileName
}
