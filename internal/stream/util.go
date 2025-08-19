package stream

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/pool"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/rclone/rclone/lib/mmap"
	log "github.com/sirupsen/logrus"
)

type RangeReaderFunc func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error)

func (f RangeReaderFunc) RangeRead(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
	return f(ctx, httpRange)
}

func GetRangeReaderFromLink(size int64, link *model.Link) (model.RangeReaderIF, error) {
	if link.MFile != nil {
		return GetRangeReaderFromMFile(size, link.MFile), nil
	}
	if link.Concurrency > 0 || link.PartSize > 0 {
		down := net.NewDownloader(func(d *net.Downloader) {
			d.Concurrency = link.Concurrency
			d.PartSize = link.PartSize
		})
		var rangeReader RangeReaderFunc = func(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
			var req *net.HttpRequestParams
			if link.RangeReader != nil {
				req = &net.HttpRequestParams{
					Range: httpRange,
					Size:  size,
				}
			} else {
				requestHeader, _ := ctx.Value(conf.RequestHeaderKey).(http.Header)
				header := net.ProcessHeader(requestHeader, link.Header)
				req = &net.HttpRequestParams{
					Range:     httpRange,
					Size:      size,
					URL:       link.URL,
					HeaderRef: header,
				}
			}
			return down.Download(ctx, req)
		}
		if link.RangeReader != nil {
			down.HttpClient = net.GetRangeReaderHttpRequestFunc(link.RangeReader)
			return rangeReader, nil
		}
		return RateLimitRangeReaderFunc(rangeReader), nil
	}

	if link.RangeReader != nil {
		return link.RangeReader, nil
	}

	if len(link.URL) == 0 {
		return nil, errors.New("invalid link: must have at least one of MFile, URL, or RangeReader")
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
			if _, ok := errors.Unwrap(err).(net.ErrorHttpStatusCode); ok {
				return nil, err
			}
			return nil, fmt.Errorf("http request failure, err:%w", err)
		}
		if httpRange.Start == 0 && (httpRange.Length == -1 || httpRange.Length == size) || response.StatusCode == http.StatusPartialContent ||
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
	return RateLimitRangeReaderFunc(rangeReader), nil
}

// RangeReaderIF.RangeRead返回的io.ReadCloser保留file的签名。
func GetRangeReaderFromMFile(size int64, file model.File) model.RangeReaderIF {
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

type StreamSectionReader struct {
	file    model.FileStreamer
	off     int64
	bufPool *pool.Pool[[]byte]
}

func NewStreamSectionReader(file model.FileStreamer, maxBufferSize int, up *model.UpdateProgress) (*StreamSectionReader, error) {
	ss := &StreamSectionReader{file: file}
	if file.GetFile() != nil {
		return ss, nil
	}

	maxBufferSize = min(maxBufferSize, int(file.GetSize()))
	if maxBufferSize > conf.MaxBufferLimit {
		_, err := file.CacheFullAndWriter(up, nil)
		if err != nil {
			return nil, err
		}
		return ss, nil
	}
	if conf.MmapThreshold > 0 && maxBufferSize >= conf.MmapThreshold {
		ss.bufPool = &pool.Pool[[]byte]{
			New: func() []byte {
				buf, err := mmap.Alloc(maxBufferSize)
				if err == nil {
					file.Add(utils.CloseFunc(func() error {
						return mmap.Free(buf)
					}))
				} else {
					buf = make([]byte, maxBufferSize)
				}
				return buf
			},
		}
	} else {
		ss.bufPool = &pool.Pool[[]byte]{
			New: func() []byte {
				return make([]byte, maxBufferSize)
			},
		}
	}

	file.Add(utils.CloseFunc(func() error {
		ss.bufPool.Reset()
		return nil
	}))
	return ss, nil
}

// 线程不安全
func (ss *StreamSectionReader) GetSectionReader(off, length int64) (*SectionReader, error) {
	var cache io.ReaderAt = ss.file.GetFile()
	var buf []byte
	if cache == nil {
		if off != ss.off {
			return nil, fmt.Errorf("stream not cached: request offset %d != current offset %d", off, ss.off)
		}
		tempBuf := ss.bufPool.Get()
		buf = tempBuf[:length]
		n, err := io.ReadFull(ss.file, buf)
		if int64(n) != length {
			return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", length, n, err)
		}
		ss.off += int64(n)
		off = 0
		cache = bytes.NewReader(buf)
	}
	return &SectionReader{io.NewSectionReader(cache, off, length), buf}, nil
}

func (ss *StreamSectionReader) FreeSectionReader(sr *SectionReader) {
	if sr != nil {
		if sr.buf != nil {
			ss.bufPool.Put(sr.buf[0:cap(sr.buf)])
			sr.buf = nil
		}
		sr.ReadSeeker = nil
	}
}

type SectionReader struct {
	io.ReadSeeker
	buf []byte
}
