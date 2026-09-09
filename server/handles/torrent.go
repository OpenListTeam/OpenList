package handles

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	stdpath "path"
	"slices"
	"strings"
	"time"

	_189pc "github.com/OpenListTeam/OpenList/v4/drivers/189pc"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
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
	if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, reqPath)) || !common.CanWrite(user, meta, reqPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	if len(req.TorrentData) > maxTorrentBase64Len {
		common.ErrorStrResp(c, "torrent data exceeds the maximum size", 413)
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
	// 对于仅返回 URL（无 RangeReader）的驱动，GetRangeReaderFromLink 会退化为 HTTP Range 流式读取
	rangeReader, err := stream.GetRangeReaderFromLink(obj.GetSize(), link)
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("该存储不支持流式读取，无法生成 torrent: %w", err), 400)
		return
	}

	// 读取整个文件
	rc, err := rangeReader.RangeRead(c.Request.Context(), http_range.Range{Length: obj.GetSize()})
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

// SeedDataReq carries a bounded base64-encoded seed document.
// SeedData is intentionally NOT bound with `required` here: some requests that
// embed SeedDataReq (e.g. SeedCapabilityReq preflight) accept `paths` instead of
// `seed_data`. Required-ness is enforced inside decodeSeedData for the handlers
// that actually consume a seed document.
type SeedDataReq struct {
	SeedData string `json:"seed_data"`
	Format   string `json:"format"`
	FileName string `json:"file_name"`
}

// SeedConvertReq requests a container conversion without changing metadata.
type SeedConvertReq struct {
	SeedDataReq
	TargetFormat string `json:"target_format"`
	Format       string `json:"format"`
	Path         string `json:"path"`
}

// SeedHashSelection controls whole-file and piece hash inclusion.
type SeedHashSelection struct {
	Whole  bool `json:"whole"`
	Pieces bool `json:"pieces"`
}

// SeedHashMatrix controls the optional hash metadata stored in a seed.
type SeedHashMatrix struct {
	MD5    SeedHashSelection `json:"md5"`
	SHA1   SeedHashSelection `json:"sha1"`
	SHA256 SeedHashSelection `json:"sha256"`
}

// SeedGenerateReq generates one or more seed containers for existing files.
type SeedGenerateReq struct {
	Paths               []string              `json:"paths" binding:"required"`
	Format              string                `json:"format"`
	Formats             []string              `json:"formats"`
	Name                string                `json:"name"`
	Comment             string                `json:"comment"`
	FileComments        map[string]string     `json:"file_comments"`
	HashMatrix          SeedHashMatrix        `json:"hash_matrix"`
	PieceSize           int64                 `json:"piece_size"`
	Trackers            []string              `json:"trackers"`
	Channels            []torrent.SeedChannel `json:"channels"`
	OutputPath          string                `json:"output_path"`
	SavePath            string                `json:"save_path"`
	IncludeShare        bool                  `json:"include_share"`
	IncludeDirectSource bool                  `json:"include_direct_source"`
	ShareFiles          []string              `json:"share_files"`
	DirectFiles         []string              `json:"direct_files"`
}

// SeedCapabilityReq supports source preflight and destination import planning.
type SeedCapabilityReq struct {
	SeedDataReq
	Paths    []string `json:"paths"`
	Path     string   `json:"path"`
	Override string   `json:"policy"`
}

// SeedUpdateChannelsReq replaces only public channel metadata.
type SeedUpdateChannelsReq struct {
	SeedDataReq
	Channels []torrent.SeedChannel `json:"channels"`
}

// SeedRecalcFile maps one seed file to a server-side path for re-hashing.
type SeedRecalcFile struct {
	Path       string `json:"path"`        // seed file relative path
	SourcePath string `json:"source_path"` // server-side readable file path
}

// SeedUpdateReq edits metadata and optionally recalculates hashes.
type SeedUpdateReq struct {
	SeedDataReq
	Comment      *string                         `json:"comment"`
	Trackers     []string                        `json:"trackers"`
	Channels     []torrent.SeedChannel           `json:"channels"`
	FileComments map[string]string               `json:"file_comments"`
	FileSources  map[string][]torrent.SeedSource `json:"file_sources"`
	RemoveFiles  []string                        `json:"remove_files"`
	Recalculate  bool                            `json:"recalculate"`
	RecalcFiles  []SeedRecalcFile                `json:"recalc_files"`
	HashMatrix   SeedHashMatrix                  `json:"hash_matrix"`
	PieceSize    int64                           `json:"piece_size"`
	OutputPath   string                          `json:"output_path"`
	SavePath     string                          `json:"save_path"`
	Options      map[string]any                  `json:"options"`
}

// SeedQuickSaveReq imports selected seed files using rapid upload or an existing source URL.
type SeedQuickSaveReq struct {
	SeedDataReq
	Path          string         `json:"path" binding:"required"`
	Files         []string       `json:"files"`
	SelectedFiles []int          `json:"selected_files"`
	Tool          string         `json:"tool"`
	DeletePolicy  string         `json:"delete_policy"`
	Overwrite     bool           `json:"overwrite"`
	TransitPath   string         `json:"transit_path"`
	UpdateChannel bool           `json:"update_channel"`
	Options       map[string]any `json:"options"`
}

