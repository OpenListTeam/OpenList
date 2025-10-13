package halalcloudopen

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
	"github.com/ipfs/go-cid"
)

// get the next chunk
func (oo *openObject) getChunk(_ context.Context) (err error) {
	if oo.id >= len(oo.chunks) {
		return io.EOF
	}
	var chunk []byte
	err = utils.Retry(3, time.Second, func() (err error) {
		chunk, err = getRawFiles(oo.d[oo.id])
		return err
	})
	if err != nil {
		return err
	}
	oo.id++
	oo.chunk = chunk
	return nil
}

// Read reads up to len(p) bytes into p.
func (oo *openObject) Read(p []byte) (n int, err error) {
	oo.mu.Lock()
	defer oo.mu.Unlock()
	if oo.closed {
		return 0, fmt.Errorf("read on closed file")
	}
	// Skip data at the start if requested
	for oo.skip > 0 {
		//size := 1024 * 1024
		_, size, err := oo.ChunkLocation(oo.id)
		if err != nil {
			return 0, err
		}
		if oo.skip < int64(size) {
			break
		}
		oo.id++
		oo.skip -= int64(size)
	}
	if len(oo.chunk) == 0 {
		err = oo.getChunk(oo.ctx)
		if err != nil {
			return 0, err
		}
		if oo.skip > 0 {
			oo.chunk = (oo.chunk)[oo.skip:]
			oo.skip = 0
		}
	}
	n = copy(p, oo.chunk)
	oo.shaTemp.Write(p[:n])
	oo.chunk = (oo.chunk)[n:]
	return n, nil
}

// Close closed the file - MAC errors are reported here
func (oo *openObject) Close() (err error) {
	oo.mu.Lock()
	defer oo.mu.Unlock()
	if oo.closed {
		return nil
	}
	// 校验Sha1
	if string(oo.shaTemp.Sum(nil)) != oo.sha {
		return fmt.Errorf("failed to finish download: SHA mismatch")
	}

	oo.closed = true
	return nil
}

func GetMD5Hash(text string) string {
	tHash := md5.Sum([]byte(text))
	return hex.EncodeToString(tHash[:])
}

type chunkSize struct {
	position int64
	size     int
}

type openObject struct {
	ctx     context.Context
	mu      sync.Mutex
	d       []*sdkUserFile.SliceDownloadInfo
	id      int
	skip    int64
	chunk   []byte
	chunks  []chunkSize
	closed  bool
	sha     string
	shaTemp hash.Hash
}

func getChunkSizes(sliceSize []*sdkUserFile.SliceSize) (chunks []chunkSize) {
	chunks = make([]chunkSize, 0)
	for _, s := range sliceSize {
		// 对最后一个做特殊处理
		endIndex := s.EndIndex
		startIndex := s.StartIndex
		if endIndex == 0 {
			endIndex = startIndex
		}
		for j := startIndex; j <= endIndex; j++ {
			size := s.Size
			chunks = append(chunks, chunkSize{position: j, size: int(size)})
		}
	}
	return chunks
}

func (oo *openObject) ChunkLocation(id int) (position int64, size int, err error) {
	if id < 0 || id >= len(oo.chunks) {
		return 0, 0, errors.New("invalid arguments")
	}

	return (oo.chunks)[id].position, (oo.chunks)[id].size, nil
}

func getRawFiles(addr *sdkUserFile.SliceDownloadInfo) ([]byte, error) {

	if addr == nil {
		return nil, errors.New("addr is nil")
	}

	client := http.Client{
		Timeout: time.Duration(60 * time.Second), // Set timeout to 60 seconds
	}
	resp, err := client.Get(addr.DownloadAddress)
	if err != nil {

		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bad status: %s, body: %s", resp.Status, body)
	}

	if addr.Encrypt > 0 {
		cd := uint8(addr.Encrypt)
		for idx := 0; idx < len(body); idx++ {
			body[idx] = body[idx] ^ cd
		}
	}
	storeType := addr.StoreType
	if storeType != 10 {

		sourceCid, err := cid.Decode(addr.Identity)
		if err != nil {
			return nil, err
		}
		checkCid, err := sourceCid.Prefix().Sum(body)
		if err != nil {
			return nil, err
		}
		if !checkCid.Equals(sourceCid) {
			return nil, fmt.Errorf("bad cid: %s, body: %s", checkCid.String(), body)
		}
	}

	return body, nil

}
