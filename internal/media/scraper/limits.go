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
	// MaxImagePixels 解码前允许的最大像素总量（约 5657x5657）。
	// 解码后按 RGBA 计算约 128MB，默认 5 路并发刮削最坏约 640MB；
	// 原来的 64MP 单张就要 256MB，并发时会接近 1.3GB。
	MaxImagePixels int64 = 32_000_000
	// MaxRawCoverFallbackBytes 解码失败时退回「原始字节 base64 写库」的上限。
	// 超过就直接放弃封面——否则单条记录可能写入 16MB 以上的 base64。
	MaxRawCoverFallbackBytes = 512 << 10
)

// ErrTooLarge 内容超过读取上限
var ErrTooLarge = errors.New("scraper: content exceeds size limit")

// decodeSem 限制同时解码的图片数：解码阶段的内存峰值 ≈ 像素数 × 4，
// 只靠单张上限无法约束多路并发叠加后的总量。
// 容量必须小于默认刮削并发（5），否则信号量等于不起作用：
// 每路最坏 32MP × 4B ≈ 128MB，12 路就是 1.5GB。
// 解码是 CPU 密集型操作，并发超过核数也不会更快，取 3 兼顾内存与吞吐。
var decodeSem = make(chan struct{}, 3)

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
	// 用 int64 相乘：32 位平台上 int 溢出会让超限图片绕过检查
	if cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width)*int64(cfg.Height) > MaxImagePixels {
		return nil, ErrTooLarge
	}
	decodeSem <- struct{}{}
	defer func() { <-decodeSem }()
	img, _, err := image.Decode(bytes.NewReader(data))
	return img, err
}
