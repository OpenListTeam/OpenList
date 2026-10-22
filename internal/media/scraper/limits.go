package scraper

import (
	"bytes"
	"errors"
	"image"
	"io"
)

// 刮削阶段会把文件整体读入内存再解码，必须限制单文件大小与像素总量，
// 否则几路并发刮削一张超大图片/压缩炸弹就能把进程内存打满。
const (
	// MaxImageFileBytes 单张待刮削图片的最大字节数
	MaxImageFileBytes int64 = 32 << 20
	// MaxBookFileBytes 单本待提取封面的书籍（epub/pdf）最大字节数
	MaxBookFileBytes int64 = 128 << 20
	// MaxEmbeddedEntryBytes epub(zip) 内单个封面条目的最大解压字节数，防止 zip 炸弹
	MaxEmbeddedEntryBytes int64 = 16 << 20
	// MaxImagePixels 解码前允许的最大像素总量（约 8000x8000）
	MaxImagePixels = 64_000_000
)

// ErrTooLarge 内容超过读取上限
var ErrTooLarge = errors.New("scraper: content exceeds size limit")

// readAllLimited 最多读取 max 字节；超过上限返回 ErrTooLarge（不会继续读取剩余内容）。
func readAllLimited(r io.Reader, max int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > max {
		return nil, ErrTooLarge
	}
	return data, nil
}

// decodeBounded 先只读取图片头部获取尺寸，超过像素上限直接拒绝，再做完整解码。
func decodeBounded(data []byte) (image.Image, error) {
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > MaxImagePixels {
		return nil, ErrTooLarge
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}
