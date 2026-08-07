package _189

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// GenerateTorrent 根据上传过程中收集的哈希信息生成包含 CAS 扩展的 torrent 文件
func GenerateTorrent(fileName string, fileSize int64, fileMD5 string, sliceMD5s []string, sliceSize int64, pieceHashes []byte) ([]byte, error) {
	// 计算 sliceMD5
	sliceMD5 := fileMD5
	if len(sliceMD5s) > 1 {
		joined := strings.Join(sliceMD5s, "\n")
		sliceMD5 = strings.ToUpper(torrent.GetMD5Str(joined))
	}

	t := torrent.NewTorrent(fileName, fileSize, fileMD5)
	t.Info.PieceLength = sliceSize
	t.SetPieces(pieceHashes)
	t.SetCASInfo(&torrent.CASInfo{
		FileMD5:   fileMD5,
		SliceMD5:  sliceMD5,
		SliceMD5s: sliceMD5s,
		SliceSize: sliceSize,
		Cloud:     "189",
	})

	return t.Encode()
}

// RapidUploadFromTorrent 从 torrent 文件中提取 CAS 信息进行秒传
func (d *Cloud189) RapidUploadFromTorrent(ctx context.Context, dstDir model.Obj, torrentData []byte) error {
	// 解析 torrent
	t, err := torrent.Decode(torrentData)
	if err != nil {
		return fmt.Errorf("解析 torrent 失败: %w", err)
	}

	// 检查是否包含 CAS 扩展信息
	if !t.HasCASInfo() {
		return fmt.Errorf("torrent 不包含 CAS 扩展信息，无法秒传")
	}

	cas := t.CAS
	fileName := t.Info.Name
	fileSize := t.GetTotalSize()

	// 获取 sessionKey
	sessionKey, err := d.getSessionKey()
	if err != nil {
		return err
	}
	d.sessionKey = sessionKey

	// 初始化上传
	res, err := d.uploadRequest("/person/initMultiUpload", map[string]string{
		"parentFolderId": dstDir.GetID(),
		"fileName":       encode(fileName),
		"fileSize":       fmt.Sprint(fileSize),
		"sliceSize":      fmt.Sprint(cas.SliceSize),
		"lazyCheck":      "1",
	}, nil)
	if err != nil {
		return fmt.Errorf("初始化上传失败: %w", err)
	}

	uploadFileId := utils.Json.Get(res, "data", "uploadFileId").ToString()

	// 提交上传（使用 CAS 信息秒传）
	_, err = d.uploadRequest("/person/commitMultiUploadFile", map[string]string{
		"uploadFileId": uploadFileId,
		"fileMd5":      cas.FileMD5,
		"sliceMd5":     cas.SliceMD5,
		"lazyCheck":    "1",
		"opertype":     "3",
	}, nil)
	if err != nil {
		return fmt.Errorf("秒传提交失败: %w", err)
	}

	return nil
}

// ComputeTorrentFromReader 从 io.Reader 计算并生成 torrent 文件
func ComputeTorrentFromReader(reader io.Reader, fileName string, fileSize int64, sliceSize int64) ([]byte, error) {
	if sliceSize <= 0 {
		sliceSize = torrent.DefaultPieceSize
	}

	hw := torrent.NewHashWriter(sliceSize, sliceSize)

	buf := make([]byte, 32*1024)
	for {
		n, err := reader.Read(buf)
		if n > 0 {
			hw.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	hw.Finish()

	fileMD5 := hw.GetFileMD5()
	sliceMD5s := hw.GetSliceMD5s()
	pieceHashes := hw.GetPieceHashes()

	return GenerateTorrent(fileName, fileSize, fileMD5, sliceMD5s, sliceSize, pieceHashes)
}

// ComputePieceSHA1 计算单个分片的 SHA-1 哈希
func ComputePieceSHA1(data []byte) []byte {
	h := sha1.Sum(data)
	return h[:]
}

// ExtractCASFromTorrent 从 torrent 数据中提取 CAS 信息
func ExtractCASFromTorrent(torrentData []byte) (*torrent.CASInfo, string, int64, error) {
	t, err := torrent.Decode(torrentData)
	if err != nil {
		return nil, "", 0, fmt.Errorf("解析 torrent 失败: %w", err)
	}

	if !t.HasCASInfo() {
		return nil, "", 0, fmt.Errorf("torrent 不包含 CAS 扩展信息")
	}

	return t.CAS, t.Info.Name, t.GetTotalSize(), nil
}

// GetInfoHashHex 获取 torrent 的 info_hash（十六进制字符串）
func GetInfoHashHex(torrentData []byte) (string, error) {
	t, err := torrent.Decode(torrentData)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(t.InfoHash), nil
}
