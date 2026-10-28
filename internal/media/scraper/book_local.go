package scraper

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/media/imgmime"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/disintegration/imaging"
	"github.com/go-pdfkit/reader"
	"github.com/go-pdfkit/render"
)

// BookLocalScraper 书籍本地刮削器
// 尝试从书籍文件中提取封面图片并以 base64 存入 Cover 字段，或保存到本地文件。
// - EPUB：解压 zip，提取 OPF 声明的封面或 cover.jpg/cover.png
// - PDF：尝试提取内嵌 JPEG 图片流作为封面
// - 其他/失败：降级为文件路径
type BookLocalScraper struct {
	// ThumbnailMode 缩略图存储方式："base64"（默认，存入数据库）或 "local"（存到本地文件）
	ThumbnailMode string
	// ThumbnailPath 本地存储路径，ThumbnailMode 为 "local" 时有效，默认 "/.thumbnail"
	ThumbnailPath string
	// PreserveCover 为 true 时不提取或写入封面。
	PreserveCover bool
}

// NewBookLocalScraper 创建书籍本地刮削器
func NewBookLocalScraper() *BookLocalScraper {
	return &BookLocalScraper{
		ThumbnailMode: "base64",
		ThumbnailPath: "/.thumbnail",
	}
}

// NewBookLocalScraperWithConfig 创建带完整配置的书籍本地刮削器
func NewBookLocalScraperWithConfig(thumbnailMode, thumbnailPath string) *BookLocalScraper {
	if thumbnailMode == "" {
		thumbnailMode = "base64"
	}
	if thumbnailPath == "" {
		thumbnailPath = "/.thumbnail"
	}
	return &BookLocalScraper{
		ThumbnailMode: thumbnailMode,
		ThumbnailPath: thumbnailPath,
	}
}

// ScrapeBookLocal 从书籍文件流中提取封面图片，填充基本信息。
// reader 为书籍文件内容流，不能为 nil。
// 返回提取到的封面 base64 字符串（data URI），提取失败返回空字符串。
// 注意：本函数不会将文件路径作为 cover 降级，cover 为空表示提取失败。
func (s *BookLocalScraper) ScrapeBookLocal(item *model.MediaItem, reader io.Reader) error {
	ext := strings.ToLower(getFileExt(item.FileName))

	// 根据文件类型补充 MimeType
	if item.MimeType == "" {
		item.MimeType = mimeTypeByBookExt(ext)
	}
	// 去掉扩展名作为展示名
	if item.ScrapedName == "" {
		item.ScrapedName = trimExt(item.FileName)
	}

	// 有已有封面或要求保留时不读取、生成或写入候选封面。
	if reader != nil && !s.PreserveCover && item.Cover == "" {
		data, err := readAllLimited(reader, MaxBookFileBytes)
		if err == nil && len(data) > 0 {
			var coverData []byte
			switch ext {
			case ".epub":
				coverData = extractEpubCoverData(data)
			case ".pdf":
				coverData = extractPDFCoverData(data)
			}
			if len(coverData) > 0 {
				cover := s.storeCover(strings.TrimSuffix(item.FolderPath, "/")+"/"+item.FileName, coverData)
				if cover != "" {
					item.Cover = cover
				}
			}
		}
	}

	now := time.Now()
	item.ScrapedAt = &now
	return nil
}

// ExtractLocalCover 从书籍文件流中提取封面并根据配置存储（供外部调用）。
// 提取失败返回空字符串，不降级为文件路径。
func (s *BookLocalScraper) ExtractLocalCover(fileName string, filePath string, reader io.Reader) string {
	if reader == nil || s.PreserveCover {
		return ""
	}
	ext := strings.ToLower(getFileExt(fileName))
	data, err := readAllLimited(reader, MaxBookFileBytes)
	if err != nil || len(data) == 0 {
		return ""
	}
	var coverData []byte
	switch ext {
	case ".epub":
		coverData = extractEpubCoverData(data)
	case ".pdf":
		coverData = extractPDFCoverData(data)
	}
	if len(coverData) == 0 {
		return ""
	}
	return s.storeCover(filePath, coverData)
}

