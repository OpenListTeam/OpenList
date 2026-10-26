package torrent

import (
	"bytes"
	"crypto/md5"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	// DefaultPieceSize 默认分片大小 10MB，与天翼云 CAS 分片大小一致
	DefaultPieceSize int64 = 10 * 1024 * 1024

	// CASExtensionKey torrent 根字典中的 CAS 扩展 key
	CASExtensionKey = "x-cas"

	// CASSliceSizeKey CAS 分片大小 key
	CASSliceSizeKey = "slice_size"
	// CASSliceMD5sKey 每片 MD5 列表 key
	CASSliceMD5sKey = "slice_md5s"
	// CASSliceMD5Key 最终 sliceMd5 key
	CASSliceMD5Key = "slice_md5"
	// CASFileMD5Key 整文件 MD5 key
	CASFileMD5Key = "file_md5"
	// CASCloudKey 云盘类型 key
	CASCloudKey = "cloud"

	// Cloud189 identifies the 天翼云 (189) PC driver CAS slice rule.
	Cloud189 = "189"
	// Cloud115 identifies the 115 driver rapid-upload rule (whole-file SHA1).
	Cloud115 = "115"
	// CloudAliyundriveOpen identifies the aliyundrive_open rapid-upload rule (whole-file SHA1).
	CloudAliyundriveOpen = "aliyundrive_open"

	// OpenListExtensionKey is the optional root-level extension key.
	OpenListExtensionKey = "x-openlist"
	// OSSFormat identifies OpenList sharing seed JSON documents.
	OSSFormat = "openlist-sharing-seed"
	// OSSVersion is the currently supported sharing seed schema version.
	OSSVersion = 1
	// DefaultMaxSeedSize bounds untrusted seed documents.
	DefaultMaxSeedSize int64 = 10 * 1024 * 1024
	// DefaultMaxSeedFiles bounds file-list fan-out in untrusted seeds.
	DefaultMaxSeedFiles = 100000
)

// Seed describes the portable OpenList sharing seed contract.
type Seed struct {
	Format    string        `json:"format"`
	Version   int           `json:"version"`
	Name      string        `json:"name"`
	Comment   string        `json:"comment,omitempty"`
	CreatedAt string        `json:"created_at"`
	CreatedBy string        `json:"created_by"`
	PieceSize int64         `json:"piece_size"`
	Trackers  []string      `json:"trackers,omitempty"`
	Channels  []SeedChannel `json:"channels,omitempty"`
	Files     []SeedFile    `json:"files"`
}

// SeedChannel contains only public storage discovery metadata.
type SeedChannel struct {
	Driver    string `json:"driver"`
	MountPath string `json:"mount_path,omitempty"`
}

// SeedFile describes one relative file in a sharing seed.
type SeedFile struct {
	Path            string       `json:"path"`
	Size            int64        `json:"size"`
	Modified        string       `json:"modified,omitempty"`
	Comment         string       `json:"comment,omitempty"`
	Hashes          SeedHashes   `json:"hashes"`
	Sources         []SeedSource `json:"sources,omitempty"`
	CASSliceMD5     string       `json:"cas_slice_md5,omitempty"`
	CASCreateTime   string       `json:"cas_create_time,omitempty"`
	// CASCloud 标识该文件 CAS 元数据所属的云盘类型（如 "189"）。留空视为 189。
	CASCloud        string       `json:"cas_cloud,omitempty"`
	MissingChannels []string     `json:"missing_channels,omitempty"`
}

// SeedHashes contains whole-file and optional per-piece hashes.
type SeedHashes struct {
	MD5    string           `json:"md5,omitempty"`
	SHA1   string           `json:"sha1,omitempty"`
	SHA256 string           `json:"sha256,omitempty"`
	GCID   string           `json:"gcid,omitempty"`
	Pieces *SeedPieceHashes `json:"pieces,omitempty"`
}

// SeedPieceHashes contains independent per-file piece hashes.
type SeedPieceHashes struct {
	MD5    []string `json:"md5,omitempty"`
	SHA1   []string `json:"sha1,omitempty"`
	SHA256 []string `json:"sha256,omitempty"`
}

// SeedSource is an optional retrievable public or signed source URL.
type SeedSource struct {
	Type      string `json:"type"`
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at,omitempty"`
	ShareID   string `json:"share_id,omitempty"`
}

// CASFileEntry describes one file inside a multi-file .cas payload.
type CASFileEntry struct {
	Name       string   `json:"name"`
	Size       int64    `json:"size"`
	MD5        string   `json:"md5"`
	SliceMD5   string   `json:"sliceMd5"`
	CreateTime string   `json:"create_time"`
	SliceMD5s  []string `json:"slice_md5s,omitempty"`
	SliceSize  int64    `json:"slice_size,omitempty"`
	// Cloud 标识该 CAS 条目所属的云盘类型（如 "189"）；留空视为 189。
	Cloud string `json:"cloud,omitempty"`
}

// CASPayload matches the reference .cas JSON payload. The five legacy fields
// describe a single file (byte-for-byte compatible with the reference project);
// the optional "files" array extends it to multi-file seeds, and the optional
// slice_md5s/slice_size preserve the per-piece MD5 list.
type CASPayload struct {
	Name       string         `json:"name"`
	Size       int64          `json:"size"`
	MD5        string         `json:"md5"`
	SliceMD5   string         `json:"sliceMd5"`
	CreateTime string         `json:"create_time"`
	SliceMD5s  []string       `json:"slice_md5s,omitempty"`
	SliceSize  int64          `json:"slice_size,omitempty"`
	// Cloud 标识该 CAS 载荷所属的云盘类型（如 "189"）；留空视为 189。
	Cloud string         `json:"cloud,omitempty"`
	Files []CASFileEntry `json:"files,omitempty"`
}

// ParseLimits controls resource use while parsing untrusted seeds.
type ParseLimits struct {
	MaxBytes int64
	MaxFiles int
	MaxDepth int
}

// DefaultParseLimits returns conservative public API limits.
func DefaultParseLimits() ParseLimits {
	return ParseLimits{MaxBytes: DefaultMaxSeedSize, MaxFiles: DefaultMaxSeedFiles, MaxDepth: 64}
}

// CASInfo 天翼云 CAS 秒传所需信息
type CASInfo struct {
	// FileMD5 整文件 MD5（大写十六进制）
	FileMD5 string
	// SliceMD5 分片 MD5 的摘要（大写十六进制）
	SliceMD5 string
	// SliceMD5s 每个 10MB 分片的 MD5（大写十六进制）
	SliceMD5s []string
	// SliceSize 分片大小（字节）
	SliceSize int64
	// Cloud 云盘类型标识。可为具体驱动名（如 "189"、"115"、"aliyundrive_open"），
	// 或留空表示由使用方按目标驱动自行判定。不同类型的分片规则可能不同。
	Cloud string
}