func decodeSeedData(req SeedDataReq) ([]byte, *torrent.Seed, string, error) {
	if strings.TrimSpace(req.SeedData) == "" {
		return nil, nil, "", fmt.Errorf("seed_data is required")
	}
	if len(req.SeedData) > maxTorrentBase64Len {
		return nil, nil, "", fmt.Errorf("seed data is too large")
	}
	data, err := base64.StdEncoding.DecodeString(req.SeedData)
	if err != nil {
		return nil, nil, "", fmt.Errorf("invalid base64 seed data: %w", err)
	}
	format := strings.ToLower(strings.TrimPrefix(req.Format, "."))
	if format == "" {
		format = torrent.DetectFormat(req.FileName, data)
	}
	seed, err := torrent.DecodeSeed(data, format, torrent.DefaultParseLimits())
	if err != nil {
		return nil, nil, "", err
	}
	return data, seed, format, nil
}

// ParseSeed parses OSS, torrent or CAS data into one preview contract.
func ParseSeed(c *gin.Context) {
	var req SeedDataReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, seed, format, err := decodeSeedData(req)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	hasSource := false
	hasRapidHashes := false
	for _, file := range seed.Files {
		hasSource = hasSource || firstUsableSeedSource(file) != ""
		hasRapidHashes = hasRapidHashes || file.Hashes.MD5 != "" || file.Hashes.SHA1 != "" || file.Hashes.SHA256 != ""
	}
	common.SuccessResp(c, gin.H{
		"format":      format,
		"seed":        seed,
		"files":       seed.Files,
		"total_size":  seedTotalSize(seed),
		"diagnostics": seedDiagnostics(seed),
		"conversions": seedConversionStates(seed),
		"capabilities": gin.H{
			"rapid_upload": hasRapidHashes, "offline_download": hasSource || format == "torrent",
			"transfer": hasSource, "convert": true, "edit": true, "recalculate": true,
		},
		"direct_preview": len(seed.Files) == 1 && setting.GetBool(conf.SeedSingleDirectPreview),
	})
}

// UploadSeedAndParse parses a multipart seed upload with the same limits as JSON parsing.
func UploadSeedAndParse(c *gin.Context) {
	file, err := c.FormFile("seed")
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if file.Size < 0 || file.Size > torrent.DefaultMaxSeedSize {
		common.ErrorStrResp(c, "seed file is too large", 400)
		return
	}
	r, err := file.Open()
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, torrent.DefaultMaxSeedSize+1))
	if err != nil || int64(len(data)) > torrent.DefaultMaxSeedSize {
		common.ErrorStrResp(c, "failed to read bounded seed file", 400)
		return
	}
	format := torrent.DetectFormat(file.Filename, data)
	seed, err := torrent.DecodeSeed(data, format, torrent.DefaultParseLimits())
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, gin.H{
		"format":      format,
		"seed":        seed,
		"seed_data":   base64.StdEncoding.EncodeToString(data),
		"diagnostics": seedDiagnostics(seed),
	})
}

// ConvertSeed converts between OSS, standard torrent+x-openlist and CAS containers.
func ConvertSeed(c *gin.Context) {
	var req SeedConvertReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, seed, _, err := decodeSeedData(req.SeedDataReq)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	targetFormat := req.TargetFormat
	if targetFormat == "" {
		targetFormat = req.Format
	}
	if targetFormat == "" {
		common.ErrorStrResp(c, "target format is required", 400)
		return
	}
	diagnostics := torrent.DiagnoseConversion(seed, targetFormat)
	if len(diagnostics) > 0 {
		common.SuccessResp(c, gin.H{"convertible": false, "diagnostics": diagnostics})
		return
	}
	data, err := torrent.EncodeSeed(seed, targetFormat)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	format := strings.ToLower(strings.TrimPrefix(targetFormat, "."))
	fileName := stdpath.Base(seed.Name) + "." + format
	result := gin.H{
		"convertible": true, "format": format, "name": fileName,
		"data": base64.StdEncoding.EncodeToString(data), "seed_data": base64.StdEncoding.EncodeToString(data), "size": len(data),
	}
	if req.Path != "" {
		user := c.Request.Context().Value(conf.UserKey).(*model.User)
		dstDir, joinErr := user.JoinPath(req.Path)
		if joinErr != nil {
			common.ErrorResp(c, joinErr, 403)
			return
		}
		meta, metaErr := op.GetNearestMeta(dstDir)
		if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
			common.ErrorResp(c, metaErr, 500, true)
			return
		}
		if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, dstDir)) || !common.CanWrite(user, meta, dstDir) {
			common.ErrorResp(c, errs.PermissionDenied, 403)
			return
		}
		fileStream := &stream.FileStream{Ctx: c.Request.Context(), Obj: &model.Object{Name: fileName, Size: int64(len(data)), Modified: time.Now()}, Reader: bytes.NewReader(data), Mimetype: "application/octet-stream"}
		if err = fs.PutDirectly(c.Request.Context(), dstDir, fileStream); err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		result["path"] = stdpath.Join(req.Path, fileName)
	}
	common.SuccessResp(c, result)
}

// DiagnoseSeed reports conversion requirements for every supported container.
func DiagnoseSeed(c *gin.Context) {
	var req SeedDataReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, seed, _, err := decodeSeedData(req)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, seedDiagnostics(seed))
}

func seedMatrixEmpty(matrix SeedHashMatrix) bool {
	return !matrix.MD5.Whole && !matrix.MD5.Pieces && !matrix.SHA1.Whole && !matrix.SHA1.Pieces && !matrix.SHA256.Whole && !matrix.SHA256.Pieces
}

