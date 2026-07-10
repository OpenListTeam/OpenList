package thumb

import (
	"bufio"
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/gen2brain/heic"
	"golang.org/x/image/draw"
	_ "golang.org/x/image/webp"
	"golang.org/x/sync/singleflight"
)

var (
	thumbDir string
	once     sync.Once
	flights  singleflight.Group
)

func initThumbDir() {
	thumbDir = filepath.Join(conf.Conf.TempDir, "thumb")
	_ = os.MkdirAll(thumbDir, 0755)
}

func cachePath(path string, size int) string {
	once.Do(initThumbDir)
	h := md5.Sum([]byte(fmt.Sprintf("%s:%d", path, size)))
	key := hex.EncodeToString(h[:])
	return filepath.Join(thumbDir, key+".jpg")
}

func readLink(ctx context.Context, link *model.Link) (io.ReadCloser, error) {
	if link.URL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, link.URL, nil)
		if err != nil {
			return nil, err
		}
		for k, v := range link.Header {
			req.Header[k] = v
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
		}
		return resp.Body, nil
	}
	if link.RangeReader != nil {
		return link.RangeReader.RangeRead(ctx, http_range.Range{Start: 0, Length: -1})
	}
	return nil, errors.New("no readable source")
}

func resize(src image.Image, size int) image.Image {
	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w <= size && h <= size {
		return src
	}
	ratio := float64(size) / float64(w)
	if rh := float64(size) / float64(h); rh < ratio {
		ratio = rh
	}
	newW := int(float64(w) * ratio)
	newH := int(float64(h) * ratio)
	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), draw.Over, nil)
	return dst
}

// imageFormat 通过文件头识别图片格式，返回小写格式名或空字符串。
func imageFormat(header []byte) string {
	if len(header) < 12 {
		return ""
	}
	switch {
	// JPEG
	case header[0] == 0xFF && header[1] == 0xD8:
		return "jpeg"
	// PNG
	case bytes.Equal(header[:8], []byte{0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A}):
		return "png"
	// GIF
	case bytes.Equal(header[:6], []byte("GIF87a")) || bytes.Equal(header[:6], []byte("GIF89a")):
		return "gif"
	// WebP
	case bytes.Equal(header[:4], []byte("RIFF")) && bytes.Equal(header[8:12], []byte("WEBP")):
		return "webp"
	// HEIF/HEIC/AVIF: ISO Base Media File Format (ftyp box)
	case bytes.Equal(header[4:8], []byte("ftyp")):
		brand := string(header[8:12])
		switch brand {
		case "heic", "heix", "hevc", "hevx", "heim", "heis", "hevm", "hevs", "mif1", "msf1", "avif", "avis":
			return "heif"
		}
	}
	return ""
}

// decodeImage 根据文件头选择对应的解码器。HEIF 使用 gen2brain/heic，其余使用标准库。
func decodeImage(r io.Reader, format string) (image.Image, error) {
	if format == "heif" {
		return heic.Decode(r)
	}
	img, _, err := image.Decode(r)
	return img, err
}

// GenerateHEIFThumb 解码 HEIF/HEIC 文件并生成指定尺寸的 JPEG 缩略图，返回缓存文件路径。
// 若文件扩展名是 heif/heic 但实际为 JPEG/PNG/GIF/WebP，则按标准图解码并缩放，避免 heic 解码器报错。
// 相同路径并发请求只会执行一次解码。
func GenerateHEIFThumb(ctx context.Context, link *model.Link, path string, size int) (string, error) {
	once.Do(initThumbDir)
	cache := cachePath(path, size)
	if _, err := os.Stat(cache); err == nil {
		return cache, nil
	}

	v, err, _ := flights.Do(cache, func() (interface{}, error) {
		if _, err := os.Stat(cache); err == nil {
			return cache, nil
		}

		rc, err := readLink(ctx, link)
		if err != nil {
			return "", err
		}
		defer rc.Close()

		// 预读文件头识别真实格式，同时保留 bufio.Reader 供后续解码器复用
		br := bufio.NewReaderSize(rc, 32*1024)
		header, err := br.Peek(12)
		if err != nil {
			return "", fmt.Errorf("peek file header failed: %w", err)
		}
		format := imageFormat(header)
		if format == "" {
			return "", fmt.Errorf("unsupported image format, header: %x", header)
		}

		img, err := decodeImage(br, format)
		if err != nil {
			return "", fmt.Errorf("decode %s failed: %w", format, err)
		}

		thumb := resize(img, size)
		f, err := os.Create(cache)
		if err != nil {
			return "", err
		}
		defer f.Close()
		if err := jpeg.Encode(f, thumb, &jpeg.Options{Quality: 80}); err != nil {
			return "", err
		}
		return cache, nil
	})
	if err != nil {
		return "", err
	}
	return v.(string), nil
}