// storeCover 根据 ThumbnailMode 存储封面数据，返回 Cover 字段值
func (s *BookLocalScraper) storeCover(filePath string, coverData []byte) string {
	if s.PreserveCover {
		return ""
	}
	if NormalizeThumbMode(s.ThumbnailMode) == ThumbModeBase64 {
		return imageDataToBase64Thumb(coverData)
	}
	thumb := resizeCoverBytes(coverData)
	if len(thumb) == 0 {
		thumb = coverData
	}
	return SaveThumb(context.Background(), s.ThumbnailMode, s.ThumbnailPath, filePath, "image/jpeg", thumb)
}

// resizeCoverBytes 将封面缩小至双边不超过 300px，返回 JPEG 字节
func resizeCoverBytes(data []byte) []byte {
	img, err := decodeBounded(data)
	if err != nil {
		return nil
	}
	thumb := boundedCoverThumbnail(img)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// ── EPUB 封面提取 ─────────────────────────────────────────────────────────────

// epubContainer 解析 META-INF/container.xml
type epubContainer struct {
	Rootfiles []struct {
		FullPath string `xml:"full-path,attr"`
	} `xml:"rootfiles>rootfile"`
}

// epubOPF 解析 OPF 文件，找封面 item
type epubOPF struct {
	Metadata struct {
		Metas []struct {
			Name    string `xml:"name,attr"`
			Content string `xml:"content,attr"`
		} `xml:"meta"`
	} `xml:"metadata"`
	Manifest struct {
		Items []struct {
			ID        string `xml:"id,attr"`
			Href      string `xml:"href,attr"`
			MediaType string `xml:"media-type,attr"`
		} `xml:"item"`
	} `xml:"manifest"`
}

// extractEpubCoverData 从 EPUB（zip）数据中提取封面图片原始字节
func extractEpubCoverData(data []byte) []byte {
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil
	}

	// 建立文件名→ZipFile 映射（忽略大小写）
	fileMap := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		fileMap[strings.ToLower(f.Name)] = f
	}

	// 1. 尝试读取 META-INF/container.xml 找 OPF 路径
	opfPath := ""
	if cf, ok := fileMap["meta-inf/container.xml"]; ok {
		if rc, err := cf.Open(); err == nil {
			var container epubContainer
			if xml.NewDecoder(rc).Decode(&container) == nil && len(container.Rootfiles) > 0 {
				opfPath = container.Rootfiles[0].FullPath
			}
			rc.Close()
		}
	}

	// OPF 所在目录（用于拼接相对路径）
	opfDir := ""
	if idx := strings.LastIndex(opfPath, "/"); idx >= 0 {
		opfDir = opfPath[:idx+1]
	}

	// 2. 从 OPF 中找封面 item ID
	coverHref := ""
	if opfPath != "" {
		if of, ok := fileMap[strings.ToLower(opfPath)]; ok {
			if rc, err := of.Open(); err == nil {
				var opf epubOPF
				if xml.NewDecoder(rc).Decode(&opf) == nil {
					// 先找 <meta name="cover" content="..."/> 指向的 item id
					coverItemID := ""
					for _, m := range opf.Metadata.Metas {
						if strings.EqualFold(m.Name, "cover") && m.Content != "" {
							coverItemID = m.Content
							break
						}
					}
					// 在 manifest 中找对应 item
					for _, item := range opf.Manifest.Items {
						if coverItemID != "" && item.ID == coverItemID {
							coverHref = opfDir + item.Href
							break
						}
						// 如果没有指定 cover id，找第一个图片类型的 item
						if coverItemID == "" && strings.Contains(strings.ToLower(item.MediaType), "image") {
							// 优先选择 id 或 href 中包含 "cover" 的
							if strings.Contains(strings.ToLower(item.ID), "cover") ||
								strings.Contains(strings.ToLower(item.Href), "cover") {
								coverHref = opfDir + item.Href
								break
							}
						}
					}
					// 如果还没找到，取第一个图片
					if coverHref == "" && coverItemID == "" {
						for _, item := range opf.Manifest.Items {
							if strings.Contains(strings.ToLower(item.MediaType), "image") {
								coverHref = opfDir + item.Href
								break
							}
						}
					}
				}
				rc.Close()
			}
		}
	}

	// 3. 常见封面文件名候选（按优先级）
	candidates := []string{}
	if coverHref != "" {
		candidates = append(candidates, strings.ToLower(coverHref))
	}
	candidates = append(candidates,
		"cover.jpg", "cover.jpeg", "cover.png",
		"images/cover.jpg", "images/cover.jpeg", "images/cover.png",
		"oebps/cover.jpg", "oebps/images/cover.jpg",
		"oebps/cover.jpeg", "oebps/images/cover.jpeg",
	)

	for _, name := range candidates {
		if f, ok := fileMap[name]; ok {
			if rc, err := f.Open(); err == nil {
				imgData, readErr := readAllLimited(rc, MaxEmbeddedEntryBytes)
				rc.Close()
				if readErr == nil && len(imgData) > 0 {
					return imgData
				}
			}
		}
	}
	return nil
}

