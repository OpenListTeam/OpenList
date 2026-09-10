package s3

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type objectMetadata struct {
	headers map[string]string
	hash    []byte
	etag    string
}

// Only reuse upload metadata when it still describes the current content.
func (b *s3Backend) loadMetadata(name string, hash []byte) (map[string]string, string) {
	if value, ok := b.meta.Load(name); ok {
		metadata := value.(objectMetadata)
		if bytes.Equal(metadata.hash, hash) {
			return metadata.headers, metadata.etag
		}
	}
	return nil, ""
}

// Metadata alone cannot identify content: clients can preserve both file size
// and modification time when overwriting an object. Use the driver's MD5 when
// available, otherwise hash the complete content, including for HEAD and ranges.
func getObjectHash(ctx context.Context, name string, obj model.Obj) ([]byte, error) {
	if value := obj.GetHash().GetHash(utils.MD5); value != "" {
		hash, err := hex.DecodeString(value)
		if err != nil || len(hash) != md5.Size {
			return nil, fmt.Errorf("invalid object MD5: %q", value)
		}
		return hash, nil
	}
	link, file, err := fs.Link(ctx, name, model.LinkArgs{})
	if err != nil {
		return nil, err
	}
	defer link.Close()
	size := link.ContentLength
	if size <= 0 {
		size = file.GetSize()
	}
	ranges, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		return nil, err
	}
	reader, err := ranges.RangeRead(ctx, http_range.Range{Length: -1})
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	hash := md5.New()
	n, err := utils.CopyWithBuffer(hash, reader)
	if err != nil {
		return nil, err
	}
	if n != size {
		return nil, io.ErrUnexpectedEOF
	}
	return hash.Sum(nil), nil
}
