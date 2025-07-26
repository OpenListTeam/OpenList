package stream

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/sirupsen/logrus"
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
	tmpFile  *os.File //if present, tmpFile has full content, it will be deleted at last
	peekBuff *bytes.Reader
}

func (f *FileStream) GetSize() int64 {
	if f.tmpFile != nil {
		info, err := f.tmpFile.Stat()
		if err == nil {
			return info.Size()
		}
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

func (f *FileStream) Close() error {
	var err1, err2 error

	err1 = f.Closers.Close()
	if errors.Is(err1, os.ErrClosed) {
		err1 = nil
	}
	if f.tmpFile != nil {
		err2 = os.RemoveAll(f.tmpFile.Name())
		if err2 != nil {
			err2 = errs.NewErr(err2, "failed to remove tmpFile [%s]", f.tmpFile.Name())
		} else {
			f.tmpFile = nil
		}
	}

	return errors.Join(err1, err2)
}

func (f *FileStream) GetExist() model.Obj {
	return f.Exist
}
func (f *FileStream) SetExist(obj model.Obj) {
	f.Exist = obj
}

// CacheFullInTempFile save all data into tmpFile. Not recommended since it wears disk,
// and can't start upload until the file is written. It's not thread-safe!
func (f *FileStream) CacheFullInTempFile() (model.File, error) {
	if file := f.GetFile(); file != nil {
		return file, nil
	}
	tmpF, err := utils.CreateTempFile(f.Reader, f.GetSize())
	if err != nil {
		return nil, err
	}
	f.Add(tmpF)
	f.tmpFile = tmpF
	f.Reader = tmpF
	return tmpF, nil
}

func (f *FileStream) GetFile() model.File {
	if f.tmpFile != nil {
		return f.tmpFile
	}
	if file, ok := f.Reader.(model.File); ok {
		return file
	}
	return nil
}

// RangeRead have to cache all data first since only Reader is provided.
// also support a peeking RangeRead at very start, but won't buffer more than conf.MaxBufferLimit data in memory
func (f *FileStream) RangeRead(httpRange http_range.Range) (io.Reader, error) {
	if httpRange.Length < 0 || httpRange.Start+httpRange.Length > f.GetSize() {
		httpRange.Length = f.GetSize() - httpRange.Start
	}
	var cache io.ReaderAt = f.GetFile()
	if cache != nil {
		return io.NewSectionReader(cache, httpRange.Start, httpRange.Length), nil
	}

	size := httpRange.Start + httpRange.Length
	if f.peekBuff != nil && size <= int64(f.peekBuff.Len()) {
		return io.NewSectionReader(f.peekBuff, httpRange.Start, httpRange.Length), nil
	}
	if size <= int64(conf.MaxBufferLimit) {
		bufSize := min(size, f.GetSize())
		// 使用bytes.Buffer作为io.CopyBuffer的写入对象，CopyBuffer会调用Buffer.ReadFrom
		// 即使被写入的数据量与Buffer.Cap一致，Buffer也会扩大
		buf := make([]byte, bufSize)
		n, err := io.ReadFull(f.Reader, buf)
		if err != nil {
			return nil, err
		}
		if n != int(bufSize) {
			return nil, fmt.Errorf("stream RangeRead did not get all data in peek, expect =%d ,actual =%d", bufSize, n)
		}
		f.peekBuff = bytes.NewReader(buf)
		f.Reader = io.MultiReader(f.peekBuff, f.Reader)
		cache = f.peekBuff
	} else {
		var err error
		cache, err = f.CacheFullInTempFile()
		if err != nil {
			return nil, err
		}
	}
	return io.NewSectionReader(cache, httpRange.Start, httpRange.Length), nil
}

var _ model.FileStreamer = (*SeekableStream)(nil)
var _ model.FileStreamer = (*FileStream)(nil)

//var _ seekableStream = (*FileStream)(nil)

// for most internal stream, which is either RangeReadCloser or MFile
// Any functionality implemented based on SeekableStream should implement a Close method,
// whose only purpose is to close the SeekableStream object. If such functionality has
// additional resources that need to be closed, they should be added to the Closer property of
// the SeekableStream object and be closed together when the SeekableStream object is closed.
type SeekableStream struct {
	*FileStream
	// should have one of belows to support rangeRead
	rangeReadCloser model.RangeReadCloserIF
	size            int64
}

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
			fs.Reader, err = rr.RangeRead(fs.Ctx, http_range.Range{Length: -1})
			if err != nil {
				return nil, err
			}
			fs.Add(link)
			return &SeekableStream{FileStream: fs, size: size}, nil
		}
		rrc := &model.RangeReadCloser{
			RangeReader: rr,
		}
		fs.Add(link)
		fs.Add(rrc)
		return &SeekableStream{FileStream: fs, rangeReadCloser: rrc, size: size}, nil
	}
	return nil, fmt.Errorf("illegal seekableStream")
}