// TorrentFile 表示 torrent 中的单个文件
type TorrentFile struct {
	// Length 文件大小（字节）
	Length int64
	// Path 文件路径（多文件模式下的相对路径各段）
	Path []string
	// MD5Sum 文件的 MD5（可选，BT 标准字段）
	MD5Sum string
}

// TorrentInfo torrent 的 info 字典
type TorrentInfo struct {
	// PieceLength 分片大小
	PieceLength int64
	// Pieces 所有分片的 SHA-1 哈希拼接（每 20 字节一个）
	Pieces []byte
	// Name 种子名称（单文件模式为文件名，多文件模式为目录名）
	Name string
	// Length 单文件模式下的文件大小
	Length int64
	// Files 多文件模式下的文件列表
	Files []TorrentFile
	// MD5Sum 单文件模式下的文件 MD5（可选）
	MD5Sum string
}

// Torrent 完整的 torrent 文件结构
type Torrent struct {
	// Info info 字典
	Info TorrentInfo
	// InfoHash info 字典的 SHA-1 哈希（20 字节）
	InfoHash []byte
	// Announce tracker URL
	Announce string
	// AnnounceList tracker 列表
	AnnounceList [][]string
	// CreationDate 创建时间
	CreationDate int64
	// Comment 注释
	Comment string
	// CreatedBy 创建者
	CreatedBy string
	// CAS 天翼云 CAS 扩展信息（存储在 info 字典外部，不影响 info_hash）
	CAS *CASInfo
	// OpenList stores the portable extension outside info so info_hash stays standard.
	OpenList *Seed
}

// NewTorrent 创建一个新的 torrent 结构
func NewTorrent(name string, fileSize int64, fileMD5 string) *Torrent {
	return &Torrent{
		Info: TorrentInfo{
			PieceLength: DefaultPieceSize,
			Name:        name,
			Length:      fileSize,
			MD5Sum:      fileMD5,
		},
		CreationDate: time.Now().Unix(),
		CreatedBy:    "OpenList",
		Comment:      "Generated by OpenList with CAS extension",
	}
}

// SetPieces 设置 SHA-1 分片哈希
func (t *Torrent) SetPieces(pieces []byte) {
	t.Info.Pieces = pieces
}

// SetCASInfo 设置 CAS 扩展信息
func (t *Torrent) SetCASInfo(cas *CASInfo) {
	t.CAS = cas
}

// Encode 将 torrent 编码为 bencode 格式的字节数组
func (t *Torrent) Encode() ([]byte, error) {
	// 构建 info 字典
	infoDict := make(map[string]interface{})
	infoDict["piece length"] = int64(t.Info.PieceLength)
	infoDict["pieces"] = t.Info.Pieces
	infoDict["name"] = t.Info.Name

	if len(t.Info.Files) > 0 {
		// 多文件模式
		files := make([]interface{}, 0, len(t.Info.Files))
		for _, f := range t.Info.Files {
			fileDict := make(map[string]interface{})
			fileDict["length"] = int64(f.Length)
			path := make([]interface{}, 0, len(f.Path))
			for _, p := range f.Path {
				path = append(path, p)
			}
			fileDict["path"] = path
			if f.MD5Sum != "" {
				fileDict["md5sum"] = f.MD5Sum
			}
			files = append(files, fileDict)
		}
		infoDict["files"] = files
	} else {
		// 单文件模式
		infoDict["length"] = int64(t.Info.Length)
		if t.Info.MD5Sum != "" {
			infoDict["md5sum"] = t.Info.MD5Sum
		}
	}

	// 编码 info 字典并计算 info_hash
	infoBytes, err := BencodeEncode(infoDict)
	if err != nil {
		return nil, fmt.Errorf("encode info dict: %w", err)
	}
	infoHashRaw := sha1.Sum(infoBytes)
	t.InfoHash = infoHashRaw[:]

	// 构建根字典
	rootDict := make(map[string]interface{})
	if t.Announce != "" {
		rootDict["announce"] = t.Announce
	}
	if len(t.AnnounceList) > 0 {
		announceList := make([]interface{}, 0, len(t.AnnounceList))
		for _, tier := range t.AnnounceList {
			tierList := make([]interface{}, 0, len(tier))
			for _, url := range tier {
				tierList = append(tierList, url)
			}
			announceList = append(announceList, tierList)
		}
		rootDict["announce-list"] = announceList
	}
	if t.Comment != "" {
		rootDict["comment"] = t.Comment
	}
	if t.CreatedBy != "" {
		rootDict["created by"] = t.CreatedBy
	}
	if t.CreationDate > 0 {
		rootDict["creation date"] = t.CreationDate
	}

	// info 字典使用原始编码的字节（保证 info_hash 一致）
	rootDict["info"] = infoDict

	// CAS 扩展信息（放在 info 外部，不影响 info_hash）
	if t.CAS != nil {
		casDict := make(map[string]interface{})
		casDict[CASCloudKey] = t.CAS.Cloud
		casDict[CASFileMD5Key] = t.CAS.FileMD5
		casDict[CASSliceMD5Key] = t.CAS.SliceMD5
		casDict[CASSliceSizeKey] = t.CAS.SliceSize

		if len(t.CAS.SliceMD5s) > 0 {
			md5List := make([]interface{}, 0, len(t.CAS.SliceMD5s))
			for _, md5 := range t.CAS.SliceMD5s {
				md5List = append(md5List, md5)
			}
			casDict[CASSliceMD5sKey] = md5List
		}
		rootDict[CASExtensionKey] = casDict
	}
	if t.OpenList != nil {
		if err := ValidateSeed(t.OpenList, DefaultParseLimits()); err != nil {
			return nil, fmt.Errorf("validate x-openlist: %w", err)
		}
		rootDict[OpenListExtensionKey] = seedToBencode(t.OpenList)
	}

	return BencodeEncode(rootDict)
}

