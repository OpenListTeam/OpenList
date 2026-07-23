package torrent

import (
	"crypto/md5"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"strings"
)

// HashWriter 同时计算文件的 MD5、分片 MD5 和 SHA-1 piece hash
// 用于在上传过程中一次性计算所有需要的哈希值
type HashWriter struct {
	// 整文件 MD5
	fileMD5 hash.Hash
	// 当前分片 MD5
	sliceMD5 hash.Hash
	// 当前 piece 的 SHA-1
	pieceSHA1 hash.Hash

	// 分片大小（默认 10MB）
	sliceSize int64
	// piece 大小（与 sliceSize 相同，保持对齐）
	pieceSize int64

	// 当前分片已写入字节数
	sliceWritten int64
	// 当前 piece 已写入字节数
	pieceWritten int64
	// 总写入字节数
	totalWritten int64

	// 每个分片的 MD5（大写十六进制）
	sliceMD5Hexs []string
	// 所有 piece 的 SHA-1 哈希拼接
	pieceHashes []byte
}

// NewHashWriter 创建一个新的 HashWriter
// sliceSize: CAS 分片大小（通常 10MB）
// pieceSize: BT piece 大小（设为与 sliceSize 相同以保持对齐）
func NewHashWriter(sliceSize, pieceSize int64) *HashWriter {
	if sliceSize <= 0 {
		sliceSize = DefaultPieceSize
	}
	if pieceSize <= 0 {
		pieceSize = DefaultPieceSize
	}
	return &HashWriter{
		fileMD5:   md5.New(),
		sliceMD5:  md5.New(),
		pieceSHA1: sha1.New(),
		sliceSize: sliceSize,
		pieceSize: pieceSize,
	}
}

// NewDefaultHashWriter 创建默认的 HashWriter（10MB 分片）
func NewDefaultHashWriter() *HashWriter {
	return NewHashWriter(DefaultPieceSize, DefaultPieceSize)
}

// Write 实现 io.Writer 接口
func (hw *HashWriter) Write(p []byte) (n int, err error) {
	total := len(p)
	offset := 0

	for offset < total {
		// 计算当前可以写入的字节数（取分片和 piece 剩余空间的最小值）
		sliceRemain := hw.sliceSize - hw.sliceWritten
		pieceRemain := hw.pieceSize - hw.pieceWritten
		canWrite := min64(sliceRemain, pieceRemain)
		canWrite = min64(canWrite, int64(total-offset))

		chunk := p[offset : offset+int(canWrite)]

		// 写入整文件 MD5
		hw.fileMD5.Write(chunk)
		// 写入当前分片 MD5
		hw.sliceMD5.Write(chunk)
		// 写入当前 piece SHA-1
		hw.pieceSHA1.Write(chunk)

		hw.sliceWritten += canWrite
		hw.pieceWritten += canWrite
		hw.totalWritten += canWrite
		offset += int(canWrite)

		// 检查分片是否完成
		if hw.sliceWritten >= hw.sliceSize {
			hw.finishSlice()
		}

		// 检查 piece 是否完成
		if hw.pieceWritten >= hw.pieceSize {
			hw.finishPiece()
		}
	}

	return total, nil
}

// finishSlice 完成当前分片的 MD5 计算
func (hw *HashWriter) finishSlice() {
	md5Hex := strings.ToUpper(hex.EncodeToString(hw.sliceMD5.Sum(nil)))
	hw.sliceMD5Hexs = append(hw.sliceMD5Hexs, md5Hex)
	hw.sliceMD5.Reset()
	hw.sliceWritten = 0
}

// finishPiece 完成当前 piece 的 SHA-1 计算
func (hw *HashWriter) finishPiece() {
	hw.pieceHashes = append(hw.pieceHashes, hw.pieceSHA1.Sum(nil)...)
	hw.pieceSHA1.Reset()
	hw.pieceWritten = 0
}

// Finish 完成所有哈希计算（处理最后不完整的分片/piece）
func (hw *HashWriter) Finish() {
	// 处理最后一个不完整的分片
	if hw.sliceWritten > 0 {
		hw.finishSlice()
	}
	// 处理最后一个不完整的 piece
	if hw.pieceWritten > 0 {
		hw.finishPiece()
	}
}

// GetFileMD5 获取整文件 MD5（大写十六进制）
func (hw *HashWriter) GetFileMD5() string {
	return strings.ToUpper(hex.EncodeToString(hw.fileMD5.Sum(nil)))
}

// GetSliceMD5s 获取所有分片的 MD5 列表
func (hw *HashWriter) GetSliceMD5s() []string {
	return hw.sliceMD5Hexs
}

// GetSliceMD5 获取最终的 sliceMD5（用于秒传）
func (hw *HashWriter) GetSliceMD5(fileMD5 string) string {
	if len(hw.sliceMD5Hexs) <= 1 {
		return fileMD5
	}
	joined := strings.Join(hw.sliceMD5Hexs, "\n")
	return strings.ToUpper(GetMD5Str(joined))
}

// GetPieceHashes 获取所有 piece 的 SHA-1 哈希拼接
func (hw *HashWriter) GetPieceHashes() []byte {
	return hw.pieceHashes
}

// GetTotalWritten 获取总写入字节数
func (hw *HashWriter) GetTotalWritten() int64 {
	return hw.totalWritten
}

// BuildTorrent 根据计算结果构建 Torrent 结构
func (hw *HashWriter) BuildTorrent(fileName string, fileSize int64) *Torrent {
	fileMD5 := hw.GetFileMD5()
	sliceMD5 := hw.GetSliceMD5(fileMD5)

	t := NewTorrent(fileName, fileSize, fileMD5)
	t.SetPieces(hw.GetPieceHashes())
	t.SetCASInfo(&CASInfo{
		FileMD5:   fileMD5,
		SliceMD5:  sliceMD5,
		SliceMD5s: hw.GetSliceMD5s(),
		SliceSize: hw.sliceSize,
		Cloud:     "189",
	})

	return t
}

// BuildTorrentBytes 构建并编码 torrent 文件
func (hw *HashWriter) BuildTorrentBytes(fileName string, fileSize int64) ([]byte, error) {
	t := hw.BuildTorrent(fileName, fileSize)
	return t.Encode()
}

// CopyAndHash 从 reader 读取数据，同时写入 writer 和 HashWriter
func CopyAndHash(dst io.Writer, src io.Reader, hw *HashWriter) (int64, error) {
	buf := make([]byte, 32*1024) // 32KB buffer
	var written int64
	for {
		nr, er := src.Read(buf)
		if nr > 0 {
			// 写入 HashWriter
			hw.Write(buf[:nr])
			// 写入目标
			if dst != nil {
				nw, ew := dst.Write(buf[:nr])
				if nw < 0 || nr < nw {
					nw = 0
					if ew == nil {
						ew = fmt.Errorf("invalid write result")
					}
				}
				written += int64(nw)
				if ew != nil {
					return written, ew
				}
				if nr != nw {
					return written, io.ErrShortWrite
				}
			} else {
				written += int64(nr)
			}
		}
		if er != nil {
			if er == io.EOF {
				break
			}
			return written, er
		}
	}
	return written, nil
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}
