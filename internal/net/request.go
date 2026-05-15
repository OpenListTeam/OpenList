package net

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	stdpath "path"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/aws/aws-sdk-go/aws/awsutil"
	log "github.com/sirupsen/logrus"
)

// DefaultDownloadPartSize is the default range of bytes to get at a time when
// using Download().
const DefaultDownloadPartSize = utils.MB * 8

// DefaultDownloadConcurrency is the default number of goroutines to spin up
// when using Download().
const DefaultDownloadConcurrency = 2

// DefaultPartBodyMaxRetries is the default number of retries to make when a part fails to download.
const DefaultPartBodyMaxRetries = 3

var DefaultConcurrencyLimit *ConcurrencyLimit

type Downloader struct {
	PartSize int

	// PartBodyMaxRetries is the number of retry attempts to make for failed part downloads.
	PartBodyMaxRetries int

	// The number of goroutines to spin up in parallel when sending parts.
	// If this is set to zero, the DefaultDownloadConcurrency value will be used.
	//
	// Concurrency of 1 will download the parts sequentially.
	Concurrency int

	//RequestParam        HttpRequestParams
	HttpClient HttpRequestFunc

	*ConcurrencyLimit
}
type HttpRequestFunc func(ctx context.Context, params *HttpRequestParams) (*http.Response, error)

func NewDownloader(options ...func(*Downloader)) *Downloader {
	d := &Downloader{ //允许不设置的选项
		PartBodyMaxRetries: DefaultPartBodyMaxRetries,
		ConcurrencyLimit:   DefaultConcurrencyLimit,
	}
	for _, option := range options {
		option(d)
	}
	return d
}

// Download The Downloader makes multi-thread http requests to remote URL, each chunk(except last one) has PartSize,
// cache some data, then return Reader with assembled data
// Supports range, do not support unknown FileSize, and will fail if FileSize is incorrect
// memory usage is at about Concurrency*PartSize, use this wisely
func (d Downloader) Download(ctx context.Context, p *HttpRequestParams) (readCloser io.ReadCloser, err error) {

	var finalP HttpRequestParams
	awsutil.Copy(&finalP, p)
	if finalP.Range.Length < 0 || finalP.Range.Start+finalP.Range.Length > finalP.Size {
		finalP.Range.Length = finalP.Size - finalP.Range.Start
	}
	impl := downloader{params: &finalP, cfg: d, ctx: ctx}

	// Ensures we don't need nil checks later on
	// 必需的选项
	if impl.cfg.Concurrency == 0 {
		impl.cfg.Concurrency = DefaultDownloadConcurrency
	}
	if impl.cfg.PartSize == 0 {
		impl.cfg.PartSize = DefaultDownloadPartSize
	}
	if conf.MinFreeMemory > 0 && impl.cfg.PartSize > int(conf.MaxBlockLimit) {
		impl.cfg.PartSize = int(conf.MaxBlockLimit)
	}
	if impl.cfg.HttpClient == nil {
		impl.cfg.HttpClient = DefaultHttpRequestFunc
	}

	return impl.download()
}

// downloader is the implementation structure used internally by Downloader.
type downloader struct {
	ctx    context.Context
	cancel context.CancelCauseFunc
	cfg    Downloader

	params  *HttpRequestParams //http request params
	chunkCh chan chunk         //chunk chanel

	//wg sync.WaitGroup
	mu sync.Mutex

	nextChunk int //next chunk id
	bufMap    map[int]*buffer.PipeBuffer
	written   int64 //total bytes of file downloaded from remote

	concurrency int //剩余的并发数，递减。到0时停止并发
	pos         int64
	maxPos      int64
	delayMu     sync.Mutex
	readingID   int64 // 正在被读取的id

	hc *mem.HybridCache
}

type ConcurrencyLimit struct {
	mu sync.Mutex

	Limit uint32
}

var ErrExceedMaxConcurrency = HttpStatusCodeError(http.StatusTooManyRequests)

func (l *ConcurrencyLimit) Acquire() error {
	if l == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.Limit == 0 {
		return ErrExceedMaxConcurrency
	}
	l.Limit--
	return nil
}
func (l *ConcurrencyLimit) Release() {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.Limit++
	l.mu.Unlock()
}

