package hybrid_cache_test

import (
	"errors"
	"io"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/hybrid_cache"
)

func TestBufferStore(t *testing.T) {
	type args struct {
		p   []byte
		off int64
	}
	bs := &hybrid_cache.BufferStore{}
	bs.Append([]byte("github.com"))
	bs.Append([]byte("/OpenList"))
	bs.Append([]byte("Team/?"))
	b := []byte("OpenList")
	off := bs.Size() - 1
	_ = bs.GrowTo(off + int64(len(b)))
	_, _ = bs.WriteAt(b, off)

	tests := []struct {
		name  string
		b     *hybrid_cache.BufferStore
		args  args
		check func(a args, n int, err error) error
	}{
		{
			name: "readAt len 10 offset 0",
			b:    bs,
			args: args{
				p:   make([]byte, 10),
				off: 0,
			},
			check: func(a args, n int, err error) error {
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
			check: func(a args, n int, err error) error {
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
			check: func(a args, n int, err error) error {
				if n != int(bs.Size()-a.off) {
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
			if err := tt.check(tt.args, got, err); err != nil {
				t.Errorf("BufferStore.ReadAt() error = %v", err)
			}
		})
	}
}
