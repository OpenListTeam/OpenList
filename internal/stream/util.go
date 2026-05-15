package stream

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/pool"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

type RangeReaderFunc func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error)

func (f RangeReaderFunc) RangeRead(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
	return f(ctx, httpRange)
}

func GetRangeReaderFromLink(size int64, link *model.Link) (model.RangeReaderIF, error) {
	if link.RangeReader != nil {
		if link.Concurrency < 1 && link.PartSize < 1 {
			return link.RangeReader, nil
		}
		down := net.NewDownloader(func(d *net.Downloader) {
			d.Concurrency = link.Concurrency
			d.PartSize = link.PartSize
			d.HttpClient = net.GetRangeReaderHttpRequestFunc(link.RangeReader)
		})
		rangeReader := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
			return down.Download(ctx, &net.HttpRequestParams{
				Range: httpRange,
				Size:  size,
			})
		}
		// RangeReader只能在驱动限速
		return RangeReaderFunc(rangeReader), nil
	}

	if len(link.URL) == 0 {
		return nil, errors.New("invalid link: must have at least one of URL or RangeReader")
	}

	if link.Concurrency > 0 || link.PartSize > 0 {
		down := net.NewDownloader(func(d *net.Downloader) {
			d.Concurrency = link.Concurrency
			d.PartSize = link.PartSize
			d.HttpClient = func(ctx context.Context, params *net.HttpRequestParams) (*http.Response, error) {
				if ServerDownloadLimit == nil {
					return net.DefaultHttpRequestFunc(ctx, params)
				}
				resp, err := net.DefaultHttpRequestFunc(ctx, params)
				if err == nil && resp.Body != nil {
					resp.Body = &RateLimitReader{
						Ctx:     ctx,
						Reader:  resp.Body,
						Limiter: ServerDownloadLimit,
					}
				}
				return resp, err
			}
		})
		rangeReader := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
			requestHeader, _ := ctx.Value(conf.RequestHeaderKey).(http.Header)
			header := net.ProcessHeader(requestHeader, link.Header)
			return down.Download(ctx, &net.HttpRequestParams{
				Range:     httpRange,
				Size:      size,
				URL:       link.URL,
				HeaderRef: header,
			})
		}
		return RangeReaderFunc(rangeReader), nil
	}

	rangeReader := func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
		if httpRange.Length < 0 || httpRange.Start+httpRange.Length > size {
			httpRange.Length = size - httpRange.Start
		}
		requestHeader, _ := ctx.Value(conf.RequestHeaderKey).(http.Header)
		header := net.ProcessHeader(requestHeader, link.Header)
		header = http_range.ApplyRangeToHttpHeader(httpRange, header)

		response, err := net.RequestHttp(ctx, "GET", header, link.URL)
		if err != nil {
			if _, ok := errs.UnwrapOrSelf(err).(net.HttpStatusCodeError); ok {
				return nil, err
			}
			return nil, fmt.Errorf("http request failure, err:%w", err)
		}
		if ServerDownloadLimit != nil {
			response.Body = &RateLimitReader{
				Ctx:     ctx,
				Reader:  response.Body,
				Limiter: ServerDownloadLimit,
			}
		}
		if httpRange.Start == 0 && httpRange.Length == size ||
			response.StatusCode == http.StatusPartialContent ||
			checkContentRange(&response.Header, httpRange.Start) {
			return response.Body, nil
		} else if response.StatusCode == http.StatusOK {
			log.Warnf("remote http server not supporting range request, expect low perfromace!")
			readCloser, err := net.GetRangedHttpReader(response.Body, httpRange.Start, httpRange.Length)
			if err != nil {
				return nil, err
			}
			return readCloser, nil
		}
		return response.Body, nil
	}
	return RangeReaderFunc(rangeReader), nil
}

func GetRangeReaderFromMFile(size int64, file model.File) *model.FileRangeReader {
	return &model.FileRangeReader{
		RangeReaderIF: RangeReaderFunc(func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
			length := httpRange.Length
			if length < 0 || httpRange.Start+length > size {
				length = size - httpRange.Start
			}
			return &model.FileCloser{File: io.NewSectionReader(file, httpRange.Start, length)}, nil
		}),
	}
}

// 139 cloud does not properly return 206 http status code, add a hack here
func checkContentRange(header *http.Header, offset int64) bool {
	start, _, err := http_range.ParseContentRange(header.Get("Content-Range"))
	if err != nil {
		log.Warnf("exception trying to parse Content-Range, will ignore,err=%s", err)
	}
	if start == offset {
		return true
	}
	return false
}

type ReaderWithCtx struct {
	io.Reader
	Ctx context.Context
}

func (r *ReaderWithCtx) Read(p []byte) (n int, err error) {
	if utils.IsCanceled(r.Ctx) {
		return 0, r.Ctx.Err()
	}
	return r.Reader.Read(p)
}