// Decode 从 bencode 字节数组解析 torrent
func Decode(data []byte) (*Torrent, error) {
	val, err := BencodeDecode(data)
	if err != nil {
		return nil, fmt.Errorf("bencode decode: %w", err)
	}

	rootDict, ok := val.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("torrent: root is not a dict")
	}

	t := &Torrent{}

	// 解析 announce
	if v, ok := rootDict["announce"]; ok {
		if b, ok := v.([]byte); ok {
			t.Announce = string(b)
		}
	}

	// 解析 announce-list
	if v, ok := rootDict["announce-list"]; ok {
		if list, ok := v.([]interface{}); ok {
			for _, tier := range list {
				if tierList, ok := tier.([]interface{}); ok {
					var urls []string
					for _, u := range tierList {
						if b, ok := u.([]byte); ok {
							urls = append(urls, string(b))
						}
					}
					if len(urls) > 0 {
						t.AnnounceList = append(t.AnnounceList, urls)
					}
				}
			}
		}
	}

	// 解析 comment
	if v, ok := rootDict["comment"]; ok {
		if b, ok := v.([]byte); ok {
			t.Comment = string(b)
		}
	}

	// 解析 created by
	if v, ok := rootDict["created by"]; ok {
		if b, ok := v.([]byte); ok {
			t.CreatedBy = string(b)
		}
	}

	// 解析 creation date
	if v, ok := rootDict["creation date"]; ok {
		if n, ok := v.(int64); ok {
			t.CreationDate = n
		}
	}

	// 解析 info 字典
	if infoVal, ok := rootDict["info"]; ok {
		infoDict, ok := infoVal.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("torrent: info is not a dict")
		}

		// 计算 info_hash
		infoBytes, err := BencodeEncode(infoDict)
		if err != nil {
			return nil, fmt.Errorf("encode info for hash: %w", err)
		}
		infoHashRaw := sha1.Sum(infoBytes)
		t.InfoHash = infoHashRaw[:]

		// 解析 info 字段
		if v, ok := infoDict["piece length"]; ok {
			if n, ok := v.(int64); ok {
				t.Info.PieceLength = n
			}
		}
		if v, ok := infoDict["pieces"]; ok {
			if b, ok := v.([]byte); ok {
				t.Info.Pieces = b
			}
		}
		if v, ok := infoDict["name"]; ok {
			if b, ok := v.([]byte); ok {
				t.Info.Name = string(b)
			}
		}
		if v, ok := infoDict["length"]; ok {
			if n, ok := v.(int64); ok {
				t.Info.Length = n
			}
		}
		if v, ok := infoDict["md5sum"]; ok {
			if b, ok := v.([]byte); ok {
				t.Info.MD5Sum = string(b)
			}
		}

		// 解析多文件模式
		if v, ok := infoDict["files"]; ok {
			if files, ok := v.([]interface{}); ok {
				for _, f := range files {
					if fileDict, ok := f.(map[string]interface{}); ok {
						tf := TorrentFile{}
						if l, ok := fileDict["length"]; ok {
							if n, ok := l.(int64); ok {
								tf.Length = n
							}
						}
						if p, ok := fileDict["path"]; ok {
							if pathList, ok := p.([]interface{}); ok {
								for _, pp := range pathList {
									if b, ok := pp.([]byte); ok {
										tf.Path = append(tf.Path, string(b))
									}
								}
							}
						}
						if m, ok := fileDict["md5sum"]; ok {
							if b, ok := m.([]byte); ok {
								tf.MD5Sum = string(b)
							}
						}
						t.Info.Files = append(t.Info.Files, tf)
					}
				}
			}
		}
	}

	// 解析 CAS 扩展
	if casVal, ok := rootDict[CASExtensionKey]; ok {
		if casDict, ok := casVal.(map[string]interface{}); ok {
			cas := &CASInfo{}
			if v, ok := casDict[CASCloudKey]; ok {
				if b, ok := v.([]byte); ok {
					cas.Cloud = string(b)
				}
			}
			if v, ok := casDict[CASFileMD5Key]; ok {
				if b, ok := v.([]byte); ok {
					cas.FileMD5 = string(b)
				}
			}
			if v, ok := casDict[CASSliceMD5Key]; ok {
				if b, ok := v.([]byte); ok {
					cas.SliceMD5 = string(b)
				}
			}
			if v, ok := casDict[CASSliceSizeKey]; ok {
				if n, ok := v.(int64); ok {
					cas.SliceSize = n
				}
			}
			if v, ok := casDict[CASSliceMD5sKey]; ok {
				if list, ok := v.([]interface{}); ok {
					for _, item := range list {
						if b, ok := item.([]byte); ok {
							cas.SliceMD5s = append(cas.SliceMD5s, string(b))
						}
					}
				}
			}
			t.CAS = cas
		}
	}

	if extension, ok := rootDict[OpenListExtensionKey]; ok {
		seed, err := seedFromBencode(extension)
		if err != nil {
			return nil, fmt.Errorf("decode x-openlist: %w", err)
		}
		if err = ValidateSeed(seed, DefaultParseLimits()); err != nil {
			return nil, fmt.Errorf("validate x-openlist: %w", err)
		}
		t.OpenList = seed
	}
	if err := ValidateTorrent(t, DefaultParseLimits()); err != nil {
		return nil, err
	}

	return t, nil
}

// GetInfoHashHex 获取 info_hash 的十六进制字符串
func (t *Torrent) GetInfoHashHex() string {
	return hex.EncodeToString(t.InfoHash)
}

// GetPieceHashes 获取所有分片的 SHA-1 哈希（每个 20 字节）
func (t *Torrent) GetPieceHashes() [][]byte {
	if len(t.Info.Pieces) == 0 {
		return nil
	}
	count := len(t.Info.Pieces) / 20
	hashes := make([][]byte, count)
	for i := 0; i < count; i++ {
		hashes[i] = t.Info.Pieces[i*20 : (i+1)*20]
	}
	return hashes
}

// GetTotalSize 获取 torrent 中所有文件的总大小
func (t *Torrent) GetTotalSize() int64 {
	if len(t.Info.Files) > 0 {
		var total int64
		for _, f := range t.Info.Files {
			total += f.Length
		}
		return total
	}
	return t.Info.Length
}

// HasCASInfo 检查 torrent 是否包含 CAS 扩展信息
func (t *Torrent) HasCASInfo() bool {
	return t.CAS != nil && t.CAS.FileMD5 != "" && t.CAS.SliceMD5 != ""
}

// BuildCASInfoFromMD5s 从分片 MD5 列表构建 CAS 信息（默认按天翼云分片规则标记 cloud=189）。
// 新代码应优先使用 BuildCASInfoFromMD5sWithCloud 显式指定云盘类型。
func BuildCASInfoFromMD5s(fileMD5 string, sliceMD5s []string, sliceSize int64) *CASInfo {
	return BuildCASInfoFromMD5sWithCloud(fileMD5, sliceMD5s, sliceSize, Cloud189)
}

