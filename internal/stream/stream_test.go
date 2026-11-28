package stream_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestRangeRead(t *testing.T) {
	type args struct {
		httpRange http_range.Range
	}
	buf := []byte("github.com/OpenListTeam/OpenList")
	f := &stream.FileStream{
		Obj: &model.Object{
			Size: int64(len(buf)),
		},
		Reader: io.NopCloser(bytes.NewReader(buf)),
	}
	prevAutoMemoryLimit := conf.AutoMemoryLimit
	prevMaxBlockLimit := conf.MaxBlockLimit
	t.Cleanup(func() {
		conf.AutoMemoryLimit = prevAutoMemoryLimit
		conf.MaxBlockLimit = prevMaxBlockLimit
	})
	conf.AutoMemoryLimit = 0
	conf.MaxBlockLimit = 15
	tests := []struct {
		name string
		f    *stream.FileStream
		args args
		want func(f *stream.FileStream, got io.Reader, err error) error
	}{
		{
			name: "range 11-12",
			f:    f,
			args: args{
				httpRange: http_range.Range{Start: 11, Length: 12},
			},
			want: func(f *stream.FileStream, got io.Reader, err error) error {
				if f.GetFile() != nil {
					return errors.New("cached")
				}
				b, _ := io.ReadAll(got)
				if !bytes.Equal(buf[11:11+12], b) {
					return fmt.Errorf("=%s ,want =%s", b, buf[11:11+12])
				}
				return nil
			},
		},
		{
			name: "range 11-21",
			f:    f,
			args: args{
				httpRange: http_range.Range{Start: 11, Length: 21},
			},
			want: func(f *stream.FileStream, got io.Reader, err error) error {
				if f.GetFile() == nil {
					return errors.New("not cached")
				}
				b, _ := io.ReadAll(got)
				if !bytes.Equal(buf[11:11+21], b) {
					return fmt.Errorf("=%s ,want =%s", b, buf[11:11+21])
				}
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.f.RangeRead(tt.args.httpRange)
			if err := tt.want(tt.f, got, err); err != nil {
				t.Errorf("FileStream.RangeRead() %v", err)
			}
		})
	}
	if f.GetFile() == nil {
		t.Error("not cached")
	}
	buf2 := make([]byte, len(buf))
	if _, err := io.ReadFull(f, buf2); err != nil {
		t.Errorf("FileStream.Read() error = %v", err)
	}
	if !bytes.Equal(buf, buf2) {
		t.Errorf("FileStream.Read() = %s, want %s", buf2, buf)
	}
}

func TestPreHash(t *testing.T) {
	buf := []byte("github.com/OpenListTeam/OpenList")
	f := &stream.FileStream{
		Obj: &model.Object{
			Size: int64(len(buf)),
		},
		Reader: io.NopCloser(bytes.NewReader(buf)),
	}
	prevAutoMemoryLimit := conf.AutoMemoryLimit
	prevMaxBlockLimit := conf.MaxBlockLimit
	t.Cleanup(func() {
		conf.AutoMemoryLimit = prevAutoMemoryLimit
		conf.MaxBlockLimit = prevMaxBlockLimit
	})
	conf.AutoMemoryLimit = 0
	conf.MaxBlockLimit = 15

	const hashSize int64 = 20
	reader, _ := f.RangeRead(http_range.Range{Start: 0, Length: hashSize})
	preHash, _ := utils.HashReader(utils.SHA1, reader)
	if preHash == "" {
		t.Error("preHash is empty")
	}
	tmpF, fullHash, _ := stream.CacheFullAndHash(f, nil, utils.SHA1)
	fmt.Println(fullHash)
	fileFullHash, _ := utils.HashFile(utils.SHA1, tmpF)
	fmt.Println(fileFullHash)
	if fullHash != fileFullHash {
		t.Errorf("fullHash and fileFullHash should match: fullHash=%s fileFullHash=%s", fullHash, fileFullHash)
	}
}

