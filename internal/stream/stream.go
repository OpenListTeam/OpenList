package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/mem"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"go4.org/readerutil"
)

type FileStream struct {
	Ctx context.Context
	model.Obj
	io.Reader
	Mimetype          string
	WebPutAsTask      bool
	ForceStreamUpload bool
	Exist             model.Obj //the file existed in the destination, we can reuse some info since we wil overwrite it
	utils.Closers
	size      int64
	oriReader io.Reader // the original reader, used for caching
	hc        *mem.HybridCache
	peek      buffer.SizedReadAtSeeker
}

func (f *FileStream) GetSize() int64 {
	if f.size > 0 {
		return f.size
	}
	return f.Obj.GetSize()
}

func (f *FileStream) GetMimetype() string {
	return f.Mimetype
}

func (f *FileStream) NeedStore() bool {
	return f.WebPutAsTask
}

func (f *FileStream) IsForceStreamUpload() bool {
	return f.ForceStreamUpload
}

func (f *FileStream) GetExist() model.Obj {
	return f.Exist
}
func (f *FileStream) SetExist(obj model.Obj) {
	f.Exist = obj
}

// CacheFullAndWriter save all data into tmpFile or memory.
// It's not thread-safe!
func (f *FileStream) CacheFullAndWriter(up *model.UpdateProgress, writer io.Writer) (model.File, error) {
	if cache := f.GetFile(); cache != nil {
		_, err := cache.Seek(0, io.SeekStart)
		if err != nil {
			return nil, err
		}
		if writer == nil {
			return cache, nil
		}
		reader := f.Reader
		if up != nil {
			cacheProgress := model.UpdateProgressWithRange(*up, 0, 50)
			*up = model.UpdateProgressWithRange(*up, 50, 100)
			reader = &ReaderUpdatingProgress{
				Reader: &SimpleReaderWithSize{
					Reader: reader,
					Size:   f.GetSize(),
				},
				UpdateProgress: cacheProgress,
			}
		}
		_, err = utils.CopyWithBuffer(writer, reader)
		if err == nil {
			_, err = cache.Seek(0, io.SeekStart)
		}
		if err != nil {
			return nil, err
		}
		return cache, nil
	}

	reader := f.Reader
	if f.peek != nil {
		f.peek.Seek(0, io.SeekStart)
		if writer != nil {
			_, err := utils.CopyWithBuffer(writer, f.peek)
			if err != nil {
				return nil, err
			}
			f.peek.Seek(0, io.SeekStart)
		}
		reader = f.oriReader
	}
	if writer != nil {
		reader = io.TeeReader(reader, writer)
	}

	// 如果文件大小未知，直接缓存到磁盘
	if f.GetSize() < 0 {
		// 检查是否有数据
		buf := []byte{0}
		n, err := io.ReadFull(reader, buf)
		br := bytes.NewReader(buf[:n])
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			f.size = br.Size()
			f.Reader = br
			return br, nil
		} else if err != nil {
			return nil, err
		}
		tmpF, err := utils.CreateTempFile(io.MultiReader(br, reader), 0)
		if err != nil {
			return nil, err
		}
		f.Add(utils.CloseFunc(func() error {
			return errors.Join(tmpF.Close(), os.RemoveAll(tmpF.Name()))
		}))
		stat, err := tmpF.Stat()
		if err != nil {
			return nil, err
		}
		f.size = stat.Size()
		f.Reader = tmpF
		return tmpF, nil
	}

	if up != nil {
		cacheProgress := model.UpdateProgressWithRange(*up, 0, 50)
		*up = model.UpdateProgressWithRange(*up, 50, 100)
		size := f.GetSize()
		if f.peek != nil {
			peekSize := f.peek.Size()
			// cacheProgress(float64(peekSize) / float64(size) * 100)
			size -= peekSize
		}
		reader = &ReaderUpdatingProgress{
			Reader: &SimpleReaderWithSize{
				Reader: reader,
				Size:   size,
			},
			UpdateProgress: cacheProgress,
		}
	}

	if f.oriReader != nil {
		f.oriReader = reader
	} else {
		f.Reader = reader
	}
	return f.cache(f.GetSize())
}