// BuildCASInfoFromMD5sWithCloud 从分片 MD5 列表构建 CAS 信息，并指定云盘类型标识。
func BuildCASInfoFromMD5sWithCloud(fileMD5 string, sliceMD5s []string, sliceSize int64, cloud string) *CASInfo {
	fileMD5 = strings.ToUpper(fileMD5)
	sliceMD5s = upperStrings(sliceMD5s)
	sliceMD5 := SliceMD5FromPieces(sliceMD5s, fileMD5)
	return &CASInfo{
		FileMD5:   fileMD5,
		SliceMD5:  sliceMD5,
		SliceMD5s: sliceMD5s,
		SliceSize: sliceSize,
		Cloud:     cloud,
	}
}

// GetMD5Str 计算字符串的 MD5（大写十六进制）
func GetMD5Str(data string) string {
	h := md5.New()
	h.Write([]byte(data))
	return strings.ToUpper(hex.EncodeToString(h.Sum(nil)))
}

// ValidateTorrent checks standard BitTorrent invariants and portable paths.
func ValidateTorrent(t *Torrent, limits ParseLimits) error {
	if t == nil {
		return fmt.Errorf("missing torrent")
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = DefaultMaxSeedFiles
	}
	if err := validateSeedName(t.Info.Name); err != nil {
		return fmt.Errorf("torrent: %w", err)
	}
	if t.Info.PieceLength <= 0 || t.Info.PieceLength > 1<<30 {
		return fmt.Errorf("torrent: invalid piece length")
	}
	if len(t.Info.Pieces)%sha1.Size != 0 {
		return fmt.Errorf("torrent: pieces length must be a multiple of %d", sha1.Size)
	}
	if len(t.Info.Files) > limits.MaxFiles {
		return fmt.Errorf("torrent: too many files")
	}
	var total int64
	if len(t.Info.Files) == 0 {
		if t.Info.Length < 0 {
			return fmt.Errorf("torrent: invalid file size")
		}
		total = t.Info.Length
	} else {
		seen := make(map[string]struct{}, len(t.Info.Files))
		for _, file := range t.Info.Files {
			filePath := strings.Join(file.Path, "/")
			if err := validateRelativeSeedPath(filePath); err != nil {
				return fmt.Errorf("torrent: %w", err)
			}
			if _, ok := seen[filePath]; ok {
				return fmt.Errorf("torrent: duplicate file path %q", filePath)
			}
			seen[filePath] = struct{}{}
			if file.Length < 0 || total > int64(^uint64(0)>>1)-file.Length {
				return fmt.Errorf("torrent: invalid file size")
			}
			total += file.Length
		}
	}
	expectedPieces := int64(0)
	if total > 0 {
		expectedPieces = (total + t.Info.PieceLength - 1) / t.Info.PieceLength
	}
	if int64(len(t.Info.Pieces)/sha1.Size) != expectedPieces {
		return fmt.Errorf("torrent: piece count mismatch")
	}
	return nil
}

// NewSeed creates a versioned OSS document with stable defaults.
func NewSeed(name, createdBy string, pieceSize int64) *Seed {
	if pieceSize <= 0 {
		pieceSize = DefaultPieceSize
	}
	return &Seed{
		Format:    OSSFormat,
		Version:   OSSVersion,
		Name:      name,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
		CreatedBy: createdBy,
		PieceSize: pieceSize,
	}
}

// ValidateSeed rejects unsafe paths, malformed hashes and unreasonable resource use.
func ValidateSeed(seed *Seed, limits ParseLimits) error {
	if seed == nil {
		return fmt.Errorf("missing seed")
	}
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultMaxSeedSize
	}
	if limits.MaxFiles <= 0 {
		limits.MaxFiles = DefaultMaxSeedFiles
	}
	if seed.Format != OSSFormat {
		return fmt.Errorf("unsupported seed format %q", seed.Format)
	}
	if seed.Version != OSSVersion {
		return fmt.Errorf("unsupported seed version %d", seed.Version)
	}
	if err := validateSeedName(seed.Name); err != nil {
		return err
	}
	if seed.CreatedAt == "" || seed.CreatedBy == "" {
		return fmt.Errorf("created_at and created_by are required")
	}
	if seed.PieceSize < 16*1024 || seed.PieceSize > 64*1024*1024 {
		return fmt.Errorf("piece_size must be between 16384 and 67108864")
	}
	if len(seed.Files) == 0 || len(seed.Files) > limits.MaxFiles {
		return fmt.Errorf("file count must be between 1 and %d", limits.MaxFiles)
	}
	seen := make(map[string]struct{}, len(seed.Files))
	var total int64
	for i := range seed.Files {
		file := &seed.Files[i]
		if err := validateRelativeSeedPath(file.Path); err != nil {
			return fmt.Errorf("file %d: %w", i, err)
		}
		if _, ok := seen[file.Path]; ok {
			return fmt.Errorf("duplicate file path %q", file.Path)
		}
		seen[file.Path] = struct{}{}
		if file.Size < 0 || total > int64(^uint64(0)>>1)-file.Size {
			return fmt.Errorf("file %q has invalid size", file.Path)
		}
		total += file.Size
		if err := validateSeedHashes(file.Hashes, file.Size, seed.PieceSize); err != nil {
			return fmt.Errorf("file %q: %w", file.Path, err)
		}
		if file.CASSliceMD5 != "" && !validHexHash(file.CASSliceMD5, 32) {
			return fmt.Errorf("file %q has an invalid CAS slice MD5", file.Path)
		}
		for _, source := range file.Sources {
			if source.Type == "" || source.URL == "" {
				return fmt.Errorf("file %q has an incomplete source", file.Path)
			}
			u, err := url.Parse(source.URL)
			if err != nil || u.Scheme == "" {
				return fmt.Errorf("file %q has an invalid source URL", file.Path)
			}
		}
		for _, channel := range file.MissingChannels {
			if strings.TrimSpace(channel) == "" || strings.ContainsAny(channel, "/\\\x00") {
				return fmt.Errorf("file %q has an invalid missing channel", file.Path)
			}
		}
	}
	for _, channel := range seed.Channels {
		if strings.TrimSpace(channel.Driver) == "" {
			return fmt.Errorf("channel driver is required")
		}
		if strings.ContainsAny(channel.MountPath, "?#\x00") {
			return fmt.Errorf("channel mount_path contains invalid characters")
		}
	}
	return nil
}

func validateSeedName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\\\x00") {
		return fmt.Errorf("invalid seed name %q", name)
	}
	return nil
}

func validateRelativeSeedPath(name string) error {
	if name == "" || !utf8.ValidString(name) || strings.ContainsAny(name, "\\\x00") || strings.HasPrefix(name, "/") {
		return fmt.Errorf("invalid relative path %q", name)
	}
	cleaned := path.Clean(name)
	if cleaned != name || cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return fmt.Errorf("invalid relative path %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("invalid relative path %q", name)
		}
	}
	return nil
}

