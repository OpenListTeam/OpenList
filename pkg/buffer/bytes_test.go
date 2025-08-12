package buffer

import (
	"errors"
	"io"
	"testing"
)

func TestReader_ReadAt(t *testing.T) {
	type args struct {
		p   []byte
		off int64
	}
	bs := &Reader{}
	bs.Append([]byte("github.com"))
	bs.Append([]byte("/"))
	bs.Append([]byte("OpenList"))
	bs.Append([]byte("Team/"))
	bs.Append([]byte("OpenList"))
	tests := []struct {
		name string
		b    *Reader
		args args
		want func(a args, n int, err error) error
	}{
		{
			name: "readAt len 10 offset 0",
			b:    bs,
			args: args{
				p:   make([]byte, 10),
				off: 0,
			},
			want: func(a args, n int, err error) error {
				if n != len(a.p) {
					return errors.New("read length not match")
				}
				if string(a.p) != "github.com" {
					return errors.New("read content not match")
				}
				if err != nil {
					return err
				}
				return nil
			},
		},
		{
			name: "readAt len 12 offset 11",
			b:    bs,
			args: args{
				p:   make([]byte, 12),
				off: 11,
			},
			want: func(a args, n int, err error) error {
				if n != len(a.p) {
					return errors.New("read length not match")
				}
				if string(a.p) != "OpenListTeam" {
					return errors.New("read content not match")
				}
				if err != nil {
					return err
				}
				return nil
			},
		},
		{
			name: "readAt len 50 offset 24",
			b:    bs,
			args: args{
				p:   make([]byte, 50),
				off: 24,
			},
			want: func(a args, n int, err error) error {
				if n != bs.Len()-int(a.off) {
					return errors.New("read length not match")
				}
				if string(a.p[:n]) != "OpenList" {
					return errors.New("read content not match")
				}
				if err != io.EOF {
					return errors.New("expect eof")
				}
				return nil
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.b.ReadAt(tt.args.p, tt.args.off)
			if err := tt.want(tt.args, got, err); err != nil {
				t.Errorf("Bytes.ReadAt() error = %v", err)
			}
		})
	}
}