// loadSeedDefaultMatrix parses the configured default right-click hash matrix.
func loadSeedDefaultMatrix() SeedHashMatrix {
	var matrix SeedHashMatrix
	if raw := strings.TrimSpace(setting.GetStr(conf.SeedDefaultMatrix)); raw != "" {
		_ = json.Unmarshal([]byte(raw), &matrix)
	}
	return matrix
}

func normalizedSeedMatrix(matrix SeedHashMatrix, formats []string) SeedHashMatrix {
	if seedMatrixEmpty(matrix) {
		matrix = loadSeedDefaultMatrix()
	}
	if seedMatrixEmpty(matrix) {
		matrix = SeedHashMatrix{
			MD5: SeedHashSelection{Whole: true, Pieces: true}, SHA1: SeedHashSelection{Whole: true, Pieces: true},
			SHA256: SeedHashSelection{Whole: true, Pieces: true},
		}
	}
	for _, format := range formats {
		switch format {
		case "torrent":
			matrix.SHA1 = SeedHashSelection{Whole: true, Pieces: true}
		case "cas":
			matrix.MD5 = SeedHashSelection{Whole: true, Pieces: true}
		}
	}
	return matrix
}

func applySeedMatrix(file *torrent.SeedFile, matrix SeedHashMatrix) {
	if !matrix.MD5.Whole {
		file.Hashes.MD5 = ""
	}
	if !matrix.SHA1.Whole {
		file.Hashes.SHA1 = ""
	}
	if !matrix.SHA256.Whole {
		file.Hashes.SHA256 = ""
	}
	if file.Hashes.Pieces == nil {
		return
	}
	if !matrix.MD5.Pieces {
		file.Hashes.Pieces.MD5 = nil
	}
	if !matrix.SHA1.Pieces {
		file.Hashes.Pieces.SHA1 = nil
	}
	if !matrix.SHA256.Pieces {
		file.Hashes.Pieces.SHA256 = nil
	}
	if len(file.Hashes.Pieces.MD5) == 0 && len(file.Hashes.Pieces.SHA1) == 0 && len(file.Hashes.Pieces.SHA256) == 0 {
		file.Hashes.Pieces = nil
	}
}

// toSeedGenerateParams converts the HTTP request into the transport-agnostic
// params consumed by fs.GenerateSeedArtifacts.
func toSeedGenerateParams(req SeedGenerateReq) fs.SeedGenerateParams {
	return fs.SeedGenerateParams{
		Paths:        req.Paths,
		Formats:      req.Formats,
		Name:         req.Name,
		Comment:      req.Comment,
		FileComments: req.FileComments,
		HashMatrix: fs.SeedHashMatrix{
			MD5:    fs.SeedHashSelection(req.HashMatrix.MD5),
			SHA1:   fs.SeedHashSelection(req.HashMatrix.SHA1),
			SHA256: fs.SeedHashSelection(req.HashMatrix.SHA256),
		},
		PieceSize:           req.PieceSize,
		Trackers:            req.Trackers,
		Channels:            req.Channels,
		OutputPath:          req.OutputPath,
		IncludeShare:        req.IncludeShare,
		IncludeDirectSource: req.IncludeDirectSource,
		ShareFiles:          req.ShareFiles,
		DirectFiles:         req.DirectFiles,
	}
}

// GenerateSeedForPaths reads each file once while calculating complete hashes.
// Requests exceeding the synchronous size limit are queued as a background task.
func GenerateSeedForPaths(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var req SeedGenerateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Paths) == 0 || len(req.Paths) > torrent.DefaultMaxSeedFiles {
		common.ErrorStrResp(c, "invalid seed file count", 400)
		return
	}
	if len(req.Formats) == 0 {
		req.Formats = []string{req.Format}
	}
	params := toSeedGenerateParams(req)
	async, err := fs.SeedGenerateNeedsAsync(c.Request.Context(), user, req.Paths)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if async {
		outputPath := strings.TrimSpace(req.OutputPath)
		if outputPath == "" {
			outputPath = strings.TrimSpace(req.SavePath)
		}
		if outputPath == "" {
			common.ErrorStrResp(c, "asynchronous seed generation requires an output_path", 400)
			return
		}
		params.OutputPath = outputPath
		taskInfo, addErr := fs.AddSeedGenerateTask(c.Request.Context(), user, params)
		if addErr != nil {
			common.ErrorResp(c, addErr, 500)
			return
		}
		common.SuccessResp(c, gin.H{"task": getTaskInfo(taskInfo), "async": true})
		return
	}
	artifacts, seed, genErr := fs.GenerateSeedArtifacts(c.Request.Context(), user, params)
	if genErr != nil {
		status := 400
		if errors.Is(genErr, errs.PermissionDenied) {
			status = 403
		}
		common.ErrorResp(c, genErr, status)
		return
	}
	common.SuccessResp(c, gin.H{"artifacts": artifacts, "seed": seed})
}

// loadSeedDefaultTrackers returns the configured default tracker list, one per line.
func loadSeedDefaultTrackers() []string {
	raw := strings.TrimSpace(setting.GetStr(conf.SeedDefaultTrackers))
	if raw == "" {
		return nil
	}
	var trackers []string
	for _, line := range strings.Split(raw, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			trackers = append(trackers, trimmed)
		}
	}
	return trackers
}