func validateSeedHashes(hashes SeedHashes, size, pieceSize int64) error {
	for name, value := range map[string]string{"md5": hashes.MD5, "sha1": hashes.SHA1, "sha256": hashes.SHA256} {
		if value != "" && !validHexHash(value, map[string]int{"md5": 32, "sha1": 40, "sha256": 64}[name]) {
			return fmt.Errorf("invalid %s hash", name)
		}
	}
	if hashes.Pieces == nil {
		return nil
	}
	expected := 0
	if size > 0 {
		expected = int((size + pieceSize - 1) / pieceSize)
	}
	pieceSets := []struct {
		name   string
		width  int
		values []string
	}{
		{"md5", 32, hashes.Pieces.MD5},
		{"sha1", 40, hashes.Pieces.SHA1},
		{"sha256", 64, hashes.Pieces.SHA256},
	}
	for _, set := range pieceSets {
		if len(set.values) != 0 && len(set.values) != expected {
			return fmt.Errorf("%s piece count is %d, expected %d", set.name, len(set.values), expected)
		}
		for _, value := range set.values {
			if !validHexHash(value, set.width) {
				return fmt.Errorf("invalid %s piece hash", set.name)
			}
		}
	}
	return nil
}

func validHexHash(value string, width int) bool {
	if len(value) != width {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}

// EncodeOSS serializes a validated seed as UTF-8 JSON.
func EncodeOSS(seed *Seed) ([]byte, error) {
	if err := ValidateSeed(seed, DefaultParseLimits()); err != nil {
		return nil, err
	}
	return json.MarshalIndent(seed, "", "  ")
}

// DecodeOSS parses a bounded UTF-8 OSS document.
func DecodeOSS(data []byte, limits ParseLimits) (*Seed, error) {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultMaxSeedSize
	}
	if int64(len(data)) > limits.MaxBytes {
		return nil, fmt.Errorf("seed exceeds %d bytes", limits.MaxBytes)
	}
	data = bytes.TrimPrefix(data, []byte{0xef, 0xbb, 0xbf})
	if !utf8.Valid(data) {
		return nil, fmt.Errorf("seed is not valid UTF-8")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var seed Seed
	if err := decoder.Decode(&seed); err != nil {
		return nil, fmt.Errorf("decode OSS: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("decode OSS: trailing data")
	}
	if err := ValidateSeed(&seed, limits); err != nil {
		return nil, err
	}
	return &seed, nil
}

// buildCASFileEntry computes the reference-compatible five fields for one file,
// preserving the per-piece MD5 list and piece size when available.
func buildCASFileEntry(file SeedFile, pieceSize int64) (CASFileEntry, error) {
	if file.Hashes.MD5 == "" {
		return CASFileEntry{}, fmt.Errorf("CAS requires a whole-file MD5 for %s", file.Path)
	}
	var sliceMD5s []string
	if file.Hashes.Pieces != nil && len(file.Hashes.Pieces.MD5) > 0 {
		sliceMD5s = upperStrings(file.Hashes.Pieces.MD5)
	}
	// Prefer an explicitly supplied legacy slice MD5; otherwise derive it with
	// the canonical rule so the value written here cannot drift from the one
	// the hash-generation side computes.
	sliceMD5 := strings.ToUpper(file.CASSliceMD5)
	if sliceMD5 == "" && len(sliceMD5s) > 0 && pieceSize == DefaultPieceSize {
		sliceMD5 = SliceMD5FromPieces(sliceMD5s, file.Hashes.MD5)
	}
	if sliceMD5 == "" {
		if file.Size > DefaultPieceSize {
			return CASFileEntry{}, fmt.Errorf("CAS requires a legacy slice MD5 or complete 10 MiB MD5 pieces for %s", file.Path)
		}
		sliceMD5 = strings.ToUpper(file.Hashes.MD5)
	}
	createTime := file.CASCreateTime
	if createTime == "" {
		createTime = fmt.Sprintf("%d", time.Now().Unix())
	}
	entry := CASFileEntry{
		Name: path.Base(file.Path), Size: file.Size, MD5: strings.ToUpper(file.Hashes.MD5),
		SliceMD5: sliceMD5, CreateTime: createTime, Cloud: file.CASCloud,
	}
	if len(sliceMD5s) > 0 {
		entry.SliceMD5s = sliceMD5s
		entry.SliceSize = pieceSize
		if entry.SliceSize <= 0 {
			entry.SliceSize = DefaultPieceSize
		}
	}
	return entry, nil
}

// EncodeCAS writes the reference-compatible base64 encoded JSON payload. A
// single-file seed uses the legacy five fields; multiple files are stored in
// the "files" array.
func EncodeCAS(seed *Seed) ([]byte, error) {
	if err := ValidateSeed(seed, DefaultParseLimits()); err != nil {
		return nil, err
	}
	if len(seed.Files) == 0 {
		return nil, fmt.Errorf("CAS requires at least one file")
	}
	var payload CASPayload
	if len(seed.Files) == 1 {
		entry, err := buildCASFileEntry(seed.Files[0], seed.PieceSize)
		if err != nil {
			return nil, err
		}
		payload = CASPayload{
			Name: entry.Name, Size: entry.Size, MD5: entry.MD5,
			SliceMD5: entry.SliceMD5, CreateTime: entry.CreateTime,
			SliceMD5s: entry.SliceMD5s, SliceSize: entry.SliceSize, Cloud: entry.Cloud,
		}
	} else {
		entries := make([]CASFileEntry, 0, len(seed.Files))
		var totalSize int64
		for _, file := range seed.Files {
			entry, err := buildCASFileEntry(file, seed.PieceSize)
			if err != nil {
				return nil, err
			}
			entries = append(entries, entry)
			totalSize += entry.Size
		}
		payload = CASPayload{Name: seed.Name, Size: totalSize, Files: entries}
	}
	content, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	encoded := make([]byte, base64.StdEncoding.EncodedLen(len(content)))
	base64.StdEncoding.Encode(encoded, content)
	return encoded, nil
}

// DecodeCAS accepts both reference base64 JSON and raw JSON payloads.
func DecodeCAS(data []byte, limits ParseLimits) (*Seed, error) {
	if limits.MaxBytes <= 0 {
		limits.MaxBytes = DefaultMaxSeedSize
	}
	if int64(len(data)) > limits.MaxBytes {
		return nil, fmt.Errorf("CAS seed exceeds %d bytes", limits.MaxBytes)
	}
	data = bytes.TrimSpace(data)
	decoded, err := base64.StdEncoding.DecodeString(string(data))
	if err == nil {
		data = decoded
	}
	var payload CASPayload
	if err = json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("decode CAS: %w", err)
	}
	seed := NewSeed(payload.Name, "OpenList CAS", DefaultPieceSize)
	if len(payload.Files) > 0 {
		// Multi-file extension.
		if len(payload.Files) > limits.MaxFiles {
			return nil, fmt.Errorf("CAS seed exceeds %d files", limits.MaxFiles)
		}
		for _, entry := range payload.Files {
			file, err := casEntryToSeedFile(entry.Name, entry.Size, entry.MD5, entry.SliceMD5, entry.CreateTime, entry.SliceMD5s, entry.Cloud)
			if err != nil {
				return nil, err
			}
			if entry.SliceSize > 0 {
				seed.PieceSize = entry.SliceSize
			}
			seed.Files = append(seed.Files, file)
		}
		return seed, ValidateSeed(seed, limits)
	}
	file, err := casEntryToSeedFile(payload.Name, payload.Size, payload.MD5, payload.SliceMD5, payload.CreateTime, payload.SliceMD5s, payload.Cloud)
	if err != nil {
		return nil, err
	}
	if payload.SliceSize > 0 {
		seed.PieceSize = payload.SliceSize
	}
	seed.Files = []SeedFile{file}
	return seed, ValidateSeed(seed, limits)
}

// casEntryToSeedFile converts a CAS payload entry into a SeedFile, restoring the
// per-piece MD5 list when it is present.
func casEntryToSeedFile(name string, size int64, md5Hex, sliceMD5Hex, createTime string, sliceMD5s []string, cloud string) (SeedFile, error) {
	if name == "" || size < 0 || !validHexHash(md5Hex, 32) {
		return SeedFile{}, fmt.Errorf("invalid CAS payload")
	}
	sliceMD5 := sliceMD5Hex
	if sliceMD5 == "" {
		sliceMD5 = md5Hex
	}
	if !validHexHash(sliceMD5, 32) {
		return SeedFile{}, fmt.Errorf("invalid CAS sliceMd5")
	}
	file := SeedFile{
		Path: name, Size: size, CASCreateTime: createTime,
		CASSliceMD5: strings.ToLower(sliceMD5),
		CASCloud:    cloud,
		Hashes:      SeedHashes{MD5: strings.ToLower(md5Hex)},
	}
	if len(sliceMD5s) > 0 {
		file.Hashes.Pieces = &SeedPieceHashes{MD5: lowerStrings(sliceMD5s)}
	}
	return file, nil
}

// DetectFormat determines the seed container from a file name and content.
func DetectFormat(fileName string, data []byte) string {
	switch strings.ToLower(path.Ext(fileName)) {
	case ".oss":
		return "oss"
	case ".torrent":
		return "torrent"
	case ".cas":
		return "cas"
	}
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) > 0 && (trimmed[0] == '{' || bytes.HasPrefix(trimmed, []byte{0xef, 0xbb, 0xbf, '{'})) {
		return "oss"
	}
	if len(trimmed) > 0 && trimmed[0] == 'd' {
		return "torrent"
	}
	return "cas"
}