// extractEpubCoverBase64 从 EPUB（zip）数据中提取封面图片，返回 data URI base64（向后兼容）
func extractEpubCoverBase64(data []byte) string {
	coverData := extractEpubCoverData(data)
	if len(coverData) == 0 {
		return ""
	}
	return imageDataToBase64Thumb(coverData)
}

// ── PDF 封面提取 ──────────────────────────────────────────────────────────────

// extractPDFCoverData 提取 PDF 封面，返回图片字节。
//
// 优先用纯 Go 光栅化渲染第 1 页：矢量封面、扫描件、用了 Flate/JPX 压缩的
// 内嵌图都能出图，比「扫字节流找 JPEG」可靠得多。
// 渲染失败（加密、结构异常、超时）时退回内嵌 JPEG 扫描，保证最差也有老行为。
func extractPDFCoverData(data []byte) []byte {
	if cover := renderPDFFirstPage(data); len(cover) > 0 {
		return cover
	}
	return scanPDFEmbeddedJPEG(data)
}

// renderPDFFirstPage 用 go-pdfkit/render 渲染 PDF 第 1 页并缩放到封面尺寸。
//
// MaxPixels 限制页面输出像素，MaxDuration 只约束渲染阶段。
// reader.Open 的内部解压仍缺少硬预算，并发上限不能代替单任务资源限制。
var pdfRenderSem = make(chan struct{}, 2)

