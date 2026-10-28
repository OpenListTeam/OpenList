package handles

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	log "github.com/sirupsen/logrus"
	_ "golang.org/x/image/webp"
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
	// 导出封面的并发度与总量上限：远程封面是网络 IO，串行导出大库会非常慢；
	// 但要给内存兜底，避免一次导出把整库封面都读进内存。
	coverFetchWorkers   = 4
	maxExportCovers     = 5000
	maxExportCoverBytes = 512 << 20
	maxImportJSONBytes  = 64 << 20
	maxImportBytes      = maxExportCoverBytes + maxImportJSONBytes + (1 << 20)
	maxImportEntries    = maxExportCovers + 1
	maxCoverPixels      = 40_000_000
)

// coverBytes 读取封面内容（data: / thumb: / http(s)），失败返回 nil。
// 返回的扩展名用于压缩包内的文件名。
func coverBytes(ctx context.Context, cover string) ([]byte, string) {
	cover = strings.TrimSpace(cover)
	if cover == "" || ctx.Err() != nil {
		return nil, ""
	}
	var data []byte
	var err error
	switch {
	case strings.HasPrefix(cover, "data:"):
		idx := strings.Index(cover, ";base64,")
		if idx < 0 {
			return nil, ""
		}
		encoded := cover[idx+len(";base64,"):]
		if len(encoded) > base64.StdEncoding.EncodedLen(maxCoverBytes) {
			return nil, ""
		}
		data, err = base64.StdEncoding.DecodeString(encoded)
	case strings.HasPrefix(cover, "thumb:/"):
		if _, pathErr := mediaExportThumbPath(cover); pathErr != nil {
			return nil, ""
		}
		dir := thumbsDirName()
		root, openErr := os.OpenRoot(scraper.RuntimeThumbRoot(dir))
		if openErr != nil {
			return nil, ""
		}
		defer root.Close()
		name := strings.TrimPrefix(cover, "thumb:/"+dir+"/")
		f, openErr := root.Open(filepath.FromSlash(name))
		if openErr != nil {
			return nil, ""
		}
		defer f.Close()
		stat, statErr := f.Stat()
		if statErr != nil || !stat.Mode().IsRegular() || stat.Size() > maxCoverBytes {
			return nil, ""
		}
		data, err = io.ReadAll(io.LimitReader(f, maxCoverBytes+1))
	case strings.HasPrefix(cover, "http://"), strings.HasPrefix(cover, "https://"):
		u, parseErr := url.Parse(cover)
		if parseErr != nil || safeCoverURL(u) != nil || u.User != nil {
			return nil, ""
		}
		fetchCtx, cancel := context.WithTimeout(ctx, coverFetchTimeout)
		defer cancel()
		resp, fetchErr := fetchCover(fetchCtx, u.String())
		if fetchErr != nil {
			return nil, ""
		}
		defer resp.Body.Close()
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxCoverBytes+1))
	default:
		return nil, ""
	}
	if err != nil || ctx.Err() != nil || len(data) == 0 || len(data) > maxCoverBytes {
		return nil, ""
	}
	ext := rasterCoverExt(data)
	if ext == "" {
		return nil, ""
	}
	return data, ext
}

func safeMediaRelativePath(name string) bool {
	if name == "" || strings.ContainsAny(name, `\:`) || strings.ContainsRune(name, 0) ||
		strings.HasPrefix(name, "/") || filepath.IsAbs(name) || filepath.VolumeName(name) != "" {
		return false
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." || strings.TrimRight(part, " .") != part {
			return false
		}
	}
	return true
}

func mediaExportThumbPath(cover string) (string, error) {
	dir := thumbsDirName()
	prefix := "thumb:/" + dir + "/"
	if !safeMediaRelativePath(dir) || !strings.HasPrefix(cover, prefix) {
		return "", fmt.Errorf("封面不在配置的缩略图目录内")
	}
	name := strings.TrimPrefix(cover, prefix)
	if !safeMediaRelativePath(name) {
		return "", fmt.Errorf("无效的封面路径")
	}
	root, err := filepath.Abs(scraper.RuntimeThumbRoot(dir))
	if err != nil {
		return "", err
	}
	full := filepath.Join(root, filepath.FromSlash(name))
	if !mediaPathWithin(root, full) {
		return "", fmt.Errorf("封面路径越界")
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return "", err
	}
	full, err = filepath.EvalSymlinks(full)
	if err != nil || !mediaPathWithin(root, full) {
		return "", fmt.Errorf("封面符号链接越界")
	}
	return full, nil
}

