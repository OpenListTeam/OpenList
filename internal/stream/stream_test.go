package stream

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestFileStream_RangeRead(t *testing.T) {
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
		name    string
		f       *FileStream
		args    args
		wantErr bool
		want    func(f *FileStream, got io.Reader, err error) error
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
				if !utils.SliceEqual(buf[11:11+12], b) {
					return fmt.Errorf("expect =%s ,actual =%s", buf[11:11+12], b)
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
				if !utils.SliceEqual(buf[11:11+21], b) {
					return fmt.Errorf("expect =%s ,actual =%s", buf[11:11+21], b)
				}
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.f.RangeRead(tt.args.httpRange)
			if (err != nil) != tt.wantErr {
				t.Errorf("FileStream.RangeRead() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if tt.want == nil {
				return
			}
			if err := tt.want(tt.f, got, err); err != nil {
				t.Errorf("FileStream.RangeRead() %v", err)
			}
		})
	}
}