// download performs the implementation of the object download across ranged GETs.
func (d *downloader) download() (io.ReadCloser, error) {
	if err := d.cfg.ConcurrencyLimit.Acquire(); err != nil {
		return nil, err
	}

	maxPart := 1
	if d.params.Range.Length > int64(d.cfg.PartSize) {
		maxPart = int((d.params.Range.Length + int64(d.cfg.PartSize) - 1) / int64(d.cfg.PartSize))
	}
	if maxPart < d.cfg.Concurrency {
		d.cfg.Concurrency = maxPart
	}
	log.Debugf("cfgConcurrency:%d", d.cfg.Concurrency)

	if maxPart == 1 {
		resp, err := d.cfg.HttpClient(d.ctx, d.params)
		if err != nil {
			d.cfg.ConcurrencyLimit.Release()
			return nil, err
		}
		closeFunc := resp.Body.Close
		resp.Body = utils.NewReadCloser(resp.Body, func() error {
			d.mu.Lock()
			defer d.mu.Unlock()
			var err error
			if closeFunc != nil {
				d.cfg.ConcurrencyLimit.Release()
				err = closeFunc()
				closeFunc = nil
			}
			return err
		})
		return resp.Body, nil
	}
	d.ctx, d.cancel = context.WithCancelCause(d.ctx)

	// workers
	d.chunkCh = make(chan chunk, d.cfg.Concurrency)

	d.pos = d.params.Range.Start
	d.maxPos = d.params.Range.Start + d.params.Range.Length
	d.concurrency = d.cfg.Concurrency

	var err error
	if d.params.Range.Length > int64(conf.CacheThreshold) {
		d.hc, err = mem.NewHybridCache(uint64(d.cfg.PartSize), uint64(d.params.Range.Length))
	}
	if err == nil {
		d.bufMap = make(map[int]*buffer.PipeBuffer, d.cfg.Concurrency)
		err = d.sendChunkTask(true)
	}
	if err != nil {
		d.cancel(err)
		d.cfg.ConcurrencyLimit.Release()
		return nil, d.interrupt()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	return &multiReadCloser{d: d, curBuf: d.popBuf(0), maxPos: maxPart}, nil
}

func (d *downloader) sendChunkTask(newConcurrency bool) (err error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.pos >= d.maxPos {
		return nil
	}
	if newConcurrency {
		if d.concurrency <= 0 {
			return nil
		}
		if d.nextChunk > 0 { // 第一个不检查，因为已经检查过了
			if err := d.cfg.ConcurrencyLimit.Acquire(); err != nil {
				return err
			}
			defer func() {
				if err != nil {
					d.cfg.ConcurrencyLimit.Release()
				}
			}()
		}
	}

	br := d.bufMap[d.nextChunk]
	if br == nil {
		var b buffer.Block
		if d.hc != nil {
			b, err = d.hc.NextBlock()
			if err != nil {
				return err
			}
		} else {
			defer func() {
				if r := recover(); r != nil {
					err = fmt.Errorf("panic in creating new byte block: %v", r)
				}
			}()
			b = buffer.NewByteBlock(make([]byte, d.cfg.PartSize))
		}
		br = buffer.NewPipeBuffer(d.ctx, b)
		d.bufMap[d.nextChunk] = br
	}

	finalSize := int64(d.cfg.PartSize)
	switch d.nextChunk {
	case 0:
		// 最小分片在前面有助视频播放？
		firstSize := d.params.Range.Length % finalSize
		if firstSize > 0 {
			minSize := finalSize / 2
			// 最小分片太小就调整到一半
			finalSize = max(firstSize, minSize)
		}
	case 1:
		firstSize := d.params.Range.Length % finalSize
		minSize := finalSize / 2
		if firstSize > 0 && firstSize < minSize {
			finalSize += firstSize - minSize
		}
	}
	err = br.Reset(int(finalSize))
	if err != nil {
		return err // 分片算法错误或者下载中断
	}
	if newConcurrency {
		go d.downloadPart()
		d.concurrency--
	}
	ch := chunk{
		start: d.pos,
		size:  finalSize,
		id:    d.nextChunk,
		buf:   br,

		newConcurrency: newConcurrency,
	}
	d.pos += finalSize
	d.nextChunk++
	select {
	case <-d.ctx.Done():
		return context.Cause(d.ctx)
	case d.chunkCh <- ch:
		return nil
	}
}

// when the final reader Close, we interrupt
func (d *downloader) interrupt() error {
	err := context.Cause(d.ctx)
	if err == nil {
		if atomic.LoadInt64(&d.written) != d.params.Range.Length {
			err = fmt.Errorf("interrupted")
		}
	} else if errors.Is(err, context.Canceled) {
		err = nil
	}
	d.cancel(err)
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.bufMap != nil {
		for _, buf := range d.bufMap {
			_ = buf.Close()
		}
		d.bufMap = nil
	}
	if d.hc != nil {
		_ = d.hc.Close()
		d.hc = nil
	}
	if d.maxPos != 0 {
		d.maxPos = 0
		close(d.chunkCh)
		if d.concurrency > 0 {
			d.concurrency = -d.concurrency
		}
		log.Debugf("maxConcurrency:%d", d.cfg.Concurrency+d.concurrency)
	}
	return err
}
func (d *downloader) popBuf(id int) *buffer.PipeBuffer {
	br := d.bufMap[id]
	if br != nil {
		delete(d.bufMap, id)
		d.bufMap[-1] = br // -1 保存最后一次取出的buf用来关闭
	}
	return br
}

func (d *downloader) finishBuf(nextId int, prev *buffer.PipeBuffer) (next *buffer.PipeBuffer) {
	atomic.StoreInt64(&d.readingID, int64(nextId))

	d.mu.Lock()
	shouldSendTask := d.bufMap[d.nextChunk] == nil
	if shouldSendTask {
		d.bufMap[d.nextChunk] = prev
	}
	d.mu.Unlock()

	if shouldSendTask {
		_ = d.sendChunkTask(false)
	} else {
		_ = prev.Close()
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	return d.popBuf(nextId)
}

// downloadPart is an individual goroutine worker reading from the ch channel
// and performing Http request on the data with a given byte range.
func (d *downloader) downloadPart() {
	defer d.cfg.ConcurrencyLimit.Release()
	for {
		select {
		case <-d.ctx.Done():
			return
		case c, ok := <-d.chunkCh:
			if !ok {
				return
			}
			if !d.downloadChunk(&c) {
				return
			}
		}
	}
}

// downloadChunk downloads the chunk
func (d *downloader) downloadChunk(ch *chunk) bool {
	log.Debugf("start chunk_%d, %+v", ch.id, ch)
	params := d.getParamsFromChunk(ch)
	var err error
	for retry := 0; retry <= d.cfg.PartBodyMaxRetries; retry++ {
		var n int64
		n, err = d.tryDownloadChunk(params, ch)
		if err == nil {
			d.incrWritten(n)
			log.Debugf("chunk_%d downloaded", ch.id)
			return true
		}
		if d.ctx.Err() != nil {
			return false
		}
		// Check if the returned error is an errNeedRetry.
		// If this occurs we unwrap the err to set the underlying error
		// and attempt any remaining retries.
		if e, ok := err.(*errNeedRetry); ok {
			err = e.Unwrap()
			if n > 0 {
				// 测试：下载时 断开openlist向云盘发起的下载连接
				// 校验：下载完后校验文件哈希值 一致
				d.incrWritten(n)
				ch.start += n
				ch.size -= n
				params.Range.Start = ch.start
				params.Range.Length = ch.size
			}
			log.Warnf("err chunk_%d, object part download error %s, retrying attempt %d. %v",
				ch.id, params.URL, retry, err)
		} else if err == errInfiniteRetry {
			retry--
		} else if err == errCancelConcurrency {
			return false // 取消一个的并发
		} else {
			break
		}
	}
	if err != nil {
		d.cancel(err) // 取消所有的并发
	}
	return false
}

func (d *downloader) delay(ti time.Duration) bool {
	t := time.NewTimer(ti)
	select {
	case <-d.ctx.Done():
		t.Stop()
		return false
	case <-t.C:
		return true
	}
}

var errCancelConcurrency = errors.New("")
var errInfiniteRetry = errors.New("")

func (d *downloader) tryDownloadChunk(params *HttpRequestParams, ch *chunk) (int64, error) {
	resp, err := d.cfg.HttpClient(d.ctx, params)
	if err != nil {
		statusCode, ok := errs.UnwrapOrSelf(err).(HttpStatusCodeError)
		if !ok {
			return 0, err
		}
		if statusCode == http.StatusRequestedRangeNotSatisfiable {
			return 0, err
		}
		if ch.id == 0 { //第1个任务 有限的重试，超过重试就会结束请求
			switch statusCode {
			default:
				return 0, err
			case http.StatusTooManyRequests:
			case http.StatusBadGateway:
			case http.StatusServiceUnavailable:
			case http.StatusGatewayTimeout:
			}
			if !d.delay(time.Millisecond * time.Duration(rand.Uint32N(300)+200)) {
				return 0, errCancelConcurrency
			}
			return 0, &errNeedRetry{err}
		}

		// 来到这 说明第1个分片下载 连接成功了
		// 后续分片下载出错都当超载处理
		log.Debugf("err chunk_%d, try downloading:%v", ch.id, err)

		d.mu.Lock()
		isCancelConcurrency := ch.newConcurrency
		if d.concurrency > 0 { // 取消剩余的并发任务
			// 用于计算实际的并发数
			d.concurrency = -d.concurrency
			isCancelConcurrency = true
		}
		if isCancelConcurrency {
			d.concurrency--
			d.mu.Unlock()
			select {
			case <-d.ctx.Done():
				return 0, errCancelConcurrency
			case d.chunkCh <- *ch:
				return 0, errCancelConcurrency
			}
		}
		d.mu.Unlock()
		if int64(ch.id) != atomic.LoadInt64(&d.readingID) { //正在被读取的优先重试
			d.delayMu.Lock()
			defer d.delayMu.Unlock()
			if !d.delay(time.Millisecond * time.Duration(rand.Uint32N(300)+200)) {
				return 0, errCancelConcurrency
			}
		}
		return 0, errInfiniteRetry
	}

	defer resp.Body.Close()
	//only check file size on the first task
	if ch.id == 0 {
		err = d.checkTotalBytes(resp)
		if err != nil {
			return 0, err
		}
	}
	_ = d.sendChunkTask(true)
	n, err := utils.CopyWithBuffer(ch.buf, resp.Body)

	if err != nil {
		return n, &errNeedRetry{err}
	}
	if n != ch.size {
		err = fmt.Errorf("chunk download size incorrect, expected=%d, got=%d", ch.size, n)
		return n, &errNeedRetry{err}
	}

	return n, nil
}
func (d *downloader) getParamsFromChunk(ch *chunk) *HttpRequestParams {
	var params HttpRequestParams
	awsutil.Copy(&params, d.params)

	// Get the getBuf byte range of data
	params.Range = http_range.Range{Start: ch.start, Length: ch.size}
	return &params
}

func (d *downloader) checkTotalBytes(resp *http.Response) error {
	var err error
	totalBytes := int64(-1)
	contentRange := resp.Header.Get("Content-Range")
	if len(contentRange) == 0 {
		// ContentRange is nil when the full file contents is provided, and
		// is not chunked. Use ContentLength instead.
		if resp.ContentLength > 0 {
			totalBytes = resp.ContentLength
		}
	} else {

		// Checking for whether a numbered total exists
		// If one does not exist, we will assume the total to be -1, undefined,
		// and sequentially download each chunk until hitting a 416 error

		totalStr := stdpath.Base(contentRange)
		if totalStr != "*" {
			var total int64
			if total, err = strconv.ParseInt(totalStr, 10, 64); err != nil {
				err = fmt.Errorf("failed extracting file size: %s", totalStr)
			} else {
				totalBytes = total
			}
		} else {
			err = fmt.Errorf("file size unknown: %s", contentRange)
		}

	}
	if totalBytes != d.params.Size && err == nil {
		err = fmt.Errorf("expect file size=%d unmatch remote report size=%d, need refresh cache", d.params.Size, totalBytes)
	}
	return err

}

func (d *downloader) incrWritten(n int64) {
	atomic.AddInt64(&d.written, n)
}

// Chunk represents a single chunk of data to write by the worker routine.
// This structure also implements an io.SectionReader style interface for
// io.WriterAt, effectively making it an io.SectionWriter (which does not
// exist).
type chunk struct {
	start int64
	size  int64
	buf   *buffer.PipeBuffer
	id    int

	newConcurrency bool
}

func DefaultHttpRequestFunc(ctx context.Context, params *HttpRequestParams) (*http.Response, error) {
	header := http_range.ApplyRangeToHttpHeader(params.Range, params.HeaderRef)
	return RequestHttp(ctx, "GET", header, params.URL)
}

func GetRangeReaderHttpRequestFunc(rangeReader model.RangeReaderIF) HttpRequestFunc {
	return func(ctx context.Context, params *HttpRequestParams) (*http.Response, error) {
		rc, err := rangeReader.RangeRead(ctx, params.Range)
		if err != nil {
			return nil, err
		}

		return &http.Response{
			StatusCode: http.StatusPartialContent,
			Status:     http.StatusText(http.StatusPartialContent),
			Body:       rc,
			Header: http.Header{
				"Content-Range": {params.Range.ContentRange(params.Size)},
			},
			ContentLength: params.Range.Length,
		}, nil
	}
}

type HttpRequestParams struct {
	URL string
	//only want data within this range
	Range     http_range.Range
	HeaderRef http.Header
	//total file size
	Size int64
}
type errNeedRetry struct {
	error
}

func (e *errNeedRetry) Unwrap() error {
	return e.error
}

type multiReadCloser struct {
	pos    int //current reader position, start from 0
	maxPos int
	curBuf *buffer.PipeBuffer
	d      *downloader
}

func (mr *multiReadCloser) Read(p []byte) (n int, err error) {
	if mr.curBuf == nil {
		return 0, io.EOF
	}
	n, err = mr.curBuf.Read(p)
	// log.Debugf("read_%d read current buffer, n=%d ,err=%+v", mr.rPos, n, err)
	if err == io.EOF {
		log.Debugf("read_%d finished current buffer", mr.pos)

		mr.pos++
		if mr.pos >= mr.maxPos {
			return n, io.EOF
		}
		mr.curBuf = mr.d.finishBuf(mr.pos, mr.curBuf)
		return n, nil
	}
	return n, err
}

func (mr *multiReadCloser) Close() error {
	return mr.d.interrupt()
}
