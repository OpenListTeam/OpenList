package fs

import (
	"context"
	"fmt"
	"io"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
)

// RehashSeedFromFiles 读取 basePath 下的实际文件，补全目标格式所需的哈希。
//
// 返回目标为 torrent 时的「整包」SHA-1 pieces：标准 torrent 的 pieces 是
// 所有文件首尾相接后按 piece 切分的，跨文件边界不能按文件单独切，
// 所以必须整包读一遍（这也是转换时提示 piece boundary crosses the next file 的原因）。
func RehashSeedFromFiles(
	ctx context.Context,
	user *model.User,
	seed *torrent.Seed,
	basePath string,
	targetFormat string,
) ([]byte, error) {
	if strings.TrimSpace(basePath) == "" {
		return nil, fmt.Errorf("file_base_path is required to compute missing hashes")
	}
	if seed == nil || len(seed.Files) == 0 {
		return nil, fmt.Errorf("seed has no files")
	}
	if seed.PieceSize <= 0 {
		seed.PieceSize = torrent.DefaultPieceSize
	}
	base, err := user.JoinPath(basePath)
	if err != nil {
		return nil, err
	}
	switch strings.ToLower(strings.TrimPrefix(strings.TrimSpace(targetFormat), ".")) {
	case "torrent":
		return rehashTorrentPieces(ctx, seed, base)
	case "cas":
		return nil, rehashCAS(ctx, seed, base)
	default:
		return nil, fmt.Errorf("unsupported target format %q", targetFormat)
	}
}

// rehashTorrentPieces 整包计算 SHA-1 pieces，并顺手补上每个文件自己的整文件哈希。
func rehashTorrentPieces(ctx context.Context, seed *torrent.Seed, base string) ([]byte, error) {
	global := torrent.NewHashWriter(seed.PieceSize, seed.PieceSize, 0)
	for i := range seed.Files {
		file := &seed.Files[i]
		rc, err := openSeedSource(ctx, stdpath.Join(base, file.Path), file.Size)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", file.Path, err)
		}
		fileHasher := torrent.NewHashWriter(seed.PieceSize, seed.PieceSize, 0)
		n, copyErr := io.Copy(io.MultiWriter(global, fileHasher), rc)
		_ = rc.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("read %s: %w", file.Path, copyErr)
		}
		if n != file.Size {
			return nil, fmt.Errorf("read %s: got %d of %d bytes", file.Path, n, file.Size)
		}
		fileHasher.Finish()
		built := fileHasher.BuildSeedFile(file.Path, file.Modified)
		if built.Hashes.SHA1 != "" {
			file.Hashes.SHA1 = built.Hashes.SHA1
		}
		if built.Hashes.Pieces != nil {
			if file.Hashes.Pieces == nil {
				file.Hashes.Pieces = &torrent.SeedPieceHashes{}
			}
			file.Hashes.Pieces.SHA1 = built.Hashes.Pieces.SHA1
		}
	}
	global.Finish()
	return global.GetPieceHashes(), nil
}

// rehashCAS 按 10 MiB 分片计算 MD5 整文件哈希与分片哈希。
func rehashCAS(ctx context.Context, seed *torrent.Seed, base string) error {
	seed.PieceSize = torrent.DefaultPieceSize
	for i := range seed.Files {
		file := &seed.Files[i]
		rc, err := openSeedSource(ctx, stdpath.Join(base, file.Path), file.Size)
		if err != nil {
			return fmt.Errorf("open %s: %w", file.Path, err)
		}
		hasher := torrent.NewHashWriter(torrent.DefaultPieceSize, torrent.DefaultPieceSize, 0)
		n, copyErr := io.Copy(hasher, rc)
		_ = rc.Close()
		if copyErr != nil {
			return fmt.Errorf("read %s: %w", file.Path, copyErr)
		}
		if n != file.Size {
			return fmt.Errorf("read %s: got %d of %d bytes", file.Path, n, file.Size)
		}
		hasher.Finish()
		built := hasher.BuildSeedFile(file.Path, file.Modified)
		file.Hashes.MD5 = built.Hashes.MD5
		if built.Hashes.Pieces != nil {
			if file.Hashes.Pieces == nil {
				file.Hashes.Pieces = &torrent.SeedPieceHashes{}
			}
			file.Hashes.Pieces.MD5 = built.Hashes.Pieces.MD5
		}
	}
	return nil
}

// openSeedSource 打开一个 VFS 文件的完整读取流。
func openSeedSource(ctx context.Context, fullPath string, size int64) (io.ReadCloser, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return nil, err
	}
	link, _, err := op.Link(ctx, storage, actualPath, model.LinkArgs{})
	if err != nil {
		return nil, fmt.Errorf("storage cannot stream file: %w", err)
	}
	rangeReader, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		return nil, fmt.Errorf("storage cannot stream file")
	}
	return rangeReader.RangeRead(ctx, http_range.Range{Length: size})
}