func TestStreamSectionReader(t *testing.T) {
	buf := make([]byte, 8<<10)
	for i := range len(buf) {
		buf[i] = byte(i % 256)
	}
	f := &stream.FileStream{
		Obj: &model.Object{
			Size: int64(len(buf)),
		},
		Reader: io.NopCloser(bytes.NewReader(buf)),
	}
	prevAutoMemoryLimit := conf.AutoMemoryLimit
	prevMaxBlockLimit := conf.MaxBlockLimit
	prevConf := conf.Conf
	t.Cleanup(func() {
		conf.AutoMemoryLimit = prevAutoMemoryLimit
		conf.MaxBlockLimit = prevMaxBlockLimit
		conf.Conf = prevConf
	})
	conf.AutoMemoryLimit = 0
	conf.MaxBlockLimit = 2 << 10
	partSize := 3 << 10
	conf.Conf = &conf.Config{}
	ss, err := stream.NewStreamSectionReader(f, partSize, nil)
	if err != nil {
		t.Errorf("NewStreamSectionReader() error = %v", err)
	}
	for i := 0; i < len(buf); i += partSize {
		length := partSize
		if i+length > len(buf) {
			length = len(buf) - i
		}
		rs, err := ss.GetSectionReader(int64(i), int64(length))
		if err != nil {
			t.Errorf("StreamSectionReader.GetSectionReader() error = %v", err)
		}
		b1, err := io.ReadAll(rs)
		if err != nil {
			t.Errorf("StreamSectionReader.Read() error = %v", err)
		}
		rs.Seek(1, io.SeekStart)
		b2, _ := io.ReadAll(rs)
		if !bytes.Equal(b1[1:], b2) {
			t.Errorf("StreamSectionReader.Read() = %s, want %s", b1[1:], b2)
		}
		if !bytes.Equal(buf[i:i+length], b1) {
			t.Errorf("StreamSectionReader.Read() = %s, want %s", b1, buf[i:i+length])
		}
		if i == 0 {
			prevMinFreeMemory := conf.MinFreeMemory
			conf.MinFreeMemory = 0 // 强制使用文件缓存
			t.Cleanup(func() {
				conf.MinFreeMemory = prevMinFreeMemory
			})
		}
	}
}

type slowChunkReader struct {
	r            io.Reader
	reads        int
	inSecondRead chan struct{}
	freed        chan struct{}
	done         chan struct{}
	once         sync.Once
}

func (s *slowChunkReader) Read(p []byte) (int, error) {
	s.reads++
	if s.reads == 2 {
		// 通知服务端 writeLoop 已进入第 2 次 Read，随后等待 FreeSectionReader 完成
		close(s.inSecondRead)
		<-s.freed
	}
	if len(p) > 8192 {
		p = p[:8192]
	}
	n, err := s.r.Read(p)
	if err != nil {
		s.once.Do(func() { close(s.done) })
	}
	return n, err
}

func TestHybridSectionReaderEarlyHTTPResponse(t *testing.T) {
	prevAutoMemoryLimit := conf.AutoMemoryLimit
	prevMaxBlockLimit := conf.MaxBlockLimit
	prevConf := conf.Conf
	t.Cleanup(func() {
		conf.AutoMemoryLimit = prevAutoMemoryLimit
		conf.MaxBlockLimit = prevMaxBlockLimit
		conf.Conf = prevConf
	})
	conf.AutoMemoryLimit = 0
	conf.MaxBlockLimit = 1 << 20
	conf.Conf = &conf.Config{}

	const size = 16 << 10
	f := &stream.FileStream{
		Obj:    &model.Object{Size: size},
		Reader: io.NopCloser(bytes.NewReader(make([]byte, size))),
	}
	ss, err := stream.NewStreamSectionReader(f, size, nil)
	if err != nil {
		t.Fatalf("NewStreamSectionReader() error = %v", err)
	}
	rs, err := ss.GetSectionReader(0, size)
	if err != nil {
		t.Fatalf("GetSectionReader() error = %v", err)
	}

	inSecondRead := make(chan struct{})
	// 服务端确认 writeLoop 已卡在第 2 次 Read 后，再提前返回 401
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-inSecondRead
		w.Header().Set("Connection", "close")
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	body := &slowChunkReader{
		r:            rs,
		inSecondRead: inSecondRead,
		freed:        make(chan struct{}),
		done:         make(chan struct{}),
	}
	req, err := http.NewRequest(http.MethodPut, srv.URL, body)
	if err != nil {
		t.Fatalf("NewRequest() error = %v", err)
	}
	req.ContentLength = size

	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("Client.Do() error = %v", err)
	}
	res.Body.Close()

	// readLoop 已收到 401 返回，此时 writeLoop 必然正卡在第 2 次 Read 上
	ss.FreeSectionReader(rs)
	close(body.freed)

	select {
	case <-body.done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for HTTP writeLoop to observe closed reader")
	}
}
