package stream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
)

func TestFileStream_RangeRead(t *testing.T) {
	conf.MaxBufferLimit = 16 * 1024 * 1024
	type args struct {
		httpRange http_range.Range
	}
	buf := []byte("github.com/OpenListTeam/OpenList")
	f := &FileStream{
		Obj: &model.Object{
			Size: int64(len(buf)),
		},
		Reader: io.NopCloser(bytes.NewReader(buf)),
	}
	tests := []struct {
		name string
		f    *FileStream
		args args
		want func(f *FileStream, got io.Reader, err error) error
	}{
		{
			name: "range 11-12",
			f:    f,
			args: args{
				httpRange: http_range.Range{Start: 11, Length: 12},
			},
			want: func(f *FileStream, got io.Reader, err error) error {
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
			want: func(f *FileStream, got io.Reader, err error) error {
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
	t.Run("after", func(t *testing.T) {
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
	})
}