// directSourceAvailable reports whether an unauthenticated /d/ direct source can be embedded.
// Direct sources are only safe when the file is not encrypted and guest access is allowed.
func directSourceAvailable(meta *model.Meta, path string) bool {
	if setting.GetBool(conf.SignAll) {
		return false
	}
	if isEncrypt(meta, path) {
		return false
	}
	guest, err := op.GetGuest()
	return err == nil && !guest.Disabled
}

// SeedCapabilities reports whether files can use hashes, source URLs or require downloading.
func SeedCapabilities(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var req SeedCapabilityReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Paths) > 0 {
		if len(req.Paths) > torrent.DefaultMaxSeedFiles {
			common.ErrorStrResp(c, "invalid seed file count", 400)
			return
		}
		files := make([]gin.H, 0, len(req.Paths))
		var estimatedTraffic int64
		existing := make(map[string]bool)
		for _, requestedPath := range req.Paths {
			fullPath, joinErr := user.JoinPath(requestedPath)
			if joinErr != nil {
				common.ErrorResp(c, joinErr, 403)
				return
			}
			meta, metaErr := op.GetNearestMeta(fullPath)
			if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
				common.ErrorResp(c, metaErr, 500, true)
				return
			}
			if !common.CanRead(user, meta, fullPath) {
				common.ErrorResp(c, errs.PermissionDenied, 403)
				return
			}
			storage, actualPath, getErr := op.GetStorageAndActualPath(fullPath)
			if getErr != nil {
				common.ErrorResp(c, getErr, 400)
				return
			}
			obj, getErr := op.Get(c.Request.Context(), storage, actualPath)
			if getErr != nil || obj.IsDir() {
				common.ErrorResp(c, fmt.Errorf("seed path must be a readable file: %s", requestedPath), 400)
				return
			}
			available := make([]string, 0, 3)
			hashInfo := obj.GetHash()
			for _, hashType := range []*utils.HashType{utils.MD5, utils.SHA1, utils.SHA256} {
				if hashInfo.GetHash(hashType) != "" {
					available = append(available, hashType.Name)
					existing[hashType.Name] = true
				}
			}
			requiresDownload := len(available) < 3
			var fileTraffic int64
			if requiresDownload {
				fileTraffic = obj.GetSize()
				estimatedTraffic += fileTraffic
			}
			// Probe whether the storage can be streamed for server-side hashing.
			streamable := false
			if link, _, linkErr := op.Link(c.Request.Context(), storage, actualPath, model.LinkArgs{}); linkErr == nil && link != nil {
				if _, rrErr := stream.GetRangeReaderFromLink(obj.GetSize(), link); rrErr == nil {
					streamable = true
				}
				_ = link.Close()
			}
			files = append(files, gin.H{
				"path": requestedPath, "name": obj.GetName(), "size": obj.GetSize(),
				"available_hashes": available, "requires_download": requiresDownload,
				"requires_fetch": requiresDownload, "estimated_traffic": fileTraffic,
				"streamable": streamable, "share_available": user.CanShare(),
				"direct_source_available": directSourceAvailable(meta, fullPath),
			})
		}
		existingHashes := make([]string, 0, len(existing))
		for _, name := range []string{"md5", "sha1", "sha256"} {
			if existing[name] {
				existingHashes = append(existingHashes, name)
			}
		}
		common.SuccessResp(c, gin.H{
			"formats": gin.H{"oss": true, "torrent": true, "cas": len(files) == 1},
			"files":   files, "existing_hashes": existingHashes, "estimated_traffic": estimatedTraffic,
			"default_matrix": loadSeedDefaultMatrix(), "trackers": loadSeedDefaultTrackers(),
		})
		return
	}
	_, seed, _, err := decodeSeedData(req.SeedDataReq)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	dstPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	meta, err := op.GetNearestMeta(dstPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, dstPath)) || !common.CanWrite(user, meta, dstPath) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	storage, _, err := op.GetStorageAndActualPath(dstPath)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, rapid189 := storage.(*_189pc.Cloud189PC)
	_, putURL := storage.(driver.PutURL)
	_, putURLResult := storage.(driver.PutURLResult)
	files := make([]gin.H, 0, len(seed.Files))
	for _, file := range seed.Files {
		hasCAS := rapid189 && file.Hashes.MD5 != "" && file.Hashes.Pieces != nil && len(file.Hashes.Pieces.MD5) > 0
		hasSource := firstUsableSeedSource(file) != ""
		method := "download_required"
		if hasCAS {
			method = "189pc_cas"
		} else if hasSource && (putURL || putURLResult) {
			method = "put_url"
		} else if hasSource {
			method = "offline_download"
		}
		files = append(files, gin.H{"path": file.Path, "method": method, "requires_download": method == "download_required"})
	}
	globalPolicy := setting.GetStr(conf.SeedAutoGeneratePolicy, "off")
	policy := strings.ToLower(strings.TrimSpace(req.Override))
	if policy == "" || policy == "inherit" {
		policy = globalPolicy
	}
	if policy != "on" && policy != "off" {
		common.ErrorStrResp(c, "policy must be off, on, or inherit", 400)
		return
	}
	// Describe the destination driver's rapid-transfer capability surface so the
	// frontend can show which hashes are reusable for instant upload.
	driverSupports := gin.H{
		"cas_rapid":         rapid189,
		"put_url":           putURL || putURLResult,
		"offline_download":  true,
		"rapid_hash_algos":  []string{"md5", "sha1"},
		"rapid_uses_pieces": rapid189,
	}
	common.SuccessResp(c, gin.H{
		"driver": storage.Config().Name, "global_policy": globalPolicy,
		"resolved_policy": policy, "files": files, "driver_supports": driverSupports,
	})
}

