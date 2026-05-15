package stream_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

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
	prevCacheThreshold := conf.CacheThreshold
	prevMaxBlockLimit := conf.MaxBlockLimit
	t.Cleanup(func() {
		conf.CacheThreshold = prevCacheThreshold
		conf.MaxBlockLimit = prevMaxBlockLimit
	})
	conf.CacheThreshold = 10
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
	prevCacheThreshold := conf.CacheThreshold
	prevMaxBlockLimit := conf.MaxBlockLimit
	t.Cleanup(func() {
		conf.CacheThreshold = prevCacheThreshold
		conf.MaxBlockLimit = prevMaxBlockLimit
	})
	conf.CacheThreshold = 10
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
	prevCacheThreshold := conf.CacheThreshold
	prevMaxBlockLimit := conf.MaxBlockLimit
	prevConf := conf.Conf
	t.Cleanup(func() {
		conf.CacheThreshold = prevCacheThreshold
		conf.MaxBlockLimit = prevMaxBlockLimit
		conf.Conf = prevConf
	})
	conf.CacheThreshold = 1
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
