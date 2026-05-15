package _189pc

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"

	"github.com/go-resty/resty/v2"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// GenerateTorrent 根据上传过程中收集的哈希信息生成包含 CAS 扩展的 torrent 文件
// fileMD5: 整文件 MD5（大写十六进制）
// sliceMD5s: 每个分片的 MD5 列表（大写十六进制）
// sliceSize: 分片大小
// pieceHashes: SHA-1 piece hashes 拼接（每 20 字节一个）
// fileName: 文件名
// fileSize: 文件大小
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
// 返回值：上传成功的文件对象、错误
func (y *Cloud189PC) RapidUploadFromTorrent(ctx context.Context, dstDir model.Obj, torrentData []byte, overwrite bool) (model.Obj, error) {
	isFamily := y.isFamily()

	// 解析 torrent
	t, err := torrent.Decode(torrentData)
	if err != nil {
		return nil, fmt.Errorf("解析 torrent 失败: %w", err)
	}

	// 检查是否包含 CAS 扩展信息
	if !t.HasCASInfo() {
		return nil, fmt.Errorf("torrent 不包含 CAS 扩展信息，无法秒传")
	}

	cas := t.CAS
	fileName := t.Info.Name
	fileSize := t.GetTotalSize()

	// 统一 MD5 为大写（与正常上传保持一致，天翼云盘要求大写）
	fileMD5Upper := strings.ToUpper(cas.FileMD5)

	// 优先使用 torrent 中嵌入的分片大小，与生成时保持一致
	sliceSize := cas.SliceSize
	if sliceSize <= 0 {
		sliceSize = partSize(fileSize)
	}

	// 计算 sliceMd5（与上传时一致的算法）
	// 优先使用 torrent 中已有的 SliceMD5；仅当有多分片列表时才重新计算
	sliceMd5Hex := strings.ToUpper(cas.SliceMD5)
	if sliceMd5Hex == "" {
		sliceMd5Hex = fileMD5Upper
	}
	if len(cas.SliceMD5s) > 1 {
		// 分片 MD5 也需要统一大写后再拼接计算
		upperSliceMD5s := make([]string, len(cas.SliceMD5s))
		for i, s := range cas.SliceMD5s {
			upperSliceMD5s[i] = strings.ToUpper(s)
		}
		sliceMd5Hex = strings.ToUpper(utils.GetMD5EncodeStr(strings.Join(upperSliceMD5s, "\n")))
	}


	// 使用与 Web 端一致的三步秒传流程
	fullUrl := "https://upload.cloud.189.cn"
	if isFamily {
		fullUrl += "/family"
	} else {
		fullUrl += "/person"
	}

	// Step 1: initMultiUpload（不传 fileMd5/sliceMd5，只传 lazyCheck）
	initParams := Params{
		"parentFolderId": dstDir.GetID(),
		"fileName":       url.QueryEscape(fileName),
		"fileSize":       fmt.Sprint(fileSize),
		"sliceSize":      fmt.Sprint(sliceSize),
		"lazyCheck":      "1",
	}
	if isFamily {
		initParams.Set("familyId", y.FamilyID)
	}


	var uploadInfo InitMultiUploadResp
	_, err = y.request(fullUrl+"/initMultiUpload", "GET", func(req *resty.Request) {
		req.SetContext(ctx)
	}, initParams, &uploadInfo, isFamily)
	if err != nil {
		return nil, fmt.Errorf("initMultiUpload 失败: %w", err)
	}


	uploadFileId := uploadInfo.Data.UploadFileID

	// Step 2: checkTransSecond（用 fileMd5 + sliceMd5 + uploadFileId 检查秒传）
	checkParams := Params{
		"fileMd5":      fileMD5Upper,
		"sliceMd5":     sliceMd5Hex,
		"uploadFileId": uploadFileId,
	}


	var checkResp struct {
		Data struct {
			FileDataExists int `json:"fileDataExists"`
		} `json:"data"`
	}
	_, err = y.request(fullUrl+"/checkTransSecond", "GET", func(req *resty.Request) {
		req.SetContext(ctx)
	}, checkParams, &checkResp, isFamily)
	if err != nil {
		utils.Log.Errorf("[RapidUpload] checkTransSecond 失败: uploadFileId=%s, err=%v", uploadFileId, err)
		return nil, fmt.Errorf("秒传检查失败: %w", err)
	}


	if checkResp.Data.FileDataExists != 1 {
		return nil, fmt.Errorf("秒传失败：云端不存在该文件（fileMD5=%s, sliceMD5=%s, size=%d）", fileMD5Upper, sliceMd5Hex, fileSize)
	}

	// Step 3: commitMultiUploadFile（传 fileMd5 + sliceMd5）

	var resp CommitMultiUploadFileResp
	commitParams := Params{
		"uploadFileId": uploadFileId,
		"fileMd5":      fileMD5Upper,
		"sliceMd5":     sliceMd5Hex,
		"lazyCheck":    "1",
		"opertype":     IF(overwrite, "3", "1"),
	}

	_, err = y.request(fullUrl+"/commitMultiUploadFile", "GET", func(req *resty.Request) {
		req.SetContext(ctx)
	}, commitParams, &resp, isFamily)
	if err != nil {
		utils.Log.Errorf("[RapidUpload] commitMultiUploadFile 失败: uploadFileId=%s, err=%v", uploadFileId, err)
		return nil, fmt.Errorf("提交上传失败: %w", err)
	}

	return resp.toFile(), nil
}