func (f *FileStream) GetFile() model.File {
	if file, ok := f.Reader.(model.File); ok {
		return file
	}
	return nil
}

// 从流读取指定范围的一块数据,并且不消耗流。
// 当读取的边界超过内部设置大小后会缓存整个流。
// 流未缓存时线程不完全
func (f *FileStream) RangeRead(httpRange http_range.Range) (io.Reader, error) {
	if httpRange.Length < 0 || httpRange.Start+httpRange.Length > f.GetSize() {
		httpRange.Length = f.GetSize() - httpRange.Start
	}
	if f.GetFile() != nil {
		return io.NewSectionReader(f.GetFile(), httpRange.Start, httpRange.Length), nil
	}

	cache, err := f.cache(httpRange.Start + httpRange.Length)
	if err != nil {
		return nil, err
	}

	return io.NewSectionReader(cache, httpRange.Start, httpRange.Length), nil
}

// *旧笔记
// 使用bytes.Buffer作为io.CopyBuffer的写入对象，CopyBuffer会调用Buffer.ReadFrom
// 即使被写入的数据量与Buffer.Cap一致，Buffer也会扩大

// 确保指定大小的数据被缓存
func (f *FileStream) cache(maxCacheSize int64) (model.File, error) {
	if f.peek == nil {
		f.oriReader = f.Reader
		if f.GetSize() > int64(conf.CacheThreshold) {
			blockSize := min(f.GetSize(), int64(conf.MaxBlockLimit))
			hc, err := mem.NewHybridCache(uint64(blockSize), uint64(f.GetSize()))
			if err != nil {
				return nil, err
			}
			f.hc = hc
			f.peek = buffer.NewDynamicReadAtSeeker(hc)
			f.Reader = io.MultiReader(f.peek, f.oriReader)
			f.Add(hc)
		} else {
			br := &buffer.Reader{}
			f.peek = br
			f.Reader = io.MultiReader(br, f.oriReader)
			f.Add(br)
		}
	}
	cacheSize := maxCacheSize - f.peek.Size()
	if cacheSize <= 0 {
		return f.peek, nil
	}
	if f.hc != nil {
		written, err := f.hc.CopyFromN(f.oriReader, cacheSize)
		if written != cacheSize {
			f.hc.RollbackBlockWithSize(uint64(cacheSize - written))
			return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", cacheSize, written, err)
		}
	} else {
		buf := make([]byte, cacheSize)
		n, err := io.ReadFull(f.oriReader, buf)
		if n > 0 {
			f.peek.(*buffer.Reader).Append(buf[:n])
		}
		if n != len(buf) {
			return nil, fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", len(buf), n, err)
		}
	}
	if f.peek.Size() >= f.GetSize() {
		f.Reader = f.peek
	}
	return f.peek, nil
}

var _ model.FileStreamer = (*SeekableStream)(nil)
var _ model.FileStreamer = (*FileStream)(nil)

type SeekableStream struct {
	*FileStream
	// should have one of belows to support rangeRead
	rangeReader model.RangeReaderIF
}

// NewSeekableStream create a SeekableStream from FileStream and Link
// if FileStream.Reader is not nil, use it directly
// else create RangeReader from Link
func NewSeekableStream(fs *FileStream, link *model.Link) (*SeekableStream, error) {
	if len(fs.Mimetype) == 0 {
		fs.Mimetype = utils.GetMimeType(fs.Obj.GetName())
	}

	if fs.Reader != nil {
		fs.Add(link)
		return &SeekableStream{FileStream: fs}, nil
	}

	if link != nil {
		size := link.ContentLength
		if size <= 0 {
			size = fs.GetSize()
		}
		rr, err := GetRangeReaderFromLink(size, link)
		if err != nil {
			return nil, err
		}
		if _, ok := rr.(*model.FileRangeReader); ok {
			var rc io.ReadCloser
			rc, err = rr.RangeRead(fs.Ctx, http_range.Range{Length: -1})
			if err != nil {
				return nil, err
			}
			fs.Reader = rc
			fs.Add(rc)
		}
		fs.size = size
		fs.Add(link)
		return &SeekableStream{FileStream: fs, rangeReader: rr}, nil
	}
	return nil, fmt.Errorf("illegal seekableStream")
}

