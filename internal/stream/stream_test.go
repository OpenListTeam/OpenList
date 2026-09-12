package stream_test

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestFileStreamCachePolicy(t *testing.T) {
	oldConf := conf.Conf
	oldPolicy := conf.CachePolicy
	oldBlockLimit := conf.MaxBlockLimit
	oldAutoMemoryLimit := conf.AutoMemoryLimit
	t.Cleanup(func() {
		conf.Conf = oldConf
		conf.CachePolicy = oldPolicy
		conf.MaxBlockLimit = oldBlockLimit
		conf.AutoMemoryLimit = oldAutoMemoryLimit
	})
	conf.MaxBlockLimit = 4
	conf.AutoMemoryLimit = 0

	t.Run("inherit and override", func(t *testing.T) {
		conf.CachePolicy = cache.PolicyDisk
		f := &stream.FileStream{}
		if got := f.GetCachePolicy(); got != cache.PolicyDisk {
			t.Fatalf("GetCachePolicy() = %q, want disk", got)
		}
		if err := f.SetCachePolicy(cache.PolicyMemory); err != nil {
			t.Fatalf("SetCachePolicy() error = %v", err)
		}
		if got := f.GetCachePolicy(); got != cache.PolicyMemory {
			t.Fatalf("GetCachePolicy() = %q, want memory", got)
		}
		if err := f.SetCachePolicy(cache.PolicyInherit); err != nil {
			t.Fatalf("SetCachePolicy(inherit) error = %v", err)
		}
		if got := f.GetCachePolicy(); got != cache.PolicyDisk {
			t.Fatalf("GetCachePolicy() = %q after inherit, want disk", got)
		}
	})

	for _, tt := range []struct {
		policy   cache.Policy
		wantFile bool
	}{
		{cache.PolicyAuto, true},
		{cache.PolicyDisk, true},
		{cache.PolicyMemory, false},
	} {
		t.Run(string(tt.policy)+" unknown size", func(t *testing.T) {
			tempDir := t.TempDir()
			conf.Conf = &conf.Config{TempDir: tempDir}
			conf.CachePolicy = cache.PolicyAuto
			input := []byte("unknown-size-stream")
			f := &stream.FileStream{
				Obj:    &model.Object{Size: -1},
				Reader: io.NopCloser(bytes.NewReader(input)),
			}
			if err := f.SetCachePolicy(tt.policy); err != nil {
				t.Fatalf("SetCachePolicy() error = %v", err)
			}
			cached, err := f.CacheFullAndWriter(nil, nil)
			if err != nil {
				t.Fatalf("CacheFullAndWriter() error = %v", err)
			}
			if f.GetSize() != int64(len(input)) {
				t.Fatalf("GetSize() = %d, want %d", f.GetSize(), len(input))
			}
			got, err := io.ReadAll(cached)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if !bytes.Equal(got, input) {
				t.Fatalf("cached content = %q, want %q", got, input)
			}
			entries, err := os.ReadDir(tempDir)
			if err != nil {
				t.Fatalf("ReadDir() error = %v", err)
			}
			if gotFile := len(entries) > 0; gotFile != tt.wantFile {
				t.Fatalf("temporary file present = %v, want %v", gotFile, tt.wantFile)
			}
			if err := f.SetCachePolicy(cache.PolicyDisk); err == nil {
				t.Fatal("SetCachePolicy() expected an error after cache initialization")
			}
			if err := f.Close(); err != nil {
				t.Fatalf("Close() error = %v", err)
			}
			entries, err = os.ReadDir(tempDir)
			if err != nil {
				t.Fatalf("ReadDir() after close error = %v", err)
			}
			if len(entries) != 0 {
				t.Fatalf("temporary files remain after close: %v", entries)
			}
		})
	}
}

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