// DecodeSeed normalizes OSS, torrent and CAS containers to the OSS contract.
func DecodeSeed(data []byte, format string, limits ParseLimits) (*Seed, error) {
	if limits.MaxBytes <= 0 {
		limits = DefaultParseLimits()
	}
	if int64(len(data)) > limits.MaxBytes {
		return nil, fmt.Errorf("seed exceeds %d bytes", limits.MaxBytes)
	}
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "oss":
		return DecodeOSS(data, limits)
	case "torrent":
		t, err := Decode(data)
		if err != nil {
			return nil, err
		}
		return SeedFromTorrent(t, limits)
	case "cas":
		return DecodeCAS(data, limits)
	default:
		return nil, fmt.Errorf("unsupported seed format %q", format)
	}
}

// EncodeSeed converts a normalized seed to the requested container.
func EncodeSeed(seed *Seed, format string) ([]byte, error) {
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "oss":
		return EncodeOSS(seed)
	case "torrent":
		t, diagnostics := TorrentFromSeed(seed)
		if len(diagnostics) > 0 {
			return nil, fmt.Errorf("cannot convert to torrent: %s", strings.Join(diagnostics, "; "))
		}
		return t.Encode()
	case "cas":
		return EncodeCAS(seed)
	default:
		return nil, fmt.Errorf("unsupported seed format %q", format)
	}
}

// DiagnoseConversion reports information missing for a lossless target conversion.
func DiagnoseConversion(seed *Seed, format string) []string {
	if err := ValidateSeed(seed, DefaultParseLimits()); err != nil {
		return []string{err.Error()}
	}
	var diagnostics []string
	switch strings.ToLower(strings.TrimPrefix(format, ".")) {
	case "torrent":
		for i, file := range seed.Files {
			if file.Hashes.Pieces == nil || len(file.Hashes.Pieces.SHA1) == 0 {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: missing SHA-1 piece hashes", file.Path))
			}
			if len(seed.Files) > 1 && i < len(seed.Files)-1 && file.Size%seed.PieceSize != 0 {
				diagnostics = append(diagnostics, fmt.Sprintf("%s: piece boundary crosses the next file", file.Path))
			}
		}
	case "cas":
		for _, file := range seed.Files {
			if file.Hashes.MD5 == "" {
				diagnostics = append(diagnostics, file.Path+": missing whole-file MD5")
			}
			if file.Size > DefaultPieceSize && file.CASSliceMD5 == "" &&
				(seed.PieceSize != DefaultPieceSize || file.Hashes.Pieces == nil || len(file.Hashes.Pieces.MD5) == 0) {
				diagnostics = append(diagnostics, file.Path+": missing legacy slice MD5 or complete 10 MiB MD5 pieces")
			}
		}
	case "oss":
	default:
		diagnostics = append(diagnostics, fmt.Sprintf("unsupported target format %q", format))
	}
	return diagnostics
}