// 如果使用缓存或者rangeReader读取指定范围的数据，是线程安全的
// 其他特性继承自FileStream.RangeRead
func (ss *SeekableStream) RangeRead(httpRange http_range.Range) (io.Reader, error) {
	if ss.GetFile() == nil && ss.rangeReader != nil {
		rc, err := ss.rangeReader.RangeRead(ss.Ctx, httpRange)
		if err != nil {
			return nil, err
		}
		ss.Add(rc)
		return rc, nil
	}
	return ss.FileStream.RangeRead(httpRange)
}

// only provide Reader as full stream when it's demanded. in rapid-upload, we can skip this to save memory
func (ss *SeekableStream) Read(p []byte) (n int, err error) {
	if err := ss.generateReader(); err != nil {
		return 0, err
	}
	return ss.FileStream.Read(p)
}

func (ss *SeekableStream) generateReader() error {
	if ss.Reader == nil {
		if ss.rangeReader == nil {
			return fmt.Errorf("illegal seekableStream")
		}
		rc, err := ss.rangeReader.RangeRead(ss.Ctx, http_range.Range{Length: -1})
		if err != nil {
			return err
		}
		ss.Add(rc)
		ss.Reader = rc
	}
	return nil
}

func (ss *SeekableStream) CacheFullAndWriter(up *model.UpdateProgress, writer io.Writer) (model.File, error) {
	if err := ss.generateReader(); err != nil {
		return nil, err
	}
	return ss.FileStream.CacheFullAndWriter(up, writer)
}

type ReaderWithSize interface {
	io.Reader
	GetSize() int64
}

type SimpleReaderWithSize struct {
	io.Reader
	Size int64
}

func (r *SimpleReaderWithSize) GetSize() int64 {
	return r.Size
}