func (ss *SeekableStream) GetSize() int64 {
	if ss.size > 0 {
		return ss.size
	}
	return ss.FileStream.GetSize()
}

//func (ss *SeekableStream) Peek(length int) {
//
//}

// RangeRead is not thread-safe, pls use it in single thread only.
func (ss *SeekableStream) RangeRead(httpRange http_range.Range) (io.Reader, error) {
	if ss.tmpFile == nil && ss.rangeReadCloser != nil {
		rc, err := ss.rangeReadCloser.RangeRead(ss.Ctx, httpRange)
		if err != nil {
			return nil, err
		}
		return rc, nil
	}
	return ss.FileStream.RangeRead(httpRange)
}

//func (f *FileStream) GetReader() io.Reader {
//	return f.Reader
//}

// only provide Reader as full stream when it's demanded. in rapid-upload, we can skip this to save memory
func (ss *SeekableStream) Read(p []byte) (n int, err error) {
	if ss.Reader == nil {
		if ss.rangeReadCloser == nil {
			return 0, fmt.Errorf("illegal seekableStream")
		}
		rc, err := ss.rangeReadCloser.RangeRead(ss.Ctx, http_range.Range{Length: -1})
		if err != nil {
			return 0, nil
		}
		ss.Reader = rc
	}
	return ss.Reader.Read(p)
}

func (ss *SeekableStream) CacheFullInTempFile() (model.File, error) {
	if file := ss.GetFile(); file != nil {
		return file, nil
	}
	tmpF, err := utils.CreateTempFile(ss, ss.GetSize())
	if err != nil {
		return nil, err
	}
	ss.Add(tmpF)
	ss.tmpFile = tmpF
	ss.Reader = tmpF
	return tmpF, nil
}

func (f *FileStream) SetTmpFile(r *os.File) {
	f.Add(r)
	f.tmpFile = r
	f.Reader = r
}

type ReaderWithSize interface {
	io.ReadCloser
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
	return r.Reader.Close()
}

type readerCur struct {
	reader io.Reader
	cur    int64
}

type RangeReadReadAtSeeker struct {
	ss        *SeekableStream
	masterOff int64
	readers   []*readerCur
	headCache *headCache
}

type headCache struct {
	*readerCur
	bufs [][]byte
}

func (c *headCache) read(p []byte) (n int, err error) {
	pL := len(p)
	logrus.Debugf("headCache read_%d", pL)
	if c.cur < int64(pL) {
		bufL := int64(pL) - c.cur
		buf := make([]byte, bufL)
		lr := io.LimitReader(c.reader, bufL)
		off := 0
		for c.cur < int64(pL) {
			n, err = lr.Read(buf[off:])
			off += n
			c.cur += int64(n)
			if err == io.EOF && off == int(bufL) {
				err = nil
			}
			if err != nil {
				break
			}
		}
		c.bufs = append(c.bufs, buf)
	}
	n = 0
	if c.cur >= int64(pL) {
		for i := 0; n < pL; i++ {
			buf := c.bufs[i]
			r := len(buf)
			if n+r > pL {
				r = pL - n
			}
			n += copy(p[n:], buf[:r])
		}
	}
	return
}
func (r *headCache) Close() error {
	for i := range r.bufs {
		r.bufs[i] = nil
	}
	r.bufs = nil
	return nil
}

