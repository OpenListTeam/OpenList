package handles

import (
	"encoding/base64"
	"fmt"
	"io"
	"strings"

	_189pc "github.com/OpenListTeam/OpenList/v4/drivers/189pc"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
)

// maxTorrentBase64Len is the max allowed Base64-encoded torrent size (~10MB decoded)
const maxTorrentBase64Len = 14 * 1024 * 1024

// maxTorrentGenFileSize is the max file size allowed for synchronous torrent generation (1GB)
const maxTorrentGenFileSize = 1 * 1024 * 1024 * 1024

// validateParsedTorrent checks that basic torrent invariants hold.
func validateParsedTorrent(t *torrent.Torrent) error {
	if len(t.Info.Pieces)%20 != 0 {
		return fmt.Errorf("torrent pieces 数据无效：长度必须为 20 的整数倍")
	}
	return nil
}

// ParseTorrentReq 解析 torrent 文件请求
type ParseTorrentReq struct {
	// TorrentData Base64 编码的 torrent 文件内容
	TorrentData string `json:"torrent_data" binding:"required"`
}

// ParseTorrentResp 解析 torrent 文件响应
type ParseTorrentResp struct {
	// Name 种子名称
	Name string `json:"name"`
	// TotalSize 总大小
	TotalSize int64 `json:"total_size"`
	// PieceLength 分片大小
	PieceLength int64 `json:"piece_length"`
	// PieceCount 分片数量
	PieceCount int `json:"piece_count"`
	// InfoHash info_hash（十六进制）
	InfoHash string `json:"info_hash"`
	// Files 文件列表（多文件模式）
	Files []TorrentFileInfo `json:"files"`
	// HasCAS 是否包含 CAS 扩展信息
	HasCAS bool `json:"has_cas"`
	// CAS CAS 扩展信息
	CAS *CASInfoResp `json:"cas,omitempty"`
}

// TorrentFileInfo torrent 中的文件信息
type TorrentFileInfo struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
}

// CASInfoResp CAS 信息响应
type CASInfoResp struct {
	FileMD5   string `json:"file_md5"`
	SliceMD5  string `json:"slice_md5"`
	SliceSize int64  `json:"slice_size"`
	Cloud     string `json:"cloud"`
}

// ParseTorrent 解析 torrent 文件，返回文件列表等信息
func ParseTorrent(c *gin.Context) {
	var req ParseTorrentReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	// 限制 Base64 输入大小（最大 ~10MB decoded）
	if len(req.TorrentData) > maxTorrentBase64Len {
		common.ErrorResp(c, fmt.Errorf("torrent 数据过大（最大 10MB）"), 400)
		return
	}

	// Base64 解码
	torrentData, err := base64.StdEncoding.DecodeString(req.TorrentData)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("无效的 Base64 编码: %w", err), 400)
		return
	}

	// 解析 torrent
	t, err := torrent.Decode(torrentData)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("解析 torrent 失败: %w", err), 400)
		return
	}
	if err := validateParsedTorrent(t); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	resp := ParseTorrentResp{
		Name:        t.Info.Name,
		TotalSize:   t.GetTotalSize(),
		PieceLength: t.Info.PieceLength,
		PieceCount:  len(t.Info.Pieces) / 20,
		InfoHash:    t.GetInfoHashHex(),
		HasCAS:      t.HasCASInfo(),
	}

	// 文件列表
	if len(t.Info.Files) > 0 {
		resp.Files = make([]TorrentFileInfo, 0, len(t.Info.Files))
		for _, f := range t.Info.Files {
			resp.Files = append(resp.Files, TorrentFileInfo{
				Path: strings.Join(f.Path, "/"),
				Size: f.Length,
			})
		}
	} else {
		// 单文件模式
		resp.Files = []TorrentFileInfo{
			{Path: t.Info.Name, Size: t.Info.Length},
		}
	}

	// CAS 信息
	if t.HasCASInfo() {
		resp.CAS = &CASInfoResp{
			FileMD5:   t.CAS.FileMD5,
			SliceMD5:  t.CAS.SliceMD5,
			SliceSize: t.CAS.SliceSize,
			Cloud:     t.CAS.Cloud,
		}
	}

	common.SuccessResp(c, resp)
}

