package fs

import (
	"context"
	"fmt"
	"io"
	"math"
	stdpath "path"
	"strings"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/torrent"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/pkg/errors"
)

var seedSourceMetaProvider = op.GetNearestMeta
var seedSourceStreamOpener = openSeedSourceStream
var seedHashSlots = make(chan struct{}, 2)

// AcquireSeedHashSlot limits simultaneous file-hashing operations across seed APIs.
func AcquireSeedHashSlot(ctx context.Context) (func(), error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	select {
	case seedHashSlots <- struct{}{}:
		if err := ctx.Err(); err != nil {
			<-seedHashSlots
			return nil, err
		}
		var once sync.Once
		return func() { once.Do(func() { <-seedHashSlots }) }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func addSeedHashSize(total, size int64) (int64, error) {
	if size < 0 || size > MaxSeedGenerateSyncSize || total < 0 || total > MaxSeedGenerateSyncSize-size {
		return total, fmt.Errorf("seed hash operation exceeds 1GB file or total limit")
	}
	return total + size, nil
}

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
	return RehashSeedFromFilesWithMatrix(ctx, user, seed, basePath, targetFormat, SeedHashMatrix{})
}

// RehashSeedFromFilesWithMatrix adds requested hashes and the target format's required hashes.
func RehashSeedFromFilesWithMatrix(
	ctx context.Context,
	user *model.User,
	seed *torrent.Seed,
	basePath string,
	targetFormat string,
	matrix SeedHashMatrix,
) ([]byte, error) {
	if strings.TrimSpace(basePath) == "" {
		return nil, fmt.Errorf("file_base_path is required to compute missing hashes")
	}
	if seed == nil || len(seed.Files) == 0 {
		return nil, fmt.Errorf("seed has no files")
	}
	if len(seed.Files) > torrent.DefaultMaxSeedFiles {
		return nil, fmt.Errorf("too many seed files")
	}
	var total int64
	for _, file := range seed.Files {
		var err error
		total, err = addSeedHashSize(total, file.Size)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", file.Path, err)
		}
	}
	release, err := AcquireSeedHashSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer release()
	base, err := user.JoinPath(basePath)
	if err != nil {
		return nil, err
	}
	target := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(targetFormat), "."))
	if target == "bt" {
		target = "torrent"
	}
	switch target {
	case "torrent":
		matrix.SHA1 = SeedHashSelection{Whole: true, Pieces: true}
	case "cas":
		matrix.MD5 = SeedHashSelection{Whole: true, Pieces: true}
	case "oss":
	default:
		return nil, fmt.Errorf("unsupported target format %q", targetFormat)
	}
	pieceSize := seed.PieceSize
	if pieceSize <= 0 || target == "cas" {
		pieceSize = torrent.DefaultPieceSize
	}
	setSeedHashPieceSize(seed, pieceSize)
	return rehashSeedContents(ctx, user, seed, base, matrix, target == "torrent")
}

func setSeedHashPieceSize(seed *torrent.Seed, pieceSize int64) {
	if seed.PieceSize != pieceSize {
		seed.GlobalPieces = nil
		seed.AvailablePieceHashes = nil
		for i := range seed.Files {
			seed.Files[i].Hashes.Pieces = nil
		}
	}
	seed.PieceSize = pieceSize
}

func rehashSeedContents(ctx context.Context, user *model.User, seed *torrent.Seed, base string, matrix SeedHashMatrix, needGlobal bool) ([]byte, error) {
	var global *torrent.HashWriter
	if needGlobal || matrix.SHA1.Pieces && len(seed.GlobalPieces) > 0 {
		global = torrent.NewHashWriter(seed.PieceSize, seed.PieceSize, 0)
	}
	for i := range seed.Files {
		file := &seed.Files[i]
		rc, err := openSeedSource(ctx, user, stdpath.Join(base, file.Path), file.Size)
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", file.Path, err)
		}
		hasher := torrent.NewHashWriter(seed.PieceSize, seed.PieceSize, 0)
		var writer io.Writer = hasher
		if global != nil {
			writer = io.MultiWriter(global, hasher)
		}
		n, copyErr := io.Copy(writer, io.LimitReader(rc, file.Size+1))
		_ = rc.Close()
		if copyErr != nil {
			return nil, fmt.Errorf("read %s: %w", file.Path, copyErr)
		}
		if n != file.Size {
			return nil, fmt.Errorf("read %s: got %d of %d bytes", file.Path, n, file.Size)
		}
		hasher.Finish()
		built := hasher.BuildSeedFile(file.Path, file.Modified)
		mergeSeedRehashMatrix(&file.Hashes, built.Hashes, matrix)
	}
	if global != nil {
		global.Finish()
		seed.GlobalPieces = global.GetPieceSHA1s()
		return global.GetPieceHashes(), nil
	}
	return nil, nil
}