// UpdateSeedChannels updates public discovery metadata and keeps the requested container.
func UpdateSeedChannels(c *gin.Context) {
	UpdateSeed(c)
}

// applySeedUpdateOptions folds the frontend options passthrough into typed fields.
func applySeedUpdateOptions(req *SeedUpdateReq) {
	if req.Options == nil {
		return
	}
	if req.Comment == nil {
		if v, ok := req.Options["comment"].(string); ok {
			req.Comment = &v
		}
	}
	if !req.Recalculate {
		if v, ok := req.Options["recalculate"].(bool); ok {
			req.Recalculate = v
		}
	}
}

// validateSeedSource enforces the metadata contract for editable share/direct sources.
func validateSeedSource(src torrent.SeedSource) error {
	if src.Type != "openlist-direct" && src.Type != "openlist-share" {
		return fmt.Errorf("unsupported seed source type %q", src.Type)
	}
	u, err := url.Parse(src.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("invalid seed source URL for %q", src.Type)
	}
	if configured := strings.TrimSpace(setting.GetStr(conf.SeedSiteURL)); configured != "" {
		if site, parseErr := url.Parse(configured); parseErr == nil && !strings.EqualFold(u.Host, site.Host) {
			return fmt.Errorf("seed source host must match the configured site URL")
		}
	}
	if src.Type == "openlist-direct" && !strings.HasPrefix(u.EscapedPath(), "/d/") {
		return fmt.Errorf("openlist-direct source URL must start with /d/")
	}
	if src.Type == "openlist-share" && !strings.HasPrefix(u.EscapedPath(), "/sd/") {
		return fmt.Errorf("openlist-share source URL must start with /sd/")
	}
	return nil
}

// rehashSeedFile reads one server-side file and recomputes its whole and piece hashes.
func rehashSeedFile(c *gin.Context, user *model.User, sourcePath string, pieceSize int64) (torrent.SeedFile, error) {
	var out torrent.SeedFile
	fullPath, err := user.JoinPath(sourcePath)
	if err != nil {
		return out, err
	}
	meta, err := op.GetNearestMeta(fullPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return out, err
	}
	if !common.CanRead(user, meta, fullPath) {
		return out, errs.PermissionDenied
	}
	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return out, err
	}
	obj, err := op.Get(c.Request.Context(), storage, actualPath)
	if err != nil || obj.IsDir() {
		return out, fmt.Errorf("recalculate path must be a readable file: %s", sourcePath)
	}
	if obj.GetSize() < 0 || obj.GetSize() > maxTorrentGenFileSize {
		return out, fmt.Errorf("recalculate file exceeds 1GB limit: %s", sourcePath)
	}
	link, _, err := op.Link(c.Request.Context(), storage, actualPath, model.LinkArgs{})
	if err != nil {
		return out, fmt.Errorf("storage cannot stream %s: %v", sourcePath, err)
	}
	rangeReader, err := stream.GetRangeReaderFromLink(obj.GetSize(), link)
	if err != nil {
		return out, fmt.Errorf("storage cannot stream %s", sourcePath)
	}
	rc, err := rangeReader.RangeRead(c.Request.Context(), http_range.Range{Length: obj.GetSize()})
	if err != nil {
		return out, err
	}
	defer rc.Close()
	hasher := torrent.NewHashWriter(pieceSize, pieceSize)
	n, copyErr := io.Copy(hasher, io.LimitReader(rc, obj.GetSize()+1))
	if copyErr != nil {
		return out, fmt.Errorf("read %s: %w", sourcePath, copyErr)
	}
	if n != obj.GetSize() {
		return out, fmt.Errorf("read %s: got %d of %d bytes", sourcePath, n, obj.GetSize())
	}
	hasher.Finish()
	modified := ""
	if !obj.ModTime().IsZero() {
		modified = obj.ModTime().UTC().Format(time.RFC3339)
	}
	return hasher.BuildSeedFile(stdpath.Base(sourcePath), modified), nil
}