// TorrentRapidUploadReq 从 torrent 秒传请求
type TorrentRapidUploadReq struct {
	// TorrentData Base64 编码的 torrent 文件内容
	TorrentData string `json:"torrent_data" binding:"required"`
	// Path 目标路径
	Path string `json:"path" binding:"required"`
}

// TorrentRapidUpload 从 torrent 文件中提取 CAS 信息尝试秒传到天翼云
func TorrentRapidUpload(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)

	var req TorrentRapidUploadReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	// 检查权限
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if !common.CanWrite(user, meta, reqPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}

	// Base64 解码
	torrentData, err := base64.StdEncoding.DecodeString(req.TorrentData)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("无效的 Base64 编码: %w", err), 400)
		return
	}

	// 解析 torrent
	t, err := torrent.Decode(torrentData)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("解析 torrent 失败: %w", err), 400)
		return
	}

	if !t.HasCASInfo() {
		common.ErrorResp(c, fmt.Errorf("torrent 不包含 CAS 扩展信息，无法秒传"), 400)
		return
	}

	// 获取目标存储
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// 获取目标目录对象
	dstDir, err := op.Get(c.Request.Context(), storage, dstDirActualPath)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("获取目标目录失败: %w", err), 500)
		return
	}
	if !dstDir.IsDir() {
		common.ErrorResp(c, errs.NotFolder, 400)
		return
	}

	// 检查是否是天翼云 PC 驱动
	cloud189PC, ok := storage.(*_189pc.Cloud189PC)
	if !ok {
		common.ErrorResp(c, fmt.Errorf("目标存储不是天翼云PC驱动，不支持 CAS 秒传"), 400)
		return
	}

	// 尝试秒传
	obj, err := cloud189PC.RapidUploadFromTorrent(c.Request.Context(), dstDir, torrentData, true)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("秒传失败: %w", err), 400)
		return
	}

	common.SuccessResp(c, gin.H{
		"message":   "秒传成功",
		"file_name": obj.GetName(),
		"file_size": obj.GetSize(),
	})
}

// UploadTorrentAndParse 通过文件上传方式解析 torrent
func UploadTorrentAndParse(c *gin.Context) {
	file, err := c.FormFile("torrent")
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("获取上传文件失败: %w", err), 400)
		return
	}

	// 限制文件大小（最大 10MB）
	if file.Size > 10*1024*1024 {
		common.ErrorResp(c, fmt.Errorf("torrent 文件过大（最大 10MB）"), 400)
		return
	}

	f, err := file.Open()
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("打开文件失败: %w", err), 500)
		return
	}
	defer f.Close()

	torrentData, err := io.ReadAll(f)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("读取文件失败: %w", err), 500)
		return
	}

	// 解析 torrent
	t, err := torrent.Decode(torrentData)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("解析 torrent 失败: %w", err), 400)
		return
	}
	if err := validateParsedTorrent(t); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	resp := ParseTorrentResp{
		Name:        t.Info.Name,
		TotalSize:   t.GetTotalSize(),
		PieceLength: t.Info.PieceLength,
		PieceCount:  len(t.Info.Pieces) / 20,
		InfoHash:    t.GetInfoHashHex(),
		HasCAS:      t.HasCASInfo(),
	}

	// 文件列表
	if len(t.Info.Files) > 0 {
		resp.Files = make([]TorrentFileInfo, 0, len(t.Info.Files))
		for _, f := range t.Info.Files {
			resp.Files = append(resp.Files, TorrentFileInfo{
				Path: strings.Join(f.Path, "/"),
				Size: f.Length,
			})
		}
	} else {
		resp.Files = []TorrentFileInfo{
			{Path: t.Info.Name, Size: t.Info.Length},
		}
	}

	// CAS 信息
	if t.HasCASInfo() {
		resp.CAS = &CASInfoResp{
			FileMD5:   t.CAS.FileMD5,
			SliceMD5:  t.CAS.SliceMD5,
			SliceSize: t.CAS.SliceSize,
			Cloud:     t.CAS.Cloud,
		}
	}

	// 同时返回 Base64 编码的 torrent 数据，方便后续使用
	common.SuccessResp(c, gin.H{
		"info":         resp,
		"torrent_data": base64.StdEncoding.EncodeToString(torrentData),
	})
}

