package buffer

import (
	"context"
	"fmt"
	"io"
	"sync"
)

type PipeBuffer struct {
	limit int //expected size
	ctx   context.Context
	offR  int
	offW  int
	rw    sync.Mutex
	block Block

	readSignal  chan struct{}
	readPending bool
}

// NewPipeBuffer is a buffer that can have 1 read & 1 write at the same time.
// when read is faster write, immediately feed data to read after written
func NewPipeBuffer(ctx context.Context, block Block) *PipeBuffer {
	br := &PipeBuffer{
		ctx:        ctx,
		limit:      int(block.Size()),
		readSignal: make(chan struct{}, 1),
		block:      block,
	}
	return br
}

func (br *PipeBuffer) Read(p []byte) (int, error) {
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
		if br.block == nil {
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
	block := br.block
	br.rw.Unlock()

	n, err := block.ReadAt(p[:min(len(p), canRead)], int64(off))

	br.rw.Lock()
	br.offR += n
	br.rw.Unlock()

	if n < len(p) && br.offR >= br.limit {
		return n, io.EOF
	}
	return n, err
}

func (br *PipeBuffer) Write(p []byte) (int, error) {
	if err := br.ctx.Err(); err != nil {
		return 0, err
	}
	if len(p) == 0 {
		return 0, nil
	}

	br.rw.Lock()
	if br.block == nil {
		br.rw.Unlock()
		return 0, io.ErrClosedPipe
	}

	canWrite := br.limit - br.offW
	if canWrite <= 0 {
		br.rw.Unlock()
		return 0, io.ErrShortWrite
	}

	off := br.offW
	block := br.block
	br.rw.Unlock()

	n, err := block.WriteAt(p[:min(canWrite, len(p))], int64(off))

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

func (br *PipeBuffer) Reset(limit int) error {
	br.rw.Lock()
	defer br.rw.Unlock()
	if br.block == nil {
		return io.ErrClosedPipe
	}
	if int64(limit) > br.block.Size() {
		return fmt.Errorf("reset limit %d exceeds max size %d", limit, br.block.Size())
	}
	br.limit = limit
	br.offR = 0
	br.offW = 0
	return nil
}

func (br *PipeBuffer) Close() error {
	br.rw.Lock()
	defer br.rw.Unlock()
	if br.block != nil {
		br.block = nil
		br.readPending = false
		close(br.readSignal)
	}
	return nil
}
