package handles

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/media/scraper"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

// maxCoverUploadSize 自定义封面上限，与封面代理的 8MB 限制保持一致
const maxCoverUploadSize = 8 << 20

// thumbsDirName 缩略图目录名（相对程序数据目录），与 PublicMediaThumb 共用同一配置
func thumbsDirName() string {
	dir := strings.Trim(setting.GetStr(conf.MediaThumbnailPath, "/imgs"), "/")
	if dir == "" {
		dir = "imgs"
	}
	return dir
}

// coverFileExt 根据文件头判断图片类型，非图片返回空
func coverFileExt(data []byte) string {
	switch http.DetectContentType(data) {
	case "image/jpeg":
		return ".jpg"
	case "image/png":
		return ".png"
	case "image/gif":
		return ".gif"
	case "image/webp":
		return ".webp"
	}
	return ""
}

// isFileOwnedCover 判断封面是否来自媒体文件自身（内嵌 data URL / 本地缩略图 /
// 站内代理 / blob）。这类封面不该被刮削结果覆盖。
func isFileOwnedCover(cover string) bool {
	switch {
	case strings.HasPrefix(cover, "data:"),
		strings.HasPrefix(cover, "thumb:"),
		strings.HasPrefix(cover, "blob:"),
		strings.HasPrefix(cover, "/d/"),
		strings.HasPrefix(cover, "/p/"):
		return true
	}
	return false
}

// saveUploadedCover 把上传的图片写入缩略图目录，返回 cover 字段值（thumb:/<dir>/<name>）
func saveUploadedCover(itemID uint, data []byte) (string, error) {
	ext := coverFileExt(data)
	if ext == "" {
		return "", fmt.Errorf("不支持的图片格式（仅支持 jpg / png / gif / webp）")
	}
	dir := thumbsDirName()
	root := scraper.RuntimeThumbRoot(dir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("cover-%d-%d%s", itemID, time.Now().UnixNano(), ext)
	if err := os.WriteFile(filepath.Join(root, name), data, 0o644); err != nil {
		return "", err
	}
	return "thumb:/" + dir + "/" + name, nil
}

// removeUploadedCover 换封面时删掉上一张自定义封面。
// 只删本接口生成的文件（cover-<id>-<ts>.<ext>），不动刮削缩略图。
func removeUploadedCover(oldCover, newCover string) {
	if oldCover == "" || oldCover == newCover {
		return
	}
	dir := thumbsDirName()
	prefix := "thumb:/" + dir + "/cover-"
	if !strings.HasPrefix(oldCover, prefix) {
		return
	}
	name := strings.TrimPrefix(oldCover, "thumb:/"+dir+"/")
	if name == "" || strings.Contains(name, "/") {
		return
	}
	_ = os.Remove(filepath.Join(scraper.RuntimeThumbRoot(dir), name))
}

// UploadMediaItemCover 后台上传自定义封面
//
//	POST /api/admin/media/items/cover   form-data: id=<item id> & file=<image>
//
// 图片写入 <data>/<thumbnail_path>/cover-<id>-<ts>.<ext>，返回 thumb:/... 形式的
// cover 值；前端用 /api/public/media/thumb?path= 访问，与刮削缩略图同一套机制。
func UploadMediaItemCover(c *gin.Context) {
	id, err := strconv.ParseUint(c.PostForm("id"), 10, 64)
	if err != nil || id == 0 {
		common.ErrorStrResp(c, "无效的 id", 400)
		return
	}
	item, err := db.GetMediaItemByID(uint(id))
	if err != nil {
		common.ErrorResp(c, err, 404)
		return
	}
	fileHeader, err := c.FormFile("file")
	if err != nil {
		common.ErrorStrResp(c, "缺少 file 文件参数", 400)
		return
	}
	if fileHeader.Size > maxCoverUploadSize {
		common.ErrorStrResp(c, "图片过大（上限 8MB）", 400)
		return
	}
	f, err := fileHeader.Open()
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxCoverUploadSize+1))
	if err != nil {
		common.ErrorStrResp(c, "读取上传文件失败", 400)
		return
	}
	if len(data) == 0 || len(data) > maxCoverUploadSize {
		common.ErrorStrResp(c, "图片为空或超过 8MB", 400)
		return
	}

	cover, err := saveUploadedCover(item.ID, data)
	if err != nil {
		common.ErrorStrResp(c, err.Error(), 400)
		return
	}
	old := item.Cover
	item.Cover = cover
	if err := db.UpdateMediaItem(item); err != nil {
		removeUploadedCover(cover, "")
		common.ErrorResp(c, err, 500)
		return
	}
	removeUploadedCover(old, cover)
	common.SuccessResp(c, gin.H{"cover": cover})
}