// TorrentFromSeed constructs standard BitTorrent info/pieces plus x-openlist.
func TorrentFromSeed(seed *Seed) (*Torrent, []string) {
	diagnostics := DiagnoseConversion(seed, "torrent")
	if len(diagnostics) > 0 {
		return nil, diagnostics
	}
	t := &Torrent{
		Info:         TorrentInfo{PieceLength: seed.PieceSize, Name: seed.Name},
		Comment:      seed.Comment,
		CreatedBy:    seed.CreatedBy,
		CreationDate: time.Now().Unix(),
		OpenList:     seed,
	}
	if parsed, err := time.Parse(time.RFC3339, seed.CreatedAt); err == nil {
		t.CreationDate = parsed.Unix()
	}
	if len(seed.Trackers) > 0 {
		t.Announce = seed.Trackers[0]
		for _, tracker := range seed.Trackers {
			t.AnnounceList = append(t.AnnounceList, []string{tracker})
		}
	}
	for _, file := range seed.Files {
		for _, piece := range file.Hashes.Pieces.SHA1 {
			raw, _ := hex.DecodeString(piece)
			t.Info.Pieces = append(t.Info.Pieces, raw...)
		}
		if len(seed.Files) == 1 {
			t.Info.Length = file.Size
			t.Info.MD5Sum = file.Hashes.MD5
		} else {
			t.Info.Files = append(t.Info.Files, TorrentFile{
				Length: file.Size,
				Path:   strings.Split(file.Path, "/"),
				MD5Sum: file.Hashes.MD5,
			})
		}
	}
	if len(seed.Files) == 1 {
		file := seed.Files[0]
		if file.Hashes.MD5 != "" && file.CASSliceMD5 != "" {
			cloud := file.CASCloud
			if cloud == "" {
				cloud = Cloud189
			}
			t.CAS = &CASInfo{
				FileMD5: strings.ToUpper(file.Hashes.MD5), SliceMD5: strings.ToUpper(file.CASSliceMD5),
				SliceSize: DefaultPieceSize, Cloud: cloud,
			}
		} else if file.Hashes.MD5 != "" && seed.PieceSize == DefaultPieceSize && file.Hashes.Pieces != nil && len(file.Hashes.Pieces.MD5) > 0 {
			cloud := file.CASCloud
			if cloud == "" {
				cloud = Cloud189
			}
			t.CAS = BuildCASInfoFromMD5sWithCloud(file.Hashes.MD5, upperStrings(file.Hashes.Pieces.MD5), DefaultPieceSize, cloud)
		}
	}
	return t, nil
}

func validateOpenListTorrentConsistency(t *Torrent, seed *Seed) error {
	if seed.Name != t.Info.Name || seed.PieceSize != t.Info.PieceLength {
		return fmt.Errorf("x-openlist metadata conflicts with torrent info")
	}
	if len(t.Info.Files) == 0 {
		if len(seed.Files) != 1 || seed.Files[0].Size != t.Info.Length {
			return fmt.Errorf("x-openlist file list conflicts with torrent info")
		}
	} else {
		if len(seed.Files) != len(t.Info.Files) {
			return fmt.Errorf("x-openlist file count conflicts with torrent info")
		}
		for i, file := range t.Info.Files {
			if seed.Files[i].Path != strings.Join(file.Path, "/") || seed.Files[i].Size != file.Length {
				return fmt.Errorf("x-openlist file %d conflicts with torrent info", i)
			}
		}
	}
	standardPieces := t.GetPieceHashes()
	extensionPieces := make([]string, 0, len(standardPieces))
	pieceBoundariesAligned := true
	for i, file := range seed.Files {
		if i < len(seed.Files)-1 && file.Size%seed.PieceSize != 0 {
			pieceBoundariesAligned = false
		}
		if file.Hashes.Pieces != nil {
			extensionPieces = append(extensionPieces, file.Hashes.Pieces.SHA1...)
		}
	}
	if pieceBoundariesAligned && len(extensionPieces) > 0 {
		if len(extensionPieces) != len(standardPieces) {
			return fmt.Errorf("x-openlist SHA-1 pieces conflict with torrent info")
		}
		for i, piece := range standardPieces {
			if !strings.EqualFold(extensionPieces[i], hex.EncodeToString(piece)) {
				return fmt.Errorf("x-openlist SHA-1 piece %d conflicts with torrent info", i)
			}
		}
	}
	return nil
}

// SeedFromTorrent normalizes a standard torrent and preserves x-openlist when present.
func SeedFromTorrent(t *Torrent, limits ParseLimits) (*Seed, error) {
	if t == nil {
		return nil, fmt.Errorf("missing torrent")
	}
	if t.OpenList != nil {
		if err := ValidateSeed(t.OpenList, limits); err != nil {
			return nil, err
		}
		if err := validateOpenListTorrentConsistency(t, t.OpenList); err != nil {
			return nil, err
		}
		return t.OpenList, nil
	}
	seed := NewSeed(t.Info.Name, t.CreatedBy, t.Info.PieceLength)
	if seed.CreatedBy == "" {
		seed.CreatedBy = "BitTorrent"
	}
	if t.CreationDate > 0 {
		seed.CreatedAt = time.Unix(t.CreationDate, 0).UTC().Format(time.RFC3339)
	}
	seed.Comment = t.Comment
	seed.Trackers = append(seed.Trackers, t.Announce)
	for _, tier := range t.AnnounceList {
		seed.Trackers = append(seed.Trackers, tier...)
	}
	seed.Trackers = uniqueNonEmpty(seed.Trackers)
	pieceHex := make([]string, 0, len(t.Info.Pieces)/sha1.Size)
	for _, piece := range t.GetPieceHashes() {
		pieceHex = append(pieceHex, hex.EncodeToString(piece))
	}
	if len(t.Info.Files) == 0 {
		hashes := SeedHashes{MD5: t.Info.MD5Sum}
		if len(pieceHex) > 0 {
			hashes.Pieces = &SeedPieceHashes{SHA1: pieceHex}
		}
		if t.CAS != nil {
			hashes.MD5 = t.CAS.FileMD5
			if hashes.Pieces == nil {
				hashes.Pieces = &SeedPieceHashes{}
			}
			hashes.Pieces.MD5 = append([]string(nil), t.CAS.SliceMD5s...)
		}
		seed.Files = []SeedFile{{Path: t.Info.Name, Size: t.Info.Length, Hashes: hashes}}
	} else {
		pieceOffset := 0
		for i, file := range t.Info.Files {
			hashes := SeedHashes{MD5: file.MD5Sum}
			pieceCount := 0
			if file.Length > 0 && t.Info.PieceLength > 0 {
				pieceCount = int((file.Length + t.Info.PieceLength - 1) / t.Info.PieceLength)
			}
			aligned := i == len(t.Info.Files)-1 || file.Length%t.Info.PieceLength == 0
			if aligned && pieceOffset+pieceCount <= len(pieceHex) {
				hashes.Pieces = &SeedPieceHashes{SHA1: append([]string(nil), pieceHex[pieceOffset:pieceOffset+pieceCount]...)}
			}
			pieceOffset += pieceCount
			seed.Files = append(seed.Files, SeedFile{Path: strings.Join(file.Path, "/"), Size: file.Length, Hashes: hashes})
		}
	}
	if err := ValidateSeed(seed, limits); err != nil {
		return nil, err
	}
	return seed, nil
}

