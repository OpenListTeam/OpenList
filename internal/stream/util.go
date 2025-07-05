package stream

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

func GetRangeReadCloserFromLink(size int64, link *model.Link) (model.RangeReadCloserIF, error) {
	if len(link.URL) == 0 {
		return nil, fmt.Errorf("can't create RangeReadCloser since URL is empty in link")
	}
	rangeReaderFunc := func(ctx context.Context, r http_range.Range) (io.ReadCloser, error) {
		if link.Concurrency > 0 || link.PartSize > 0 {
			header := net.ProcessHeader(nil, link.Header)
			down := net.NewDownloader(func(d *net.Downloader) {
				d.Concurrency = link.Concurrency
				d.PartSize = link.PartSize
			})
			req := &net.HttpRequestParams{
				URL:       link.URL,
				Range:     r,
				Size:      size,
				HeaderRef: header,
			}
			rc, err := down.Download(ctx, req)
			return rc, err

		}
		response, err := RequestRangedHttp(ctx, link, r.Start, r.Length)
		if err != nil {
			if response == nil {
				return nil, fmt.Errorf("http request failure, err:%s", err)
			}
			return nil, err
		}
		if r.Start == 0 && (r.Length == -1 || r.Length == size) || response.StatusCode == http.StatusPartialContent ||
			checkContentRange(&response.Header, r.Start) {
			return response.Body, nil
		} else if response.StatusCode == http.StatusOK {
			log.Warnf("remote http server not supporting range request, expect low perfromace!")
			readCloser, err := net.GetRangedHttpReader(response.Body, r.Start, r.Length)
			if err != nil {
				return nil, err
			}
			return readCloser, nil
		}

		return response.Body, nil
	}
	resultRangeReadCloser := model.RangeReadCloser{RangeReader: rangeReaderFunc}
	return &resultRangeReadCloser, nil
}

func RequestRangedHttp(ctx context.Context, link *model.Link, offset, length int64) (*http.Response, error) {
	header := net.ProcessHeader(nil, link.Header)
	header = http_range.ApplyRangeToHttpHeader(http_range.Range{Start: offset, Length: length}, header)

	return net.RequestHttp(ctx, http.MethodGet, header, link.URL)
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

func CacheFullInTempFileAndUpdateProgress(stream model.FileStreamer, up model.UpdateProgress) (model.File, error) {
	if cache := stream.GetFile(); cache != nil {
		up(100)
		return cache, nil
	}
	tmpF, err := utils.CreateTempFile(&ReaderUpdatingProgress{
		Reader:         stream,
		UpdateProgress: up,
	}, stream.GetSize())
	if err == nil {
		stream.SetTmpFile(tmpF)
	}
	return tmpF, err
}

func CacheFullInTempFileAndWriter(stream model.FileStreamer, w io.Writer) (model.File, error) {
	if cache := stream.GetFile(); cache != nil {
		_, err := cache.Seek(0, io.SeekStart)
		if err == nil {
			_, err = utils.CopyWithBuffer(w, cache)
			if err == nil {
				_, err = cache.Seek(0, io.SeekStart)
			}
		}
		return cache, err
	}
	tmpF, err := utils.CreateTempFile(io.TeeReader(stream, w), stream.GetSize())
	if err == nil {
		stream.SetTmpFile(tmpF)
	}
	return tmpF, err
}

func CacheFullInTempFileAndHash(stream model.FileStreamer, hashType *utils.HashType, params ...any) (model.File, string, error) {
	h := hashType.NewFunc(params...)
	tmpF, err := CacheFullInTempFileAndWriter(stream, h)
	if err != nil {
		return nil, "", err
	}
	return tmpF, hex.EncodeToString(h.Sum(nil)), err
}

type StreamSectionReader struct {
	file    model.FileStreamer
	off     int64
	mu      sync.Mutex
	bufPool *sync.Pool
}

func NewStreamSectionReader(file model.FileStreamer, bufMaxLen int) (*StreamSectionReader, error) {
	ss := &StreamSectionReader{file: file}
	if file.GetFile() == nil {
		if bufMaxLen > conf.MaxBufferLimit {
			_, err := file.CacheFullInTempFile()
			if err != nil {
				return nil, err
			}
		} else {
			ss.bufPool = &sync.Pool{
				New: func() any {
					return make([]byte, bufMaxLen) // Two times of size in io package
				},
			}
		}
	}
	return ss, nil
}

func (ss *StreamSectionReader) GetSectionReader(off, length int64) (*SectionReader, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()
	var cache io.ReaderAt = ss.file.GetFile()
	var buf []byte
	if cache == nil {
		if off != ss.off {
			return nil, fmt.Errorf("stream not cached: request offset %d != current offset %d", off, ss.off)
		}
		tempBuf := ss.bufPool.Get().([]byte)
		buf = tempBuf[:length]
		n, err := io.ReadFull(ss.file, buf)
		if err != nil {
			return nil, err
		}
		if int64(n) != length {
			return nil, fmt.Errorf("stream read did not get all data, expect =%d ,actual =%d", length, n)
		}
		ss.off += int64(n)
		off = 0
		cache = bytes.NewReader(buf)
	}
	return &SectionReader{io.NewSectionReader(cache, off, length), buf}, nil
}

func (ss *StreamSectionReader) RecycleSectionReader(sr *SectionReader) {
	if sr != nil {
		ss.mu.Lock()
		defer ss.mu.Unlock()
		if sr.buf != nil {
			ss.bufPool.Put(sr.buf[0:cap(sr.buf)])
			sr.buf = nil
		}
		sr.ReadSeeker = nil
	}
}

// func (ss *StreamSectionReader) GetBytes(sr *SectionReader) ([]byte, error) {
// 	if sr != nil && ss.bufPool != nil {
// 		ss.mu.Lock()
// 		defer ss.mu.Unlock()
// 		buf := sr.buf
// 		if buf == nil {
// 			buf := ss.bufPool.Get().([]byte)
// 			n, err := io.ReadFull(sr, buf)
// 			if err == io.EOF && n > 0 {
// 				err = nil
// 			}
// 			if err != nil {
// 				return nil, err
// 			}
// 			sr.buf = buf[:n]
// 		}
// 		return sr.buf, nil
// 	}
// 	return nil, errors.New("SectionReader is nil")
// }

type SectionReader struct {
	io.ReadSeeker
	buf []byte
}