// ComputeTorrentFromReader 从 io.Reader 计算并生成 torrent 文件
// 适用于：已有文件需要生成 torrent 的场景（如下载完成后生成）
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
// 返回：CAS 信息、文件名、文件大小、错误
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

// InjectCASIntoTorrent 向已有的 torrent 文件注入 CAS 扩展信息
// 用于：下载完成后，计算了 MD5 信息，写回到 torrent 中
func InjectCASIntoTorrent(torrentData []byte, fileMD5 string, sliceMD5s []string, sliceSize int64) ([]byte, error) {
	t, err := torrent.Decode(torrentData)
	if err != nil {
		return nil, fmt.Errorf("解析 torrent 失败: %w", err)
	}

	// 计算 sliceMD5
	sliceMD5 := fileMD5
	if len(sliceMD5s) > 1 {
		joined := strings.Join(sliceMD5s, "\n")
		sliceMD5 = strings.ToUpper(torrent.GetMD5Str(joined))
	}

	// 注入 CAS 信息
	t.SetCASInfo(&torrent.CASInfo{
		FileMD5:   fileMD5,
		SliceMD5:  sliceMD5,
		SliceMD5s: sliceMD5s,
		SliceSize: sliceSize,
		Cloud:     "189",
	})

	// 同时更新 info 中的 md5sum 字段
	if t.Info.MD5Sum == "" {
		t.Info.MD5Sum = fileMD5
	}

	return t.Encode()
}

// GetInfoHashHex 获取 torrent 的 info_hash（十六进制字符串）
func GetInfoHashHex(torrentData []byte) (string, error) {
	t, err := torrent.Decode(torrentData)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(t.InfoHash), nil
}

// ComputeSliceMD5sFromReader 从 reader 中计算每个 10MB 分片的 MD5
// 返回：整文件 MD5、分片 MD5 列表
func ComputeSliceMD5sFromReader(reader io.Reader, sliceSize int64) (string, []string, error) {
	if sliceSize <= 0 {
		sliceSize = torrent.DefaultPieceSize
	}

	fileMD5Hash := utils.MD5.NewFunc()
	sliceMD5s := make([]string, 0)

	buf := make([]byte, sliceSize)
	for {
		n, err := io.ReadFull(reader, buf)
		if n > 0 {
			chunk := buf[:n]
			fileMD5Hash.Write(chunk)
			// 计算该分片的 MD5
			sliceMD5 := strings.ToUpper(utils.HashData(utils.MD5, chunk))
			sliceMD5s = append(sliceMD5s, sliceMD5)
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return "", nil, err
		}
	}

	fileMD5Hex := strings.ToUpper(hex.EncodeToString(fileMD5Hash.Sum(nil)))
	return fileMD5Hex, sliceMD5s, nil
}
