package buffer

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

type blockingBlock struct {
	data    []byte
	blockOn string
	started chan struct{}
	release chan struct{}
}

func newBlockingBlock(blockOn string) *blockingBlock {
	return &blockingBlock{
		data:    make([]byte, 1),
		blockOn: blockOn,
		started: make(chan struct{}),
		release: make(chan struct{}),
	}
}

func (b *blockingBlock) Size() int64 {
	return int64(len(b.data))
}

func (b *blockingBlock) ReadAt(p []byte, off int64) (int, error) {
	if b.blockOn == "read" {
		close(b.started)
		<-b.release
	}
	n := copy(p, b.data[off:])
	if n < len(p) {
		return n, io.EOF
	}
	return n, nil
}

func (b *blockingBlock) WriteAt(p []byte, off int64) (int, error) {
	if b.blockOn == "write" {
		close(b.started)
		<-b.release
	}
	n := copy(b.data[off:], p)
	if n < len(p) {
		return n, io.ErrShortWrite
	}
	return n, nil
}

func TestPipeBufferCloseWaitsForActiveIO(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		t.Run(operation, func(t *testing.T) {
			block := newBlockingBlock(operation)
			buf := NewPipeBuffer(context.Background(), block)
			if operation == "read" {
				if _, err := buf.Write([]byte{1}); err != nil {
					t.Fatalf("prepare read: %v", err)
				}
			}

			ioDone := make(chan error, 1)
			go func() {
				var err error
				if operation == "read" {
					_, err = buf.Read(make([]byte, 1))
				} else {
					_, err = buf.Write([]byte{1})
				}
				ioDone <- err
			}()

			select {
			case <-block.started:
			case <-time.After(time.Second):
				t.Fatal("I/O did not start")
			}

			closeDone := make(chan error, 1)
			go func() {
				closeDone <- buf.Close()
			}()

			deadline := time.Now().Add(time.Second)
			for {
				buf.rw.Lock()
				closed := buf.block == nil
				buf.rw.Unlock()
				if closed {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("buffer did not enter the closed state")
				}
				time.Sleep(time.Millisecond)
			}

			select {
			case err := <-closeDone:
				t.Fatalf("Close returned before active %s completed: %v", operation, err)
			default:
			}

			close(block.release)
			select {
			case err := <-ioDone:
				if err != nil {
					t.Fatalf("active %s failed: %v", operation, err)
				}
			case <-time.After(time.Second):
				t.Fatalf("active %s did not complete", operation)
			}
			select {
			case err := <-closeDone:
				if err != nil {
					t.Fatalf("Close failed: %v", err)
				}
			case <-time.After(time.Second):
				t.Fatal("Close did not wait for active I/O")
			}

			if _, err := buf.Write([]byte{1}); !errors.Is(err, io.ErrClosedPipe) {
				t.Fatalf("write after Close error = %v, want %v", err, io.ErrClosedPipe)
			}
		})
	}
}