// GenerateTorrentReq 为指定路径的文件生成 torrent 请求
type GenerateTorrentReq struct {
	// Path 文件在 OpenList 中的路径
	Path string `json:"path" binding:"required"`
	// WithCAS 是否注入 CAS 扩展信息（仅天翼云需要）
	WithCAS bool `json:"with_cas"`
}

// GenerateTorrentForPath 为指定路径的文件生成 torrent
// 这是一个通用接口，适用于所有驱动
// 会获取文件内容计算哈希，然后生成 torrent
func GenerateTorrentForPath(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)

	var req GenerateTorrentReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	// 检查读取权限
	meta, err := op.GetNearestMeta(reqPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if !common.CanRead(user, meta, reqPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}

	// 获取存储和文件信息
	storage, actualPath, err := op.GetStorageAndActualPath(reqPath)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// with_cas 仅支持天翼云PC驱动
	if req.WithCAS {
		if _, is189pc := storage.(*_189pc.Cloud189PC); !is189pc {
			common.ErrorResp(c, fmt.Errorf("CAS 秒传扩展仅支持天翼云PC驱动"), 400)
			return
		}
	}

	// 获取文件对象
	obj, err := op.Get(c.Request.Context(), storage, actualPath)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("获取文件失败: %w", err), 500)
		return
	}
	if obj.IsDir() {
		common.ErrorResp(c, fmt.Errorf("不支持为目录生成 torrent"), 400)
		return
	}

	// 限制可生成 torrent 的文件大小
	if obj.GetSize() > maxTorrentGenFileSize {
		common.ErrorResp(c, fmt.Errorf("文件过大，无法生成 torrent（最大 1GB）"), 400)
		return
	}

	// 获取文件下载链接
	link, _, err := op.Link(c.Request.Context(), storage, actualPath, model.LinkArgs{})
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("获取文件链接失败: %w", err), 500)
		return
	}
	defer link.Close()

	// 通过 RangeReader 获取文件内容并计算哈希生成 torrent
	if link.RangeReader == nil {
		common.ErrorResp(c, fmt.Errorf("该存储不支持流式读取，无法生成 torrent（请先下载文件到本地）"), 400)
		return
	}

	// 读取整个文件
	rc, err := link.RangeReader.RangeRead(c.Request.Context(), http_range.Range{Length: obj.GetSize()})
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("读取文件失败: %w", err), 500)
		return
	}
	defer rc.Close()

	var torrentData []byte
	if req.WithCAS {
		torrentData, err = torrent.GenerateFromReaderWithCAS(rc, obj.GetName(), obj.GetSize(), torrent.DefaultPieceSize)
	} else {
		torrentData, err = torrent.GenerateFromReader(rc, obj.GetName(), obj.GetSize(), torrent.DefaultPieceSize)
	}
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("生成 torrent 失败: %w", err), 500)
		return
	}

	// 解析生成的 torrent 获取 info_hash
	t, _ := torrent.Decode(torrentData)
	var infoHash string
	if t != nil {
		infoHash = t.GetInfoHashHex()
	}

	common.SuccessResp(c, gin.H{
		"torrent_data": base64.StdEncoding.EncodeToString(torrentData),
		"info_hash":    infoHash,
		"file_name":    obj.GetName() + ".torrent",
		"size":         len(torrentData),
		"with_cas":     req.WithCAS,
	})
}
