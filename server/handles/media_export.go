package handles

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
)

// 导出/导入的压缩包结构：
//
//	media_export.json           —— MediaExportData（items[].cover 改写为 covers/<file>）
//	covers/<file>               —— 封面图片本体
//
// 这样换机器导入时封面不用再联网抓一次；不带封面的 item 保持原 cover 值不变。
const (
	mediaExportJSONName = "media_export.json"
	mediaExportCoverDir = "covers"
	maxCoverBytes       = 8 << 20
	coverFetchTimeout   = 10 * time.Second
)

// coverBytes 读取封面内容（data: / thumb: / http(s)），失败返回 nil。
// 返回的扩展名用于压缩包内的文件名。
func coverBytes(cover string) ([]byte, string) {
	cover = strings.TrimSpace(cover)
	if cover == "" {
		return nil, ""
	}
	// data URL
	if strings.HasPrefix(cover, "data:") {
		idx := strings.Index(cover, "base64,")
		if idx < 0 {
			return nil, ""
		}
		data, err := base64.StdEncoding.DecodeString(cover[idx+len("base64,"):])
		if err != nil || len(data) == 0 || len(data) > maxCoverBytes {
			return nil, ""
		}
		return data, coverFileExt(data)
	}
	// 本地缩略图 thumb:/<dir>/<name>
	if strings.HasPrefix(cover, "thumb:/") {
		rel := strings.TrimPrefix(cover, "thumb:/")
		root := scraper.RuntimeThumbRoot(strings.SplitN(rel, "/", 2)[0])
		name := rel
		if i := strings.Index(rel, "/"); i >= 0 {
			name = rel[i+1:]
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil || len(data) == 0 || len(data) > maxCoverBytes {
			return nil, ""
		}
		return data, coverFileExt(data)
	}
	// 远程图片
	if strings.HasPrefix(cover, "http://") || strings.HasPrefix(cover, "https://") {
		client := &http.Client{Timeout: coverFetchTimeout}
		resp, err := client.Get(cover)
		if err != nil {
			return nil, ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, ""
		}
		data, err := io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
		if err != nil || len(data) == 0 || len(data) > maxCoverBytes {
			return nil, ""
		}
		return data, coverFileExt(data)
	}
	return nil, ""
}

// writeMediaExportZip 把导出数据打包成 zip：JSON + covers/。
// 会就地改写 items[].cover 为压缩包内的相对路径。
func writeMediaExportZip(w io.Writer, data *MediaExportData) error {
	zw := zip.NewWriter(w)
	used := map[string]bool{}
	for i := range data.Items {
		item := &data.Items[i]
		raw, ext := coverBytes(item.Cover)
		if len(raw) == 0 {
			continue
		}
		if ext == "" {
			ext = ".jpg"
		}
		name := fmt.Sprintf("item-%d%s", item.ID, ext)
		for n := 1; used[name]; n++ {
			name = fmt.Sprintf("item-%d-%d%s", item.ID, n, ext)
		}
		used[name] = true
		f, err := zw.Create(mediaExportCoverDir + "/" + name)
		if err != nil {
			return err
		}
		if _, err := f.Write(raw); err != nil {
			return err
		}
		item.Cover = mediaExportCoverDir + "/" + name
	}

	f, err := zw.Create(mediaExportJSONName)
	if err != nil {
		return err
	}
	if err := json.NewEncoder(f).Encode(data); err != nil {
		return err
	}
	return zw.Close()
}

// readMediaExportZip 解析导出的 zip：JSON 里的 covers/xxx 会落盘到本地缩略图目录，
// 并改写回 thumb:/<dir>/<name> 形式。
func readMediaExportZip(r io.ReaderAt, size int64) (*MediaExportData, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, fmt.Errorf("压缩包解析失败: %w", err)
	}
	var data *MediaExportData
	coverFiles := map[string]*zip.File{}
	for _, f := range zr.File {
		switch {
		case f.Name == mediaExportJSONName:
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			var out MediaExportData
			err = json.NewDecoder(rc).Decode(&out)
			_ = rc.Close()
			if err != nil {
				return nil, fmt.Errorf("导入文件解析失败: %w", err)
			}
			data = &out
		case strings.HasPrefix(f.Name, mediaExportCoverDir+"/"):
			coverFiles[strings.TrimPrefix(f.Name, mediaExportCoverDir+"/")] = f
		}
	}
	if data == nil {
		return nil, fmt.Errorf("压缩包里没有 %s", mediaExportJSONName)
	}
	if len(coverFiles) == 0 {
		return data, nil
	}

	dir := thumbsDirName()
	root := scraper.RuntimeThumbRoot(dir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	saved := map[string]string{} // 压缩包内文件名 -> thumb:/...
	for name, f := range coverFiles {
		rc, err := f.Open()
		if err != nil {
			continue
		}
		buf := bytes.Buffer{}
		_, err = io.Copy(&buf, io.LimitReader(rc, maxCoverBytes+1))
		_ = rc.Close()
		if err != nil || buf.Len() == 0 || buf.Len() > maxCoverBytes {
			continue
		}
		raw := buf.Bytes()
		ext := coverFileExt(raw)
		if ext == "" {
			ext = filepath.Ext(name)
		}
		if ext == "" {
			ext = ".jpg"
		}
		local := fmt.Sprintf("import-%d-%s%s", time.Now().UnixNano(), strings.TrimSuffix(filepath.Base(name), filepath.Ext(name)), ext)
		if err := os.WriteFile(filepath.Join(root, local), raw, 0o644); err != nil {
			continue
		}
		saved[name] = "thumb:/" + dir + "/" + local
	}

	for i := range data.Items {
		cover := data.Items[i].Cover
		if !strings.HasPrefix(cover, mediaExportCoverDir+"/") {
			continue
		}
		if v, ok := saved[strings.TrimPrefix(cover, mediaExportCoverDir+"/")]; ok {
			data.Items[i].Cover = v
		}
	}
	return data, nil
}

// isZipPayload 通过文件头判断是否为 zip（PK\x03\x04）
func isZipPayload(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}