// UpdateSeed edits seed metadata and optionally recalculates hashes from server files.
func UpdateSeed(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var req SeedUpdateReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	applySeedUpdateOptions(&req)
	_, seed, format, err := decodeSeedData(req.SeedDataReq)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if req.Comment != nil {
		seed.Comment = strings.TrimSpace(*req.Comment)
	}
	if req.Trackers != nil {
		seed.Trackers = req.Trackers
	}
	if req.Channels != nil {
		seed.Channels = req.Channels
	}
	for i := range seed.Files {
		if comment, ok := req.FileComments[seed.Files[i].Path]; ok {
			seed.Files[i].Comment = strings.TrimSpace(comment)
		}
		if sources, ok := req.FileSources[seed.Files[i].Path]; ok {
			for _, src := range sources {
				if srcErr := validateSeedSource(src); srcErr != nil {
					common.ErrorResp(c, srcErr, 400)
					return
				}
			}
			seed.Files[i].Sources = sources
		}
	}
	if len(req.RemoveFiles) > 0 {
		removeSet := make(map[string]struct{}, len(req.RemoveFiles))
		for _, path := range req.RemoveFiles {
			if trimmed := strings.TrimSpace(path); trimmed != "" {
				removeSet[trimmed] = struct{}{}
			}
		}
		if len(removeSet) == 0 {
			common.ErrorStrResp(c, "remove_files requires at least one non-empty path", 400)
			return
		}
		kept := make([]torrent.SeedFile, 0, len(seed.Files))
		for _, file := range seed.Files {
			if _, removed := removeSet[file.Path]; !removed {
				kept = append(kept, file)
			}
		}
		if len(kept) == 0 {
			common.ErrorStrResp(c, "cannot remove all files from a seed", 400)
			return
		}
		seed.Files = kept
	}
	if req.Recalculate {
		pieceSize := req.PieceSize
		if pieceSize <= 0 {
			pieceSize = seed.PieceSize
		}
		if pieceSize <= 0 {
			pieceSize = torrent.DefaultPieceSize
		}
		if pieceSize < 16*1024 || pieceSize > 64*1024*1024 {
			common.ErrorStrResp(c, "piece_size must be between 16384 and 67108864", 400)
			return
		}
		seed.PieceSize = pieceSize
		matrix := normalizedSeedMatrix(req.HashMatrix, []string{format})
		recalcMap := make(map[string]string, len(req.RecalcFiles))
		for _, rf := range req.RecalcFiles {
			if strings.TrimSpace(rf.SourcePath) != "" {
				recalcMap[rf.Path] = rf.SourcePath
			}
		}
		if len(recalcMap) == 0 {
			common.ErrorStrResp(c, "recalculate requires at least one source_path", 400)
			return
		}
		for i := range seed.Files {
			srcPath, ok := recalcMap[seed.Files[i].Path]
			if !ok {
				continue
			}
			rehashed, rehashErr := rehashSeedFile(c, user, srcPath, pieceSize)
			if rehashErr != nil {
				common.ErrorResp(c, rehashErr, 400)
				return
			}
			rehashed.Path = seed.Files[i].Path
			rehashed.Comment = seed.Files[i].Comment
			rehashed.Sources = seed.Files[i].Sources
			rehashed.CASSliceMD5 = ""
			rehashed.CASCreateTime = ""
			applySeedMatrix(&rehashed, matrix)
			seed.Files[i] = rehashed
		}
	}
	data, err := torrent.EncodeSeed(seed, format)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	result := gin.H{"format": format, "seed_data": base64.StdEncoding.EncodeToString(data), "seed": seed}
	outputPath := strings.TrimSpace(req.OutputPath)
	if outputPath == "" {
		outputPath = strings.TrimSpace(req.SavePath)
	}
	if outputPath != "" {
		fileName := stdpath.Base(seed.Name) + "." + format
		dstDir, joinErr := user.JoinPath(outputPath)
		if joinErr != nil {
			common.ErrorResp(c, joinErr, 403)
			return
		}
		meta, metaErr := op.GetNearestMeta(dstDir)
		if metaErr != nil && !errors.Is(errors.Cause(metaErr), errs.MetaNotFound) {
			common.ErrorResp(c, metaErr, 500, true)
			return
		}
		if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, dstDir)) || !common.CanWrite(user, meta, dstDir) {
			common.ErrorResp(c, errs.PermissionDenied, 403)
			return
		}
		fileStream := &stream.FileStream{Ctx: c.Request.Context(), Obj: &model.Object{Name: fileName, Size: int64(len(data)), Modified: time.Now()}, Reader: bytes.NewReader(data), Mimetype: "application/octet-stream"}
		if putErr := fs.PutDirectly(c.Request.Context(), dstDir, fileStream); putErr != nil {
			common.ErrorResp(c, putErr, 500)
			return
		}
		result["path"] = stdpath.Join(outputPath, fileName)
	}
	shareStatus := checkSeedShareValidity(seed)
	if len(shareStatus) > 0 {
		result["share_status"] = shareStatus
	}
	common.SuccessResp(c, result)
}

