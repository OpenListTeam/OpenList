package torrent

import (
	"fmt"
	"io"
	"os"
	"path"
	"strings"
)

// GenerateFromFile 从文件路径生成通用的 torrent 文件（不含 CAS 扩展）
// 这是一个通用函数，适用于所有驱动
func GenerateFromFile(filePath string) ([]byte, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	return GenerateFromReader(f, info.Name(), info.Size(), DefaultPieceSize)
}

// GenerateFromReader 从 io.Reader 生成通用的 torrent 文件（不含 CAS 扩展）
// 返回 torrent 字节数据
func GenerateFromReader(reader io.Reader, fileName string, fileSize int64, pieceSize int64) ([]byte, error) {
	if pieceSize <= 0 {
		pieceSize = DefaultPieceSize
	}

	hw := NewHashWriter(pieceSize, pieceSize, fileSize)

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
	pieceHashes := hw.GetPieceHashes()

	t := NewTorrent(fileName, fileSize, fileMD5)
	t.Info.PieceLength = pieceSize
	t.SetPieces(pieceHashes)

	return t.Encode()
}

// GenerateFromReaderWithCAS 从 io.Reader 生成包含 CAS 扩展的 torrent 文件
// 适用于天翼云等支持秒传的网盘
func GenerateFromReaderWithCAS(reader io.Reader, fileName string, fileSize int64, pieceSize int64) ([]byte, error) {
	if pieceSize <= 0 {
		pieceSize = DefaultPieceSize
	}

	hw := NewHashWriter(pieceSize, pieceSize, fileSize)

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

	// 计算 sliceMD5
	sliceMD5 := fileMD5
	if len(sliceMD5s) > 1 {
		joined := strings.Join(sliceMD5s, "\n")
		sliceMD5 = strings.ToUpper(GetMD5Str(joined))
	}

	t := NewTorrent(fileName, fileSize, fileMD5)
	t.Info.PieceLength = pieceSize
	t.SetPieces(pieceHashes)
	t.SetCASInfo(&CASInfo{
		FileMD5:   fileMD5,
		SliceMD5:  sliceMD5,
		SliceMD5s: sliceMD5s,
		SliceSize: pieceSize,
		Cloud:     Cloud189,
	})

	return t.Encode()
}

// GenerateFromFileWithCAS 从文件路径生成包含 CAS 扩展的 torrent 文件
func GenerateFromFileWithCAS(filePath string) ([]byte, error) {
	f, err := os.Open(filePath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	return GenerateFromReaderWithCAS(f, info.Name(), info.Size(), DefaultPieceSize)
}

// GenerateSeedFromReader computes the complete OSS hash matrix in one stream pass.
func GenerateSeedFromReader(reader io.Reader, filePath string, expectedSize, pieceSize int64, createdBy string) (*Seed, error) {
	if pieceSize <= 0 {
		pieceSize = DefaultPieceSize
	}
	if err := validateRelativeSeedPath(filePath); err != nil {
		return nil, err
	}
	hw := NewHashWriter(pieceSize, pieceSize, expectedSize)
	if _, err := CopyAndHash(nil, reader, hw); err != nil {
		return nil, err
	}
	hw.Finish()
	if expectedSize >= 0 && hw.GetTotalWritten() != expectedSize {
		return nil, fmt.Errorf("stream size mismatch: read %d bytes, expected %d", hw.GetTotalWritten(), expectedSize)
	}
	seed := NewSeed(path.Base(filePath), createdBy, pieceSize)
	seed.Files = []SeedFile{hw.BuildSeedFile(filePath, "")}
	if err := ValidateSeed(seed, DefaultParseLimits()); err != nil {
		return nil, err
	}
	return seed, nil
}
