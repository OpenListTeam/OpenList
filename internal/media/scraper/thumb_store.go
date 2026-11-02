package scraper

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	stdpath "path"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
)

// 缩略图存储模式
const (
	// ThumbModeBase64 缩略图以 base64 存入数据库
	ThumbModeBase64 = "base64"
	// ThumbModeRuntime 缩略图存入程序数据目录（<data>/<thumbnail_path>）
	ThumbModeRuntime = "runtime"
	// ThumbModeVFS 缩略图写入挂载目录（VFS 路径）
	ThumbModeVFS = "local"
)

// NormalizeThumbMode 归一化缩略图模式，未知/空值按运行目录处理
func NormalizeThumbMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case ThumbModeBase64:
		return ThumbModeBase64
	case ThumbModeVFS, "vfs":
		return ThumbModeVFS
	default:
		return ThumbModeRuntime
	}
}

// RuntimeThumbRoot 返回运行目录模式下缩略图的本地根目录
func RuntimeThumbRoot(thumbPath string) string {
	dir := strings.Trim(strings.TrimSpace(thumbPath), "/")
	if dir == "" {
		dir = "imgs"
	}
	return filepath.Join(conf.DataDir, filepath.FromSlash(dir))
}

// SaveThumb 按模式保存缩略图数据，返回 cover 字段值。
// rawPath 为媒体文件的完整 VFS 路径，用于生成稳定且唯一的文件名。
func SaveThumb(ctx context.Context, mode, thumbPath, rawPath, mimeType string, data []byte) string {
	if len(data) == 0 {
		return ""
	}
	mode = NormalizeThumbMode(mode)
	if mimeType == "" {
		mimeType = "image/jpeg"
	}
	if mode == ThumbModeBase64 {
		return "data:" + mimeType + ";base64," + base64.StdEncoding.EncodeToString(data)
	}

	dir := strings.Trim(strings.TrimSpace(thumbPath), "/")
	if dir == "" {
		dir = "imgs"
	}
	name := thumbName(rawPath)

	if mode == ThumbModeVFS {
		vfsDir := "/" + dir
		vfsFile := stdpath.Join(vfsDir, name)
		file := &stream.FileStream{
			Ctx: ctx,
			Obj: &model.Object{
				Name:     name,
				Size:     int64(len(data)),
				Modified: time.Now(),
			},
			Reader:   bytes.NewReader(data),
			Mimetype: mimeType,
		}
		if err := fs.PutDirectly(ctx, vfsDir, file, true); err != nil {
			return ""
		}
		// 带上签名，兼容开启「签名所有」的站点
		return "/d" + vfsFile + "?sign=" + sign.Sign(vfsFile)
	}

	// 运行目录模式：<data>/<dir>/<name>
	root := RuntimeThumbRoot(dir)
	if err := os.MkdirAll(root, 0755); err != nil {
		return ""
	}
	if err := os.WriteFile(filepath.Join(root, name), data, 0644); err != nil {
		return ""
	}
	return "thumb:/" + stdpath.Join(dir, name)
}

// thumbName 根据媒体文件路径生成缩略图文件名
func thumbName(rawPath string) string {
	sum := sha1.Sum([]byte(rawPath))
	return hex.EncodeToString(sum[:]) + ".jpg"
}

// DownloadCoverToStorage 下载远程封面并按缩略图配置保存，失败返回空字符串
func DownloadCoverToStorage(ctx context.Context, imgURL, rawPath, thumbMode, thumbPath string) string {
	if imgURL == "" ||
		!(strings.HasPrefix(imgURL, "http://") || strings.HasPrefix(imgURL, "https://")) {
		return ""
	}
	data, contentType, err := fetchImageBytes(ctx, imgURL)
	if err != nil {
		// 部分网络环境无法直连图片域名，回退到公共图片网关
		if u, parseErr := url.Parse(imgURL); parseErr == nil {
			gateway := "https://wsrv.nl/?url=" + url.QueryEscape(
				strings.TrimPrefix(strings.TrimPrefix(u.String(), "https://"), "http://"),
			)
			data, contentType, err = fetchImageBytes(ctx, gateway)
		}
	}
	if err != nil || len(data) == 0 {
		return ""
	}
	return SaveThumb(ctx, thumbMode, thumbPath, rawPath, contentType, data)
}

func fetchImageBytes(ctx context.Context, raw string) ([]byte, string, error) {
	client := &http.Client{Timeout: 20 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		return nil, "", err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; OpenList-Media/1.0)")
	resp, err := client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	contentType := resp.Header.Get("Content-Type")
	if contentType == "" {
		contentType = "image/jpeg"
	}
	if !strings.HasPrefix(contentType, "image/") {
		return nil, "", fmt.Errorf("unexpected content type %s", contentType)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20))
	return data, contentType, err
}
