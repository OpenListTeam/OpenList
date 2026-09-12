package _189pc

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/go-resty/resty/v2"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
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
		Cloud:     torrent.Cloud189,
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

	// 优先使用 torrent 中嵌入的分片 MD5 与大写整文件 MD5
	fileMD5 := strings.ToUpper(cas.FileMD5)
	sliceMD5s := make([]string, len(cas.SliceMD5s))
	for i, s := range cas.SliceMD5s {
		sliceMD5s[i] = strings.ToUpper(s)
	}

	// 复用统一的 CAS 秒传核心实现
	respObj, err := y.rapidUploadByCAS(ctx, dstDir, fileName, fileSize, fileMD5, sliceMD5s, cas.SliceSize, overwrite)
	if err != nil {
		utils.Log.Errorf("[RapidUpload] 秒传失败: fileMD5=%s, err=%v", fileMD5, err)
		return nil, err
	}

	// 秒传成功后，将 torrent 文件上传到目标目录（异步，不影响秒传结果）
	if y.Addition.GenerateTorrent {
		capturedDstDir := dstDir
		capturedIsFamily := isFamily
		go func() {
			torrentName := fileName + ".cas.torrent"
			infoHash, _ := GetInfoHashHex(torrentData)
			utils.Log.Infof("秒传成功，上传 torrent: %s (info_hash: %s, size: %d bytes)",
				torrentName, infoHash, len(torrentData))

			torrentFileStream := &stream.FileStream{
				Ctx: context.Background(),
				Obj: &model.Object{
					Name:     torrentName,
					Size:     int64(len(torrentData)),
					IsFolder: false,
				},
				Reader:   bytes.NewReader(torrentData),
				Mimetype: "application/x-bittorrent",
			}
			_, uploadErr := y.fastUpload(context.Background(), capturedDstDir, torrentFileStream, func(p float64) {}, capturedIsFamily, false, false)
			if uploadErr != nil {
				utils.Log.Warnf("上传 torrent 文件失败: %v", uploadErr)
			} else {
				utils.Log.Infof("torrent 文件已上传: %s", torrentName)
				op.Cache.DeleteDirectory(y, capturedDstDir.GetPath())
			}
		}()
	}

	return respObj, nil
}

// ComputeTorrentFromReader 从 io.Reader 计算并生成 torrent 文件
// 适用于：已有文件需要生成 torrent 的场景（如下载完成后生成）
func ComputeTorrentFromReader(reader io.Reader, fileName string, fileSize int64, sliceSize int64) ([]byte, error) {
	if sliceSize <= 0 {
		sliceSize = torrent.DefaultPieceSize
	}

	hw := torrent.NewHashWriter(sliceSize, sliceSize, fileSize)

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
		Cloud:     torrent.Cloud189,
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

func isCASTorrentFile(fileName string) bool {
	return strings.HasSuffix(fileName, ".cas.torrent")
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

// torrentFollowCopy 跟随复制 torrent 文件（异步，不影响主操作）
// srcFolderId: 源文件所在目录 ID
// srcFileName: 源文件名
// dstDir: 目标目录
func (y *Cloud189PC) torrentFollowCopy(srcFolderId string, srcFileName string, dstDir model.Obj) {
	if !y.Addition.GenerateTorrent {
		return
	}
	if srcFolderId == "" {
		return
	}
	torrentName := srcFileName + ".cas.torrent"
	isFamily := y.isFamily()

	go func() {
		torrentFile, err := y.findFileByName(context.Background(), torrentName, srcFolderId, isFamily)
		if err != nil {
			utils.Log.Debugf("查找 torrent 文件失败（跟随复制）: %v", err)
			return
		}
		// 复制 torrent 文件到目标目录
		resp, copyErr := y.CreateBatchTask("COPY", IF(isFamily, y.FamilyID, ""), dstDir.GetID(),
			map[string]string{"targetFileName": dstDir.GetName()},
			BatchTaskInfo{
				FileId:   torrentFile.GetID(),
				FileName: torrentFile.GetName(),
				IsFolder: 0,
			})
		if copyErr != nil {
			utils.Log.Warnf("跟随复制 torrent 文件失败: %v", copyErr)
			return
		}
		if err = y.WaitBatchTask("COPY", resp.TaskID, time.Second); err != nil {
			utils.Log.Warnf("等待跟随复制 torrent 文件失败: %v", err)
		}
	}()
}

// torrentFollowMove 跟随移动 torrent 文件（异步，不影响主操作）
// srcFolderId: 源文件所在目录 ID
// srcFileName: 源文件名
// dstDir: 目标目录
func (y *Cloud189PC) torrentFollowMove(srcFolderId string, srcFileName string, dstDir model.Obj) {
	if !y.Addition.GenerateTorrent {
		return
	}
	if srcFolderId == "" {
		return
	}
	torrentName := srcFileName + ".cas.torrent"
	isFamily := y.isFamily()

	go func() {
		torrentFile, err := y.findFileByName(context.Background(), torrentName, srcFolderId, isFamily)
		if err != nil {
			utils.Log.Debugf("查找 torrent 文件失败（跟随移动）: %v", err)
			return
		}
		// 移动 torrent 文件到目标目录
		resp, moveErr := y.CreateBatchTask("MOVE", IF(isFamily, y.FamilyID, ""), dstDir.GetID(),
			map[string]string{"targetFileName": dstDir.GetName()},
			BatchTaskInfo{
				FileId:   torrentFile.GetID(),
				FileName: torrentFile.GetName(),
				IsFolder: 0,
			})
		if moveErr != nil {
			utils.Log.Warnf("跟随移动 torrent 文件失败: %v", moveErr)
			return
		}
		if err = y.WaitBatchTask("MOVE", resp.TaskID, time.Millisecond*400); err != nil {
			utils.Log.Warnf("等待跟随移动 torrent 文件失败: %v", err)
		}
	}()
}

// torrentFollowRename 跟随重命名 torrent 文件（异步，不影响主操作）
// folderId: 文件所在目录 ID
// oldFileName: 原文件名
// newFileName: 新文件名
func (y *Cloud189PC) torrentFollowRename(folderId string, oldFileName string, newFileName string) {
	if !y.Addition.GenerateTorrent {
		return
	}
	if folderId == "" {
		return
	}
	oldTorrentName := oldFileName + ".cas.torrent"
	newTorrentName := newFileName + ".cas.torrent"
	isFamily := y.isFamily()

	go func() {
		torrentFile, err := y.findFileByName(context.Background(), oldTorrentName, folderId, isFamily)
		if err != nil {
			utils.Log.Debugf("查找 torrent 文件失败（跟随重命名）: %v", err)
			return
		}

		// 重命名 torrent 文件
		queryParam := make(map[string]string)
		fullUrl := API_URL
		method := "POST"
		if isFamily {
			fullUrl += "/family/file"
			method = "GET"
			queryParam["familyId"] = y.FamilyID
		}
		fullUrl += "/renameFile.action"
		queryParam["fileId"] = torrentFile.GetID()
		queryParam["destFileName"] = newTorrentName

		_, renameErr := y.request(fullUrl, method, func(req *resty.Request) {
			req.SetContext(context.Background()).SetQueryParams(queryParam)
		}, nil, &RenameResp{}, isFamily)
		if renameErr != nil {
			utils.Log.Warnf("跟随重命名 torrent 文件失败: %v", renameErr)
		}
	}()
}
