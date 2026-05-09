package buffer

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type SizedReadWriterAt interface {
	io.ReaderAt
	io.WriterAt
	Size() int64
}

type StreamBuffer interface {
	io.ReadWriteCloser
	Reset(size int) error
}

type streamBuf struct {
	limit int //expected size
	ctx   context.Context
	offR  int
	offW  int
	rw    sync.Mutex
	s     SizedReadWriterAt

	readSignal  chan struct{}
	readPending bool
}

// NewStreamBuffer is a buffer that can have 1 read & 1 write at the same time.
// when read is faster write, immediately feed data to read after written
func NewStreamBuffer(ctx context.Context, s SizedReadWriterAt) StreamBuffer {
	br := &streamBuf{
		ctx:        ctx,
		limit:      int(s.Size()),
		readSignal: make(chan struct{}, 1),
		s:          s,
	}
	return br
}

func (br *streamBuf) Read(p []byte) (int, error) {
	if err := br.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	if br.offR >= br.limit {
		return 0, io.EOF
	}

	for {
		br.rw.Lock()
		if br.s == nil {
			br.rw.Unlock()
			return 0, io.ErrClosedPipe
		}

		if br.offW == br.offR {
			br.readPending = true
			br.rw.Unlock()
			select {
			case <-br.ctx.Done():
				return 0, br.ctx.Err()
			case _, ok := <-br.readSignal:
				if !ok {
					return 0, io.ErrClosedPipe
				}
				continue
			}
		}
		break
	}

	canRead := br.offW - br.offR
	if canRead < 0 {
		br.rw.Unlock()
		return 0, io.ErrUnexpectedEOF
	}
	off := br.offR
	br.rw.Unlock()
	n, err := br.s.ReadAt(p[:min(len(p), canRead)], int64(off))
	br.rw.Lock()
	br.offR += n
	br.rw.Unlock()
	if n < len(p) && br.offR >= br.limit {
		return n, io.EOF
	}
	return n, err
}

func (br *streamBuf) Write(p []byte) (int, error) {
	if err := br.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}
	br.rw.Lock()
	if br.s == nil {
		br.rw.Unlock()
		return 0, io.ErrClosedPipe
	}
	canWrite := br.limit - br.offW
	if canWrite <= 0 {
		br.rw.Unlock()
		return 0, io.ErrShortWrite
	}
	off := br.offW
	br.rw.Unlock()
	n, err := br.s.WriteAt(p[:min(canWrite, len(p))], int64(off))
	br.rw.Lock()
	br.offW += n
	if br.readPending {
		br.readPending = false
		select {
		case br.readSignal <- struct{}{}:
		default:
		}
	}
	br.rw.Unlock()

	if n < len(p) && err == nil {
		return n, io.ErrShortWrite
	}
	return n, err
}

func (br *streamBuf) Reset(limit int) error {
	br.rw.Lock()
	defer br.rw.Unlock()
	if br.s == nil {
		return io.ErrClosedPipe
	}
	if int64(limit) > br.s.Size() {
		return fmt.Errorf("reset limit %d exceeds max size %d", limit, br.s.Size())
	}
	br.limit = limit
	br.offR = 0
	br.offW = 0
	return nil
}

func (br *streamBuf) Close() error {
	br.rw.Lock()
	defer br.rw.Unlock()
	if br.s != nil {
		br.s = nil
		br.readPending = false
		close(br.readSignal)
	}
	return nil
}