func mergeSeedRehashMatrix(hashes *torrent.SeedHashes, built torrent.SeedHashes, matrix SeedHashMatrix) {
	if matrix.MD5.Whole {
		hashes.MD5 = built.MD5
	}
	if matrix.SHA1.Whole {
		hashes.SHA1 = built.SHA1
	}
	if matrix.SHA256.Whole {
		hashes.SHA256 = built.SHA256
	}
	if built.Pieces == nil || !matrix.MD5.Pieces && !matrix.SHA1.Pieces && !matrix.SHA256.Pieces {
		return
	}
	if hashes.Pieces == nil {
		hashes.Pieces = &torrent.SeedPieceHashes{}
	}
	if matrix.MD5.Pieces {
		hashes.Pieces.MD5 = built.Pieces.MD5
	}
	if matrix.SHA1.Pieces {
		hashes.Pieces.SHA1 = built.Pieces.SHA1
	}
	if matrix.SHA256.Pieces {
		hashes.Pieces.SHA256 = built.Pieces.SHA256
	}
}

// openSeedSource 打开一个 VFS 文件的完整读取流。
func openSeedSource(ctx context.Context, user *model.User, fullPath string, size int64) (io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	meta, err := seedSourceMetaProvider(fullPath)
	if err != nil && !errors.Is(errors.Cause(err), errs.MetaNotFound) {
		return nil, err
	}
	if !common.CanRead(user, meta, fullPath) {
		return nil, errs.PermissionDenied
	}
	return seedSourceStreamOpener(ctx, fullPath, size)
}

func openSeedSourceStream(ctx context.Context, fullPath string, size int64) (io.ReadCloser, error) {
	storage, actualPath, err := op.GetStorageAndActualPath(fullPath)
	if err != nil {
		return nil, err
	}
	link, obj, err := op.Link(ctx, storage, actualPath, model.LinkArgs{})
	if err != nil {
		return nil, fmt.Errorf("storage cannot stream file: %w", err)
	}
	return openSeedLinkStream(ctx, link, obj, size)
}

type seedLinkedReader struct {
	io.Reader
	utils.SyncClosers
}

func openSeedLinkStream(ctx context.Context, link *model.Link, obj model.Obj, size int64) (io.ReadCloser, error) {
	if link == nil {
		return nil, fmt.Errorf("storage returned no link")
	}
	keepLink := false
	defer func() {
		if !keepLink {
			_ = link.Close()
		}
	}()
	if size < 0 || size == math.MaxInt64 || obj == nil || obj.IsDir() || obj.GetSize() != size {
		return nil, fmt.Errorf("seed source size does not match the actual file")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	rangeReader, err := stream.GetRangeReaderFromLink(obj.GetSize(), link)
	if err != nil {
		return nil, fmt.Errorf("storage cannot stream file: %w", err)
	}
	rc, err := rangeReader.RangeRead(ctx, http_range.Range{Length: obj.GetSize()})
	if err != nil {
		if rc != nil {
			_ = rc.Close()
		}
		return nil, err
	}
	if rc == nil {
		return nil, fmt.Errorf("storage returned no reader")
	}
	keepLink = true
	return &seedLinkedReader{
		Reader:      io.LimitReader(&stream.ReaderWithCtx{Ctx: ctx, Reader: rc}, size+1),
		SyncClosers: utils.NewSyncClosers(rc, link),
	}, nil
}