// saveSeedFilesToPath saves selected seed files into one target directory.
func saveSeedFilesToPath(c *gin.Context, user *model.User, seed *torrent.Seed, req SeedQuickSaveReq, dstPath string, rapidOnly bool) ([]gin.H, string, string, error) {
	results := make([]gin.H, 0, len(seed.Files))
	fullPath, err := user.JoinPath(dstPath)
	if err != nil {
		return nil, "", "", err
	}
	meta, err := op.GetNearestMeta(fullPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return nil, "", "", err
	}
	if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(meta, fullPath)) || !common.CanWrite(user, meta, fullPath) {
		return nil, "", "", errs.PermissionDenied
	}
	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return nil, "", "", err
	}
	dstDir, err := op.Get(c.Request.Context(), storage, actualPath)
	if err != nil || !dstDir.IsDir() {
		return nil, "", "", errs.NotFolder
	}
	driverName, mountPath := "", ""
	if mounted := storage.GetStorage(); mounted != nil {
		driverName = strings.TrimSpace(mounted.Driver)
		mountPath = strings.TrimSpace(mounted.MountPath)
	}
	selected := make(map[string]struct{}, len(req.Files))
	for _, name := range req.Files {
		selected[name] = struct{}{}
	}
	selectedIndexes := make(map[int]struct{}, len(req.SelectedFiles))
	for _, index := range req.SelectedFiles {
		if index < 0 || index >= len(seed.Files) {
			return nil, "", "", fmt.Errorf("selected_files contains an invalid index")
		}
		selectedIndexes[index] = struct{}{}
	}
	cloud189, is189 := storage.(*_189pc.Cloud189PC)
	_, putURL := storage.(driver.PutURL)
	_, putURLResult := storage.(driver.PutURLResult)
	for index, file := range seed.Files {
		if len(selected) > 0 {
			if _, ok := selected[file.Path]; !ok {
				continue
			}
		} else if len(selectedIndexes) > 0 {
			if _, ok := selectedIndexes[index]; !ok {
				continue
			}
		}
		name := stdpath.Base(file.Path)
		if is189 && file.Hashes.MD5 != "" && file.Hashes.Pieces != nil && len(file.Hashes.Pieces.MD5) > 0 && len(file.Hashes.Pieces.SHA1) > 0 {
			one := *seed
			one.Name = name
			one.Files = []torrent.SeedFile{file}
			if data, encodeErr := torrent.EncodeSeed(&one, "torrent"); encodeErr == nil {
				if obj, rapidErr := cloud189.RapidUploadFromTorrent(c.Request.Context(), dstDir, data, req.Overwrite); rapidErr == nil {
					results = append(results, gin.H{"path": file.Path, "name": obj.GetName(), "method": "189pc_cas"})
					continue
				}
			}
		}
		if rapidOnly {
			results = append(results, gin.H{"path": file.Path, "name": name, "method": "unavailable", "error": "target driver cannot reuse the available hashes"})
			continue
		}
		source := firstUsableSeedSource(file)
		if source == "" {
			results = append(results, gin.H{"path": file.Path, "name": name, "method": "unavailable", "error": "no usable source URL or compatible rapid-upload hashes"})
			continue
		}
		if putURL || putURLResult {
			if putErr := fs.PutURL(c.Request.Context(), fullPath, name, source); putErr == nil {
				results = append(results, gin.H{"path": file.Path, "name": name, "method": "put_url"})
				continue
			}
		}
		if !user.CanAddOfflineDownloadTasks() {
			results = append(results, gin.H{"path": file.Path, "name": name, "method": "offline_download", "error": "offline download permission is required"})
			continue
		}
		if req.Tool == "" {
			results = append(results, gin.H{"path": file.Path, "name": name, "method": "offline_download", "error": "offline download tool is required"})
			continue
		}
		t, addErr := tool.AddURL(c, &tool.AddURLArgs{URL: source, DstDirPath: fullPath, Tool: req.Tool, DeletePolicy: tool.DeletePolicy(req.DeletePolicy)})
		if addErr != nil {
			results = append(results, gin.H{"path": file.Path, "name": name, "method": "offline_download", "error": addErr.Error()})
			continue
		}
		result := gin.H{"path": file.Path, "name": name, "method": "offline_download"}
		if t != nil {
			result["task"] = getTaskInfo(t)
		}
		results = append(results, result)
	}
	return results, driverName, mountPath, nil
}

// resolveSeedTransitPath extracts the optional transit destination for a relayed save.
func resolveSeedTransitPath(req SeedQuickSaveReq) string {
	transitPath := strings.TrimSpace(req.TransitPath)
	if transitPath == "" && req.Options != nil {
		if mode, ok := req.Options["mode"].(string); ok && mode == "transfer" {
			if tp, ok := req.Options["transit_path"].(string); ok {
				transitPath = strings.TrimSpace(tp)
			}
		}
	}
	return transitPath
}

// transferSeedViaTransit first saves to an intermediate directory then relays to the final target.
func transferSeedViaTransit(c *gin.Context, user *model.User, seed *torrent.Seed, req SeedQuickSaveReq, transitPath string) {
	if !user.CanCopy() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	transitResults, _, _, err := saveSeedFilesToPath(c, user, seed, req, transitPath, false)
	if err != nil {
		common.ErrorResp(c, err, seedSaveErrorCode(err))
		return
	}
	finalDst, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	finalMeta, err := op.GetNearestMeta(finalDst)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		common.ErrorResp(c, err, 500, true)
		return
	}
	if (!user.CanWriteContent() && !common.CanWriteContentBypassUserPerms(finalMeta, finalDst)) || !common.CanWrite(user, finalMeta, finalDst) {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	transitFullPath, err := user.JoinPath(transitPath)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	finalResults := make([]gin.H, 0, len(transitResults))
	for _, res := range transitResults {
		method, _ := res["method"].(string)
		if method == "unavailable" {
			finalResults = append(finalResults, res)
			continue
		}
		name, _ := res["name"].(string)
		if name == "" {
			continue
		}
		// Asynchronous offline downloads cannot be relayed synchronously.
		if method == "offline_download" {
			finalResults = append(finalResults, gin.H{"path": res["path"], "name": name, "method": "transfer", "error": "transit requires a synchronous intermediate save (rapid upload or PutURL)"})
			continue
		}
		srcObjPath := stdpath.Join(transitFullPath, name)
		_, copyErr := fs.Copy(context.WithValue(c.Request.Context(), conf.NoTaskKey, struct{}{}), srcObjPath, finalDst)
		if copyErr != nil {
			finalResults = append(finalResults, gin.H{"path": res["path"], "name": name, "method": "transfer", "error": copyErr.Error()})
			continue
		}
		finalResults = append(finalResults, gin.H{"path": res["path"], "name": name, "method": "transfer"})
	}
	common.SuccessResp(c, gin.H{"results": finalResults})
}