func renderPDFFirstPage(data []byte) []byte {
	pdfRenderSem <- struct{}{}
	defer func() { <-pdfRenderSem }()
	doc, err := reader.Open(data)
	if err != nil || doc.PageCount() < 1 {
		return nil
	}
	img, err := render.Page(doc, 1, render.Options{
		Scale:       1, // 72 DPI 足够当封面，后面再缩到 300px
		MaxPixels:   20_000_000,
		MaxDuration: 8 * time.Second,
	})
	// ErrTimedOut 会返回「画了一半」的图，仍然可用；其他错误放弃
	if img == nil || (err != nil && !errors.Is(err, render.ErrTimedOut)) {
		return nil
	}
	thumb := boundedCoverThumbnail(img.ToNRGBA())
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// scanPDFEmbeddedJPEG 兜底：从 PDF 字节流中找到第一个内嵌 JPEG（SOI 0xFFD8 → EOI 0xFFD9）。
func scanPDFEmbeddedJPEG(data []byte) []byte {
	// 扫描 JPEG SOI (0xFF 0xD8 0xFF) 标记
	for i := 0; i < len(data)-2; i++ {
		if data[i] == 0xFF && data[i+1] == 0xD8 && data[i+2] == 0xFF {
			// 找对应的 EOI (0xFF 0xD9)
			for j := i + 2; j < len(data)-1; j++ {
				if data[j] == 0xFF && data[j+1] == 0xD9 {
					jpegData := data[i : j+2]
					// 验证是否为有效图片（至少 1KB，避免误匹配小缩略图）
					if len(jpegData) > 1024 {
						return jpegData
					}
					break
				}
			}
		}
	}
	return nil
}

// extractPDFCoverBase64 从 PDF 数据中提取第一个内嵌 JPEG 图片流，返回 data URI base64（向后兼容）
func extractPDFCoverBase64(data []byte) string {
	coverData := extractPDFCoverData(data)
	if len(coverData) == 0 {
		return ""
	}
	return imageDataToBase64Thumb(coverData)
}

// ── 公共辅助 ──────────────────────────────────────────────────────────────────

func boundedCoverThumbnail(img image.Image) image.Image {
	width, height := img.Bounds().Dx(), img.Bounds().Dy()
	if width <= 300 && height <= 300 {
		return img
	}
	if width >= height {
		height = int(int64(height) * 300 / int64(width))
		width = 300
	} else {
		width = int(int64(width) * 300 / int64(height))
		height = 300
	}
	if width < 1 {
		width = 1
	}
	if height < 1 {
		height = 1
	}
	return imaging.Resize(img, width, height, imaging.Lanczos)
}

// imageDataToBase64Thumb 将图片字节解码并缩小至双边不超过 300px，返回 data URI base64
func imageDataToBase64Thumb(data []byte) string {
	img, err := decodeBounded(data)
	if err != nil {
		// 超限图片不做任何保留；解码失败但魔数是合法栅格格式时，原样 base64（可能是解码器不支持的变体）
		if errors.Is(err, ErrTooLarge) || len(data) > MaxRawCoverFallbackBytes {
			return ""
		}
		mimeType := imgmime.Sniff(data)
		if mimeType == "" {
			return ""
		}
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}
	thumb := boundedCoverThumbnail(img)
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 80}); err != nil {
		return ""
	}
	return "data:image/jpeg;base64," + base64.StdEncoding.EncodeToString(buf.Bytes())
}

// detectImageMime 根据文件头检测图片 MIME 类型
func detectImageMime(data []byte) string {
	if len(data) < 4 {
		return "image/jpeg"
	}
	switch {
	case data[0] == 0xFF && data[1] == 0xD8:
		return "image/jpeg"
	case data[0] == 0x89 && data[1] == 0x50 && data[2] == 0x4E && data[3] == 0x47:
		return "image/png"
	case data[0] == 0x47 && data[1] == 0x49 && data[2] == 0x46:
		return "image/gif"
	case data[0] == 0x52 && data[1] == 0x49 && data[2] == 0x46 && data[3] == 0x46:
		return "image/webp"
	default:
		return "image/jpeg"
	}
}

// getFileExt 获取文件扩展名（含点号，小写）
func getFileExt(fileName string) string {
	if idx := strings.LastIndex(fileName, "."); idx >= 0 {
		return fileName[idx:]
	}
	return ""
}

// mimeTypeByBookExt 根据书籍文件扩展名返回 MIME 类型
func mimeTypeByBookExt(ext string) string {
	switch ext {
	case ".pdf":
		return "application/pdf"
	case ".epub":
		return "application/epub+zip"
	case ".mobi":
		return "application/x-mobipocket-ebook"
	case ".azw3":
		return "application/vnd.amazon.ebook"
	case ".txt":
		return "text/plain"
	case ".djvu":
		return "image/vnd.djvu"
	case ".cbz":
		return "application/vnd.comicbook+zip"
	case ".cbr":
		return "application/vnd.comicbook-rar"
	default:
		return "application/octet-stream"
	}
}