func mediaPathWithin(root, full string) bool {
	rel, err := filepath.Rel(root, full)
	return err == nil && rel != "." && rel != ".." && !filepath.IsAbs(rel) &&
		!strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func rasterCoverExt(data []byte) string {
	ext := coverFileExt(data)
	if ext == "" {
		return ""
	}
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || cfg.Width <= 0 || cfg.Height <= 0 ||
		int64(cfg.Width) > maxCoverPixels/int64(cfg.Height) {
		return ""
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return ""
	}
	return ext
}

type coverBlob struct {
	data []byte
	ext  string
}

// fetchCovers 并发读取封面内容（key 为 items 下标）。
// 单个封面失败只跳过它自己，不影响整次导出；总量超限后不再继续读取。
func fetchCovers(ctx context.Context, items []model.MediaItem) map[int]coverBlob {
	return fetchCoversWithBudget(ctx, items, maxExportCovers, maxExportCoverBytes)
}

func fetchCoversWithBudget(ctx context.Context, items []model.MediaItem, countLimit, byteLimit int) map[int]coverBlob {
	out := make(map[int]coverBlob)
	if countLimit <= 0 || byteLimit <= 0 {
		return out
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var mu sync.Mutex
	total := 0
	var wg sync.WaitGroup
	sem := make(chan struct{}, coverFetchWorkers)
	count := 0
schedule:
	for i := range items {
		if strings.TrimSpace(items[i].Cover) == "" {
			continue
		}
		if count >= countLimit {
			log.Warnf("导出封面达到数量上限 %d", countLimit)
			break
		}
		select {
		case <-ctx.Done():
			break schedule
		case sem <- struct{}{}:
		}
		mu.Lock()
		stop := ctx.Err() != nil || total >= byteLimit
		mu.Unlock()
		if stop {
			<-sem
			break
		}
		count++
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			defer func() { <-sem }()
			raw, ext := coverBytes(ctx, items[i].Cover)
			if len(raw) == 0 {
				return
			}
			mu.Lock()
			defer mu.Unlock()
			if ctx.Err() != nil {
				return
			}
			if len(raw) > byteLimit-total {
				cancel()
				return
			}
			total += len(raw)
			out[i] = coverBlob{data: raw, ext: ext}
			if total >= byteLimit {
				cancel()
			}
		}(i)
	}
	wg.Wait()
	return out
}

// writeMediaExportZip 把导出数据打包成 zip：JSON + covers/。
// 会就地改写 items[].cover 为压缩包内的相对路径。
func writeMediaExportZip(ctx context.Context, w io.Writer, data *MediaExportData) error {
	covers := fetchCovers(ctx, data.Items)
	if err := ctx.Err(); err != nil {
		return err
	}
	zw := zip.NewWriter(&mediaContextWriter{ctx: ctx, w: w})
	defer zw.Close()
	used := map[string]bool{}
	for i := range data.Items {
		if err := ctx.Err(); err != nil {
			return err
		}
		blob, ok := covers[i]
		if !ok {
			continue
		}
		ext := blob.ext
		id := data.Items[i].ID
		name := fmt.Sprintf("item-%d%s", id, ext)
		for n := 1; used[name]; n++ {
			name = fmt.Sprintf("item-%d-%d%s", id, n, ext)
		}
		used[name] = true
		f, err := zw.Create(mediaExportCoverDir + "/" + name)
		if err != nil {
			return err
		}
		if _, err := f.Write(blob.data); err != nil {
			return err
		}
		data.Items[i].Cover = mediaExportCoverDir + "/" + name
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

type mediaContextWriter struct {
	ctx context.Context
	w   io.Writer
}

func (w *mediaContextWriter) Write(p []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.w.Write(p)
}

// readMediaExportZip 解析导出的 zip：JSON 里的 covers/xxx 会落盘到本地缩略图目录，
// 并改写回 thumb:/<dir>/<name> 形式。
func readMediaExportZip(r io.ReaderAt, size int64) (*MediaExportData, error) {
	data, _, err := readMediaExportZipWithBudget(context.Background(), r, size, defaultMediaImportLimits())
	return data, err
}

type mediaImportLimits struct {
	entries    int
	jsonBytes  int64
	coverBytes int64
}

type mediaImportedCover struct {
	path  string
	cover string
}

func defaultMediaImportLimits() mediaImportLimits {
	return mediaImportLimits{maxImportEntries, maxImportJSONBytes, maxExportCoverBytes}
}

func readMediaExportZipWithBudget(ctx context.Context, r io.ReaderAt, size int64, limits mediaImportLimits) (*MediaExportData, []mediaImportedCover, error) {
	if size < 0 || size > maxImportBytes || ctx.Err() != nil {
		return nil, nil, fmt.Errorf("导入压缩包过大或请求已取消")
	}
	if err := checkMediaZipEntryBudget(ctx, r, size, limits.entries); err != nil {
		return nil, nil, err
	}
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, nil, fmt.Errorf("压缩包解析失败: %w", err)
	}
	if len(zr.File) > limits.entries {
		return nil, nil, fmt.Errorf("压缩包条目数量超限")
	}
	files := make(map[string]*zip.File, len(zr.File))
	for _, f := range zr.File {
		if _, exists := files[f.Name]; exists {
			return nil, nil, fmt.Errorf("压缩包包含重复条目: %s", f.Name)
		}
		files[f.Name] = f
	}
	jsonFile := files[mediaExportJSONName]
	if jsonFile == nil {
		return nil, nil, fmt.Errorf("压缩包里没有 %s", mediaExportJSONName)
	}
	rawJSON, err := readMediaZipEntry(ctx, jsonFile, limits.jsonBytes)
	if err != nil {
		return nil, nil, err
	}
	var data MediaExportData
	if err := json.Unmarshal(rawJSON, &data); err != nil {
		return nil, nil, fmt.Errorf("导入文件解析失败: %w", err)
	}
	if data.Version <= 0 || data.Version > mediaExportVersion {
		return nil, nil, fmt.Errorf("不支持的导入文件版本")
	}
	references := make([]string, 0)
	seen := make(map[string]bool)
	var declaredBytes uint64
	for _, item := range data.Items {
		name := item.Cover
		if !strings.HasPrefix(name, mediaExportCoverDir+"/") || seen[name] {
			continue
		}
		if !safeMediaRelativePath(strings.TrimPrefix(name, mediaExportCoverDir+"/")) {
			return nil, nil, fmt.Errorf("无效的压缩包封面路径")
		}
		f := files[name]
		if f == nil || !f.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("压缩包封面不存在或不是普通文件: %s", name)
		}
		if limits.coverBytes < 0 || f.UncompressedSize64 > maxCoverBytes ||
			f.UncompressedSize64 > uint64(limits.coverBytes)-declaredBytes {
			return nil, nil, fmt.Errorf("导入封面总大小超限")
		}
		declaredBytes += f.UncompressedSize64
		seen[name] = true
		references = append(references, name)
	}
	if len(references) == 0 {
		return &data, nil, nil
	}
	dir := thumbsDirName()
	if !safeMediaRelativePath(dir) {
		return nil, nil, fmt.Errorf("无效的缩略图配置路径")
	}
	root := scraper.RuntimeThumbRoot(dir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, nil, err
	}
	stage, err := os.MkdirTemp(root, ".media-import-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(stage)
	saved := make(map[string]string, len(references))
	pending := make([]mediaImportedCover, 0, len(references))
	complete := false
	defer func() {
		if !complete {
			for _, cover := range pending {
				_ = os.Remove(cover.path)
			}
		}
	}()
	var total int64
	for i, name := range references {
		raw, err := readMediaZipEntry(ctx, files[name], min(int64(maxCoverBytes), limits.coverBytes-total))
		if err != nil {
			return nil, nil, err
		}
		ext := rasterCoverExt(raw)
		if ext == "" {
			return nil, nil, fmt.Errorf("压缩包封面不是合法栅格图片: %s", name)
		}
		total += int64(len(raw))
		local := fmt.Sprintf("import-%s-%d%s", strings.TrimPrefix(filepath.Base(stage), ".media-import-"), i, ext)
		if err := os.WriteFile(filepath.Join(stage, local), raw, 0o644); err != nil {
			return nil, nil, err
		}
		cover := "thumb:/" + dir + "/" + local
		saved[name] = cover
		pending = append(pending, mediaImportedCover{filepath.Join(root, local), cover})
	}
	for _, cover := range pending {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		if err := os.Rename(filepath.Join(stage, filepath.Base(cover.path)), cover.path); err != nil {
			return nil, nil, err
		}
	}
	for i := range data.Items {
		if cover, ok := saved[data.Items[i].Cover]; ok {
			data.Items[i].Cover = cover
		}
	}
	complete = true
	return &data, pending, nil
}

func checkMediaZipEntryBudget(ctx context.Context, r io.ReaderAt, size int64, limit int) error {
	if size < 22 || limit < 0 {
		return fmt.Errorf("无效的 ZIP 中心目录")
	}
	tail := make([]byte, min(size, int64(22+65535)))
	if _, err := r.ReadAt(tail, size-int64(len(tail))); err != nil {
		return err
	}
	end := -1
	for i := len(tail) - 22; i >= 0; i-- {
		if binary.LittleEndian.Uint32(tail[i:]) == 0x06054b50 &&
			i+22+int(binary.LittleEndian.Uint16(tail[i+20:])) == len(tail) {
			end = i
			break
		}
	}
	if end < 0 {
		return fmt.Errorf("无效的 ZIP 中心目录")
	}
	directoryEnd := size - int64(len(tail)) + int64(end)
	count := uint64(binary.LittleEndian.Uint16(tail[end+10:]))
	directorySize := uint64(binary.LittleEndian.Uint32(tail[end+12:]))
	directoryOffset := uint64(binary.LittleEndian.Uint32(tail[end+16:]))
	if count == 0xffff || directorySize == 0xffffffff || directoryOffset == 0xffffffff {
		var locator [20]byte
		if directoryEnd < 20 {
			return fmt.Errorf("无效的 ZIP64 中心目录")
		}
		if _, err := r.ReadAt(locator[:], directoryEnd-20); err != nil || binary.LittleEndian.Uint32(locator[:]) != 0x07064b50 {
			return fmt.Errorf("无效的 ZIP64 中心目录")
		}
		offset := binary.LittleEndian.Uint64(locator[8:])
		if size < 56 || offset > uint64(size-56) {
			return fmt.Errorf("无效的 ZIP64 中心目录偏移")
		}
		var record [56]byte
		if _, err := r.ReadAt(record[:], int64(offset)); err != nil || binary.LittleEndian.Uint32(record[:]) != 0x06064b50 {
			return fmt.Errorf("无效的 ZIP64 中心目录")
		}
		count = binary.LittleEndian.Uint64(record[32:])
		directorySize = binary.LittleEndian.Uint64(record[40:])
		directoryOffset = binary.LittleEndian.Uint64(record[48:])
		directoryEnd = int64(offset)
	}
	if count > uint64(limit) || directorySize > uint64(directoryEnd) {
		return fmt.Errorf("压缩包条目数量超限或中心目录无效")
	}
	position := directoryEnd - int64(directorySize)
	if directoryOffset < uint64(position) {
		var signature [4]byte
		if _, err := r.ReadAt(signature[:], int64(directoryOffset)); err == nil && binary.LittleEndian.Uint32(signature[:]) == 0x02014b50 {
			position = int64(directoryOffset)
		}
	}
	var header [46]byte
	actual := 0
	for position < directoryEnd {
		if err := ctx.Err(); err != nil {
			return err
		}
		if actual >= limit || directoryEnd-position < int64(len(header)) {
			return fmt.Errorf("压缩包条目数量超限或中心目录无效")
		}
		if _, err := r.ReadAt(header[:], position); err != nil || binary.LittleEndian.Uint32(header[:]) != 0x02014b50 {
			return fmt.Errorf("无效的 ZIP 中心目录条目")
		}
		position += int64(len(header)) + int64(binary.LittleEndian.Uint16(header[28:])) +
			int64(binary.LittleEndian.Uint16(header[30:])) + int64(binary.LittleEndian.Uint16(header[32:]))
		actual++
	}
	if position != directoryEnd || uint64(actual) != count {
		return fmt.Errorf("ZIP 中心目录条目数量不匹配")
	}
	return nil
}

func readMediaZipEntry(ctx context.Context, f *zip.File, limit int64) ([]byte, error) {
	if limit < 0 || f.UncompressedSize64 > uint64(limit) {
		return nil, fmt.Errorf("压缩包条目解压大小超限: %s", f.Name)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	raw, err := io.ReadAll(io.LimitReader(&mediaContextReader{ctx: ctx, r: rc}, limit+1))
	if err != nil || int64(len(raw)) > limit {
		return nil, fmt.Errorf("压缩包条目读取失败或大小超限: %s", f.Name)
	}
	return raw, nil
}

type mediaContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *mediaContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

// isZipPayload 通过文件头判断是否为 zip（PK\x03\x04）
func isZipPayload(data []byte) bool {
	return len(data) >= 4 && data[0] == 'P' && data[1] == 'K' && data[2] == 0x03 && data[3] == 0x04
}