// seedSaveErrorCode maps a save error to an HTTP status code.
func seedSaveErrorCode(err error) int {
	if errors.Is(errors.Cause(err), errs.PermissionDenied) {
		return 403
	}
	if errors.Is(errors.Cause(err), errs.NotFolder) || errors.Is(errors.Cause(err), errs.ObjectNotFound) {
		return 400
	}
	return 500
}

// checkSeedShareValidity reports whether each openlist-share source still resolves to a valid sharing.
func checkSeedShareValidity(seed *torrent.Seed) gin.H {
	status := make(gin.H)
	for _, file := range seed.Files {
		for _, source := range file.Sources {
			if source.Type != "openlist-share" || strings.TrimSpace(source.ShareID) == "" {
				continue
			}
			shareID := strings.TrimSpace(source.ShareID)
			if _, checked := status[shareID]; checked {
				continue
			}
			valid := false
			if sharing, err := op.GetSharingById(shareID, true); err == nil && sharing != nil {
				valid = sharing.Valid()
			}
			status[shareID] = valid
		}
	}
	return status
}

// applyChannelUpdate reflects successful saves as channels and failed saves as missing channels.
func applyChannelUpdate(seed *torrent.Seed, driverName, mountPath string, results []gin.H) {
	if driverName == "" {
		return
	}
	successByPath := make(map[string]bool)
	for _, res := range results {
		filePath, _ := res["path"].(string)
		if filePath == "" {
			continue
		}
		method, _ := res["method"].(string)
		successByPath[filePath] = method != "unavailable"
	}
	anySuccess := false
	for _, ok := range successByPath {
		if ok {
			anySuccess = true
			break
		}
	}
	if anySuccess {
		exists := false
		for _, channel := range seed.Channels {
			if channel.Driver == driverName {
				exists = true
				break
			}
		}
		if !exists {
			seed.Channels = append(seed.Channels, torrent.SeedChannel{Driver: driverName, MountPath: mountPath})
		}
	}
	for i := range seed.Files {
		file := &seed.Files[i]
		ok, tracked := successByPath[file.Path]
		if !tracked {
			continue
		}
		if ok {
			file.MissingChannels = removeString(file.MissingChannels, driverName)
		} else if !containsString(file.MissingChannels, driverName) {
			file.MissingChannels = append(file.MissingChannels, driverName)
		}
	}
}

func containsString(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}

func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
}

// QuickSaveSeed imports selected files without downloading when the target supports it.
func QuickSaveSeed(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	var req SeedQuickSaveReq
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	_, seed, format, err := decodeSeedData(req.SeedDataReq)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	rapidOnly := strings.HasSuffix(c.FullPath(), "/rapid_upload")
	if transitPath := resolveSeedTransitPath(req); transitPath != "" {
		transferSeedViaTransit(c, user, seed, req, transitPath)
		return
	}
	results, driverName, mountPath, err := saveSeedFilesToPath(c, user, seed, req, req.Path, rapidOnly)
	if err != nil {
		common.ErrorResp(c, err, seedSaveErrorCode(err))
		return
	}
	resp := gin.H{"results": results}
	if req.UpdateChannel {
		applyChannelUpdate(seed, driverName, mountPath, results)
		if data, encodeErr := torrent.EncodeSeed(seed, format); encodeErr == nil {
			resp["seed_data"] = base64.StdEncoding.EncodeToString(data)
			resp["seed"] = seed
		}
	}
	common.SuccessResp(c, resp)
}

func firstUsableSeedSource(file torrent.SeedFile) string {
	configuredSite, err := url.Parse(strings.TrimSpace(setting.GetStr(conf.SeedSiteURL)))
	if err != nil || configuredSite.Scheme == "" || configuredSite.Hostname() == "" {
		return ""
	}
	for _, source := range file.Sources {
		candidate, parseErr := url.Parse(source.URL)
		if parseErr != nil || (candidate.Scheme != "http" && candidate.Scheme != "https") || candidate.User != nil {
			continue
		}
		if !strings.EqualFold(candidate.Scheme, configuredSite.Scheme) || !strings.EqualFold(candidate.Host, configuredSite.Host) {
			continue
		}
		validPath := (source.Type == "openlist-direct" && strings.HasPrefix(candidate.EscapedPath(), "/d/")) ||
			(source.Type == "openlist-share" && strings.HasPrefix(candidate.EscapedPath(), "/sd/"))
		if !validPath {
			continue
		}
		if source.ExpiresAt != "" {
			expires, parseErr := time.Parse(time.RFC3339, source.ExpiresAt)
			if parseErr != nil || time.Now().After(expires) {
				continue
			}
		}
		return candidate.String()
	}
	return ""
}

func seedTotalSize(seed *torrent.Seed) int64 {
	var total int64
	for _, file := range seed.Files {
		total += file.Size
	}
	return total
}

func seedDiagnostics(seed *torrent.Seed) gin.H {
	return gin.H{
		"oss":     torrent.DiagnoseConversion(seed, "oss"),
		"torrent": torrent.DiagnoseConversion(seed, "torrent"),
		"cas":     torrent.DiagnoseConversion(seed, "cas"),
	}
}

func seedConversionStates(seed *torrent.Seed) gin.H {
	states := make(gin.H, 3)
	for _, format := range []string{"oss", "torrent", "cas"} {
		missing := torrent.DiagnoseConversion(seed, format)
		states[format] = gin.H{"feasible": len(missing) == 0, "missing": missing}
	}
	return states
}