func seedToBencode(seed *Seed) map[string]interface{} {
	root := map[string]interface{}{
		"format": seed.Format, "version": int64(seed.Version), "name": seed.Name,
		"created_at": seed.CreatedAt, "created_by": seed.CreatedBy, "piece_size": seed.PieceSize,
	}
	if seed.Comment != "" {
		root["comment"] = seed.Comment
	}
	if len(seed.Trackers) > 0 {
		root["trackers"] = stringsToInterfaces(seed.Trackers)
	}
	channels := make([]interface{}, 0, len(seed.Channels))
	for _, channel := range seed.Channels {
		item := map[string]interface{}{"driver": channel.Driver}
		if channel.MountPath != "" {
			item["mount_path"] = channel.MountPath
		}
		channels = append(channels, item)
	}
	if len(channels) > 0 {
		root["channels"] = channels
	}
	files := make([]interface{}, 0, len(seed.Files))
	for _, file := range seed.Files {
		item := map[string]interface{}{"path": file.Path, "size": file.Size, "hashes": hashesToBencode(file.Hashes)}
		if file.Modified != "" {
			item["modified"] = file.Modified
		}
		if file.Comment != "" {
			item["comment"] = file.Comment
		}
		if file.CASSliceMD5 != "" {
			item["cas_slice_md5"] = file.CASSliceMD5
		}
		if file.CASCreateTime != "" {
			item["cas_create_time"] = file.CASCreateTime
		}
		if file.CASCloud != "" {
			item["cas_cloud"] = file.CASCloud
		}
		if len(file.MissingChannels) > 0 {
			item["missing_channels"] = stringsToInterfaces(file.MissingChannels)
		}
		sources := make([]interface{}, 0, len(file.Sources))
		for _, source := range file.Sources {
			s := map[string]interface{}{"type": source.Type, "url": source.URL}
			if source.ExpiresAt != "" {
				s["expires_at"] = source.ExpiresAt
			}
			if source.ShareID != "" {
				s["share_id"] = source.ShareID
			}
			sources = append(sources, s)
		}
		if len(sources) > 0 {
			item["sources"] = sources
		}
		files = append(files, item)
	}
	root["files"] = files
	return root
}

func hashesToBencode(hashes SeedHashes) map[string]interface{} {
	result := make(map[string]interface{})
	if hashes.MD5 != "" {
		result["md5"] = hashes.MD5
	}
	if hashes.SHA1 != "" {
		result["sha1"] = hashes.SHA1
	}
	if hashes.SHA256 != "" {
		result["sha256"] = hashes.SHA256
	}
	if hashes.Pieces != nil {
		pieces := make(map[string]interface{})
		if len(hashes.Pieces.MD5) > 0 {
			pieces["md5"] = stringsToInterfaces(hashes.Pieces.MD5)
		}
		if len(hashes.Pieces.SHA1) > 0 {
			pieces["sha1"] = stringsToInterfaces(hashes.Pieces.SHA1)
		}
		if len(hashes.Pieces.SHA256) > 0 {
			pieces["sha256"] = stringsToInterfaces(hashes.Pieces.SHA256)
		}
		if len(pieces) > 0 {
			result["pieces"] = pieces
		}
	}
	return result
}

func seedFromBencode(value interface{}) (*Seed, error) {
	root, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("extension is not a dictionary")
	}
	seed := &Seed{
		Format: bString(root["format"]), Version: int(bInt(root["version"])), Name: bString(root["name"]),
		Comment: bString(root["comment"]), CreatedAt: bString(root["created_at"]),
		CreatedBy: bString(root["created_by"]), PieceSize: bInt(root["piece_size"]),
		Trackers: bStrings(root["trackers"]),
	}
	for _, value := range bList(root["channels"]) {
		item, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("channel is not a dictionary")
		}
		seed.Channels = append(seed.Channels, SeedChannel{Driver: bString(item["driver"]), MountPath: bString(item["mount_path"])})
	}
	for _, value := range bList(root["files"]) {
		item, ok := value.(map[string]interface{})
		if !ok {
			return nil, fmt.Errorf("file is not a dictionary")
		}
		file := SeedFile{
			Path: bString(item["path"]), Size: bInt(item["size"]), Modified: bString(item["modified"]), Comment: bString(item["comment"]),
			CASSliceMD5: bString(item["cas_slice_md5"]), CASCreateTime: bString(item["cas_create_time"]),
			CASCloud:        bString(item["cas_cloud"]),
			MissingChannels: bStrings(item["missing_channels"]),
		}
		if hashes, ok := item["hashes"].(map[string]interface{}); ok {
			file.Hashes = SeedHashes{MD5: bString(hashes["md5"]), SHA1: bString(hashes["sha1"]), SHA256: bString(hashes["sha256"])}
			if pieces, ok := hashes["pieces"].(map[string]interface{}); ok {
				file.Hashes.Pieces = &SeedPieceHashes{MD5: bStrings(pieces["md5"]), SHA1: bStrings(pieces["sha1"]), SHA256: bStrings(pieces["sha256"])}
			}
		}
		for _, sourceValue := range bList(item["sources"]) {
			source, ok := sourceValue.(map[string]interface{})
			if !ok {
				return nil, fmt.Errorf("source is not a dictionary")
			}
			file.Sources = append(file.Sources, SeedSource{Type: bString(source["type"]), URL: bString(source["url"]), ExpiresAt: bString(source["expires_at"]), ShareID: bString(source["share_id"])})
		}
		seed.Files = append(seed.Files, file)
	}
	return seed, nil
}

func stringsToInterfaces(values []string) []interface{} {
	result := make([]interface{}, len(values))
	for i := range values {
		result[i] = values[i]
	}
	return result
}

func bString(value interface{}) string {
	switch value := value.(type) {
	case []byte:
		return string(value)
	case string:
		return value
	default:
		return ""
	}
}

func bInt(value interface{}) int64 {
	valueInt, _ := value.(int64)
	return valueInt
}

func bList(value interface{}) []interface{} {
	list, _ := value.([]interface{})
	return list
}

func bStrings(value interface{}) []string {
	list := bList(value)
	result := make([]string, 0, len(list))
	for _, item := range list {
		if text := bString(item); text != "" {
			result = append(result, text)
		}
	}
	return result
}

func upperStrings(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ToUpper(value)
	}
	return result
}

func lowerStrings(values []string) []string {
	result := make([]string, len(values))
	for i, value := range values {
		result[i] = strings.ToLower(value)
	}
	return result
}

func uniqueNonEmpty(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}