func (r *ReaderWithCtx) Close() error {
	if c, ok := r.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

func CacheFullAndHash(stream model.FileStreamer, up *model.UpdateProgress, hashType *utils.HashType, hashParams ...any) (model.File, string, error) {
	h := hashType.NewFunc(hashParams...)
	tmpF, err := stream.CacheFullAndWriter(up, h)
	if err != nil {
		return nil, "", err
	}
	return tmpF, hex.EncodeToString(h.Sum(nil)), nil
}

type StreamSectionReader interface {
	// 线程不安全
	GetSectionReader(off, length int64) (io.ReadSeeker, error)
	// 线程安全
	FreeSectionReader(sr io.ReadSeeker)
	// 线程不安全
	DiscardSection(off int64, length int64) error
}

func NewStreamSectionReader(file model.FileStreamer, maxBufferSize int, up *model.UpdateProgress) (StreamSectionReader, error) {
	if file.GetFile() != nil {
		return &cachedSectionReader{file.GetFile()}, nil
	}

	maxBufferSize = min(maxBufferSize, int(file.GetSize()))
	if file.GetSize() <= int64(conf.CacheThreshold) {
		bufPool := &pool.Pool[[]byte]{
			New: func() []byte {
				return make([]byte, maxBufferSize)
			},
		}
		file.Add(bufPool)
		ss := &byteSectionReader{file: file, bufPool: bufPool}
		return ss, nil
	}

	blockSize := min(uint64(maxBufferSize), conf.MaxBlockLimit)
	hc, err := mem.NewHybridCache(blockSize, uint64(file.GetSize()))
	if err != nil {
		return nil, err
	}
	file.Add(hc)
	return &hybridSectionReader{file: file, hc: hc}, nil
}

type cachedSectionReader struct {
	cache io.ReaderAt
}

func (*cachedSectionReader) DiscardSection(off int64, length int64) error {
	return nil
}
func (s *cachedSectionReader) GetSectionReader(off, length int64) (io.ReadSeeker, error) {
	return io.NewSectionReader(s.cache, off, length), nil
}
func (*cachedSectionReader) FreeSectionReader(sr io.ReadSeeker) {}

type byteSectionReader struct {
	file       model.FileStreamer
	fileOffset int64
	bufPool    *pool.Pool[[]byte]
}

// 线程不安全
func (ss *byteSectionReader) DiscardSection(off int64, length int64) error {
	if off != ss.fileOffset {
		return fmt.Errorf("stream not cached: request offset %d != current offset %d", off, ss.fileOffset)
	}
	n, err := utils.CopyWithBufferN(io.Discard, ss.file, length)
	ss.fileOffset += n
	if err != nil {
		return fmt.Errorf("failed to skip data: (expect =%d, actual =%d) %w", length, n, err)
	}
	return nil
}

type bytesRefReadSeeker struct {
	io.ReadSeeker
	buf []byte
}

// 线程不安全
func (ss *byteSectionReader) GetSectionReader(off, length int64) (io.ReadSeeker, error) {
	if off != ss.fileOffset {
		return nil, fmt.Errorf("stream not cached: request offset %d != current offset %d", off, ss.fileOffset)
	}
	tempBuf := ss.bufPool.Get()
	buf := tempBuf[:length]
	n, err := io.ReadFull(ss.file, buf)
	ss.fileOffset += int64(n)
	if int64(n) != length {
		return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", length, n, err)
	}
	return &bytesRefReadSeeker{bytes.NewReader(buf), buf}, nil
}
func (ss *byteSectionReader) FreeSectionReader(rs io.ReadSeeker) {
	if sr, ok := rs.(*bytesRefReadSeeker); ok {
		ss.bufPool.Put(sr.buf[0:cap(sr.buf)])
		sr.buf = nil
		sr.ReadSeeker = nil
	}
}

type hybridSectionReader struct {
	file       model.FileStreamer
	fileOffset int64
	hc         *mem.HybridCache
	mu         sync.Mutex
	cache      []buffer.Block
}

// 线程不安全
func (ss *hybridSectionReader) DiscardSection(off int64, length int64) error {
	if off != ss.fileOffset {
		return fmt.Errorf("stream not cached: request offset %d != current offset %d", off, ss.fileOffset)
	}
	n, err := utils.CopyWithBufferN(io.Discard, ss.file, length)
	ss.fileOffset += n
	if err != nil {
		return fmt.Errorf("failed to skip data: (expect =%d, actual =%d) %w", length, n, err)
	}
	return nil
}

type blockRefReadSeeker struct {
	io.ReadSeeker
	b buffer.Block
}

// 线程不安全
func (ss *hybridSectionReader) GetSectionReader(off, length int64) (io.ReadSeeker, error) {
	if off != ss.fileOffset {
		return nil, fmt.Errorf("stream not cached: request offset %d != current offset %d", off, ss.fileOffset)
	}
	b := ss.get()
	if b == nil {
		offset := int64(ss.hc.Size())
		written, err := ss.hc.CopyFromN(ss.file, length)
		ss.fileOffset += written
		if written != length {
			return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", length, written, err)
		}
		b = buffer.NewBlockAdapter(
			io.NewOffsetWriter(ss.hc, offset),
			io.NewSectionReader(ss.hc, offset, length),
		)
	} else {
		ws := buffer.WriteAtSeekerOf(b)
		if _, err := ws.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to reset cached block writer: %w", err)
		}
		written, err := utils.CopyWithBufferN(ws, ss.file, length)
		ss.fileOffset += written
		if written != length {
			return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", length, written, err)
		}
	}

	if length == b.Size() {
		rs := buffer.ReadAtSeekerOf(b)
		if _, err := rs.Seek(0, io.SeekStart); err != nil {
			return nil, fmt.Errorf("failed to reset cached block reader: %w", err)
		}
		return &blockRefReadSeeker{rs, b}, nil
	}
	return &blockRefReadSeeker{io.NewSectionReader(b, 0, length), b}, nil
}

func (ss *hybridSectionReader) get() buffer.Block {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	if len(ss.cache) > 0 {
		b := ss.cache[len(ss.cache)-1]
		ss.cache = ss.cache[:len(ss.cache)-1]
		return b
	}
	return nil
}
func (ss *hybridSectionReader) put(b buffer.Block) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	ss.cache = append(ss.cache, b)
}

func (ss *hybridSectionReader) FreeSectionReader(rs io.ReadSeeker) {
	if sr, ok := rs.(*blockRefReadSeeker); ok {
		ss.put(sr.b)
		sr.b = nil
		sr.ReadSeeker = nil
	}
}