func (r *SimpleReaderWithSize) Close() error {
	if c, ok := r.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

type ReaderUpdatingProgress struct {
	Reader ReaderWithSize
	model.UpdateProgress
	offset int
}

func (r *ReaderUpdatingProgress) Read(p []byte) (n int, err error) {
	n, err = r.Reader.Read(p)
	r.offset += n
	r.UpdateProgress(math.Min(100.0, float64(r.offset)/float64(r.Reader.GetSize())*100.0))
	return n, err
}

func (r *ReaderUpdatingProgress) Close() error {
	if c, ok := r.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

type RangeReadReadAtSeeker struct {
	ss        *SeekableStream
	masterOff int64
	readerMap sync.Map
	headCache *headCache
}

type headCache struct {
	reader io.Reader
	bufs   [][]byte
}

func (c *headCache) head(p []byte) (int, error) {
	n := 0
	for _, buf := range c.bufs {
		n += copy(p[n:], buf)
		if n == len(p) {
			return n, nil
		}
	}
	nn, err := io.ReadFull(c.reader, p[n:])
	if nn > 0 {
		buf := make([]byte, nn)
		copy(buf, p[n:])
		c.bufs = append(c.bufs, buf)
		n += nn
		if err == io.ErrUnexpectedEOF {
			err = io.EOF
		}
	}
	return n, err
}

func (r *headCache) Close() error {
	clear(r.bufs)
	r.bufs = nil
	return nil
}

func (r *RangeReadReadAtSeeker) InitHeadCache() {
	if r.masterOff == 0 {
		value, _ := r.readerMap.LoadAndDelete(int64(0))
		r.headCache = &headCache{reader: value.(io.Reader)}
		r.ss.Closers.Add(r.headCache)
	}
}

func NewReadAtSeeker(ss *SeekableStream, offset int64, forceRange ...bool) (model.File, error) {
	if cache := ss.GetFile(); cache != nil {
		_, err := cache.Seek(offset, io.SeekStart)
		if err != nil {
			return nil, err
		}
		return cache, nil
	}
	r := &RangeReadReadAtSeeker{
		ss:        ss,
		masterOff: offset,
	}
	if offset != 0 || utils.IsBool(forceRange...) {
		if offset < 0 || offset > ss.GetSize() {
			return nil, errors.New("offset out of range")
		}
		reader, err := r.getReaderAtOffset(offset)
		if err != nil {
			return nil, err
		}
		r.readerMap.Store(int64(offset), reader)
	} else {
		r.readerMap.Store(int64(offset), ss)
	}
	return r, nil
}

func NewMultiReaderAt(ss []*SeekableStream) (readerutil.SizeReaderAt, error) {
	readers := make([]readerutil.SizeReaderAt, 0, len(ss))
	for _, s := range ss {
		ra, err := NewReadAtSeeker(s, 0)
		if err != nil {
			return nil, err
		}
		readers = append(readers, io.NewSectionReader(ra, 0, s.GetSize()))
	}
	return readerutil.NewMultiReaderAt(readers...), nil
}

func (r *RangeReadReadAtSeeker) getReaderAtOffset(off int64) (io.Reader, error) {
	for {
		var cur int64 = -1
		r.readerMap.Range(func(key, value any) bool {
			k := key.(int64)
			if off == k {
				cur = k
				return false
			}
			if off > k && off-k <= 4*utils.MB && k > cur {
				cur = k
			}
			return true
		})
		if cur < 0 {
			break
		}
		v, ok := r.readerMap.LoadAndDelete(int64(cur))
		if !ok {
			continue
		}
		rr := v.(io.Reader)
		if off == int64(cur) {
			// logrus.Debugf("getReaderAtOffset match_%d", off)
			return rr, nil
		}
		n, _ := utils.CopyWithBufferN(io.Discard, rr, off-cur)
		cur += n
		if cur == off {
			// logrus.Debugf("getReaderAtOffset old_%d", off)
			return rr, nil
		}
		break
	}

	// logrus.Debugf("getReaderAtOffset new_%d", off)
	reader, err := r.ss.RangeRead(http_range.Range{Start: off, Length: -1})
	if err != nil {
		return nil, err
	}
	return reader, nil
}

func (r *RangeReadReadAtSeeker) ReadAt(p []byte, off int64) (n int, err error) {
	if off < 0 || off >= r.ss.GetSize() {
		return 0, io.EOF
	}
	if off == 0 && r.headCache != nil {
		return r.headCache.head(p)
	}
	var rr io.Reader
	rr, err = r.getReaderAtOffset(off)
	if err != nil {
		return 0, err
	}
	n, err = io.ReadFull(rr, p)
	if n > 0 {
		off += int64(n)
		switch err {
		case nil:
			r.readerMap.Store(int64(off), rr)
		case io.ErrUnexpectedEOF:
			err = io.EOF
		}
	}
	return n, err
}

func (r *RangeReadReadAtSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		offset += r.masterOff
	case io.SeekEnd:
		offset += r.ss.GetSize()
	default:
		return 0, errors.New("Seek: invalid whence")
	}
	if offset < 0 || offset > r.ss.GetSize() {
		return 0, errors.New("Seek: invalid offset")
	}
	r.masterOff = offset
	return offset, nil
}

func (r *RangeReadReadAtSeeker) Read(p []byte) (n int, err error) {
	n, err = r.ReadAt(p, r.masterOff)
	if n > 0 {
		r.masterOff += int64(n)
	}
	return n, err
}
