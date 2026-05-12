package cache_test

import (
	"bytes"
	"errors"
	"io"
	"os"
	"reflect"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/cache"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

func TestFile(t *testing.T) {
	f, err := os.CreateTemp("", "writeat-*")
	if err != nil {
		t.Error(err)
		return
	}
	defer os.Remove(f.Name())
	defer f.Close()
	t.Run("ReadAt", func(t *testing.T) {
		_, err := f.ReadAt(make([]byte, 1), 20)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Error(err)
		}
	})
	t.Run("WriteAt", func(t *testing.T) {
		n, err := f.WriteAt([]byte("abc"), 20)
		if err != nil {
			t.Errorf("write n=%d err=%v", n, err)
			return
		}
		stat, err := f.Stat()
		if err != nil {
			t.Errorf("stat err=%v", err)
			return
		}
		if stat.Size() != 23 {
			t.Fatalf("unexpected size: got %d want 23", stat.Size())
		}

		b := make([]byte, stat.Size())
		rn, rerr := f.ReadAt(b, 0)
		if rn != len(b) || rerr != nil {
			t.Fatalf("read n=%d err=%v", rn, rerr)
		}
		want := append(make([]byte, 20), []byte("abc")...)
		if !reflect.DeepEqual(b, want) {
			t.Fatalf("unexpected content: got %v want %v", b, want)
		}
	})
}

func TestMultiFileCache(t *testing.T) {
	prevConf := conf.Conf
	t.Cleanup(func() {
		conf.Conf = prevConf
	})
	conf.Conf = &conf.Config{}
	f := cache.MultiFileCache{}
	defer f.Close()
	t.Run("ReadAt", func(t *testing.T) {
		_, err := f.ReadAt(make([]byte, 1), 20)
		if err != nil && !errors.Is(err, io.EOF) {
			t.Error(err)
		}
	})
	t.Run("WriteAt", func(t *testing.T) {
		err := f.Truncate(15)
		if err != nil {
			t.Errorf("truncate err=%v", err)
			return
		}
		n, err := f.WriteAt([]byte("abc"), 10)
		if err != nil {
			t.Errorf("write n=%d err=%v", n, err)
			return
		}

		err = f.Truncate(30)
		if err != nil {
			t.Errorf("truncate err=%v", err)
			return
		}
		_, _ = f.WriteAt([]byte("123"), 15)

		b := append(make([]byte, 17), []byte("def")...)
		b[0] = 'a'
		rn, rerr := f.ReadAt(b, 8)
		if rn != len(b) || rerr != nil {
			t.Fatalf("read n=%d err=%v", rn, rerr)
		}
		want := []byte{0, 0, 'a', 'b', 'c', 0, 0, '1', '2', '3'}
		want = append(want, make([]byte, 10)...)
		if !bytes.Equal(b, want) {
			t.Fatalf("unexpected content: got %v want %v", b, want)
		}
	})
}