func (r *RangeReadReadAtSeeker) InitHeadCache() {
	if r.ss.GetFile() == nil && r.masterOff == 0 {
		reader := r.readers[0]
		r.readers = r.readers[1:]
		r.headCache = &headCache{readerCur: reader}
		r.ss.Closers.Add(r.headCache)
	}
}

func NewReadAtSeeker(ss *SeekableStream, offset int64, forceRange ...bool) (model.File, error) {
	if ss.GetFile() != nil {
		_, err := ss.GetFile().Seek(offset, io.SeekStart)
		if err != nil {
			return nil, err
		}
		return ss.GetFile(), nil
	}
	r := &RangeReadReadAtSeeker{
		ss:        ss,
		masterOff: offset,
	}
	if offset != 0 || utils.IsBool(forceRange...) {
		if offset < 0 || offset > ss.GetSize() {
			return nil, errors.New("offset out of range")
		}
		_, err := r.getReaderAtOffset(offset)
		if err != nil {
			return nil, err
		}
	} else {
		rc := &readerCur{reader: ss, cur: offset}
		r.readers = append(r.readers, rc)
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

func (r *RangeReadReadAtSeeker) getReaderAtOffset(off int64) (*readerCur, error) {
	var rc *readerCur
	for _, reader := range r.readers {
		if reader.cur == -1 {
			continue
		}
		if reader.cur == off {
			// logrus.Debugf("getReaderAtOffset match_%d", off)
			return reader, nil
		}
		if reader.cur > 0 && off >= reader.cur && (rc == nil || reader.cur < rc.cur) {
			rc = reader
		}
	}
	if rc != nil && off-rc.cur <= utils.MB {
		n, err := utils.CopyWithBufferN(io.Discard, rc.reader, off-rc.cur)
		rc.cur += n
		if err == io.EOF && rc.cur == off {
			err = nil
		}
		if err == nil {
			logrus.Debugf("getReaderAtOffset old_%d", off)
			return rc, nil
		}
		rc.cur = -1
	}
	logrus.Debugf("getReaderAtOffset new_%d", off)

	// Range请求不能超过文件大小，有些云盘处理不了就会返回整个文件
	reader, err := r.ss.RangeRead(http_range.Range{Start: off, Length: r.ss.GetSize() - off})
	if err != nil {
		return nil, err
	}
	rc = &readerCur{reader: reader, cur: off}
	r.readers = append(r.readers, rc)
	return rc, nil
}

func (r *RangeReadReadAtSeeker) ReadAt(p []byte, off int64) (int, error) {
	if off == 0 && r.headCache != nil {
		return r.headCache.read(p)
	}
	rc, err := r.getReaderAtOffset(off)
	if err != nil {
		return 0, err
	}
	n, num := 0, 0
	for num < len(p) {
		n, err = rc.reader.Read(p[num:])
		rc.cur += int64(n)
		num += n
		if err == nil {
			continue
		}
		if err == io.EOF {
			// io.EOF是reader读取完了
			rc.cur = -1
			// yeka/zip包 没有处理EOF，我们要兼容
			// https://github.com/yeka/zip/blob/03d6312748a9d6e0bc0c9a7275385c09f06d9c14/reader.go#L433
			if num == len(p) {
				err = nil
			}
		}
		break
	}
	return num, err
}

func (r *RangeReadReadAtSeeker) Seek(offset int64, whence int) (int64, error) {
	switch whence {
	case io.SeekStart:
	case io.SeekCurrent:
		if offset == 0 {
			return r.masterOff, nil
		}
		offset += r.masterOff
	case io.SeekEnd:
		offset += r.ss.GetSize()
	default:
		return 0, errs.NotSupport
	}
	if offset < 0 {
		return r.masterOff, errors.New("invalid seek: negative position")
	}
	if offset > r.ss.GetSize() {
		offset = r.ss.GetSize()
	}
	r.masterOff = offset
	return offset, nil
}

func (r *RangeReadReadAtSeeker) Read(p []byte) (n int, err error) {
	if r.masterOff == 0 && r.headCache != nil {
		return r.headCache.read(p)
	}
	rc, err := r.getReaderAtOffset(r.masterOff)
	if err != nil {
		return 0, err
	}
	n, err = rc.reader.Read(p)
	rc.cur += int64(n)
	r.masterOff += int64(n)
	return n, err
}
