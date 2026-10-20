package multipart

import (
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const (
	slotFree uint8 = iota
	slotFilling
	slotReady
)

var (
	// ErrClosed is the sticky error after Close; all pending and future reads/writes fail with it.
	ErrClosed = errors.New("multipart upload window closed")
	// ErrChunkInFlight means another request is uploading the same chunk right now.
	ErrChunkInFlight = errors.New("chunk is being uploaded by another request")
	// ErrOutOfWindow means the chunk is still too far ahead of the consumption
	// frontier after waiting WindowWaitTimeout; the client should back off and
	// resend it later (flow control, not a failure).
	ErrOutOfWindow = errors.New("chunk is out of the receiving window")
)

// WindowWaitTimeout bounds how long WriteChunk blocks waiting for its slot.
// Browsers cannot reliably read responses sent before the request body is
// consumed (they report a network error), so under backpressure it is far
// better to hold the request until a slot frees — the wait must just stay
// well below CDN request deadlines (Cloudflare: ~100s). Tests shrink this.
var WindowWaitTimeout = 10 * time.Second

// Window reassembles concurrently uploaded chunks into a sequential stream.
// Chunks land in a ring file of slots*chunkSize bytes (chunk i -> slot i%slots),
// and Read serves bytes in order, blocking until the next needed chunk arrives.
// A chunk slot is released as soon as the reader crosses its boundary, so the
// disk footprint is bounded by slots*chunkSize regardless of the file size.
//
// WriteChunk is safe for concurrent use; Read must be called from a single
// goroutine (the same contract as the FileStreamer it backs).
type Window struct {
	mu   sync.Mutex
	cond *sync.Cond

	f    *os.File
	path string

	chunkSize int64
	totalSize int64
	total     int
	slots     int

	slotState []uint8
	slotChunk []int
	readPos   int64

	crcs   []uint32
	crcSet []bool

	err error
}

// Snapshot describes the receiving state, used for status responses and resume.
type Snapshot struct {
	// Frontier is the next chunk index to be consumed (== TotalChunks when the stream is fully consumed).
	Frontier int
	// ReadPos is the number of bytes already consumed by the pipeline.
	ReadPos int64
	// ReceivedBytes is the number of payload bytes received from the client (consumed + buffered).
	ReceivedBytes int64
	// Received holds inclusive ranges of chunk indexes the client does not need to resend.
	Received [][2]int
}

func NewWindow(dir, id string, chunkSize, totalSize int64, slots int) (*Window, error) {
	if chunkSize <= 0 || totalSize <= 0 || slots <= 0 {
		return nil, fmt.Errorf("invalid window params: chunkSize=%d totalSize=%d slots=%d", chunkSize, totalSize, slots)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, id+".ring")
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, err
	}
	total := int((totalSize + chunkSize - 1) / chunkSize)
	w := &Window{
		f:         f,
		path:      path,
		chunkSize: chunkSize,
		totalSize: totalSize,
		total:     total,
		slots:     slots,
		slotState: make([]uint8, slots),
		slotChunk: make([]int, slots),
		crcs:      make([]uint32, total),
		crcSet:    make([]bool, total),
	}
	for i := range w.slotChunk {
		w.slotChunk[i] = -1
	}
	w.cond = sync.NewCond(&w.mu)
	return w, nil
}

func (w *Window) TotalChunks() int { return w.total }

// ChunkLen returns the payload length of chunk idx (the last chunk may be short).
func (w *Window) ChunkLen(idx int) int64 {
	if idx == w.total-1 {
		return w.totalSize - int64(idx)*w.chunkSize
	}
	return w.chunkSize
}

func stopTimer(t *time.Timer) {
	if t != nil {
		t.Stop()
	}
}

// frontier returns the next chunk index to be consumed. Callers must hold mu.
func (w *Window) frontier() int {
	if w.readPos >= w.totalSize {
		return w.total
	}
	return int(w.readPos / w.chunkSize)
}

// WriteChunk reads exactly the chunk payload from r into the ring and returns its CRC32 (IEEE).
// Re-sending an already buffered or consumed chunk succeeds immediately without touching data.
func (w *Window) WriteChunk(idx int, r io.Reader) (uint32, error) {
	w.mu.Lock()
	if w.err != nil {
		w.mu.Unlock()
		return 0, w.err
	}
	if idx < 0 || idx >= w.total {
		w.mu.Unlock()
		return 0, fmt.Errorf("chunk index %d out of range [0,%d)", idx, w.total)
	}
	length := w.ChunkLen(idx)
	slot := idx % w.slots
	// Admission control with a bounded wait: instead of bouncing a chunk the
	// moment its slot is busy, park the request until the reader frees the
	// slot. Rejecting fast would answer before the request body is read, which
	// browsers surface as a network error — so under backpressure, waiting IS
	// the flow control. sync.Cond has no timed wait; a timer broadcast wakes
	// the loop at the deadline.
	deadline := time.Now().Add(WindowWaitTimeout)
	var timer *time.Timer
	for {
		if w.err != nil {
			w.mu.Unlock()
			stopTimer(timer)
			return 0, w.err
		}
		if int64(idx)*w.chunkSize+length <= w.readPos {
			// already fully consumed
			crc := w.crcs[idx]
			w.mu.Unlock()
			stopTimer(timer)
			return crc, nil
		}
		if w.slotChunk[slot] == idx {
			if w.slotState[slot] == slotReady {
				crc := w.crcs[idx]
				w.mu.Unlock()
				stopTimer(timer)
				return crc, nil
			}
			if w.slotState[slot] == slotFilling {
				w.mu.Unlock()
				stopTimer(timer)
				return 0, ErrChunkInFlight
			}
		}
		if w.slotState[slot] == slotFree && idx < w.frontier()+w.slots {
			break // admissible
		}
		if !time.Now().Before(deadline) {
			w.mu.Unlock()
			stopTimer(timer)
			return 0, ErrOutOfWindow
		}
		if timer == nil {
			timer = time.AfterFunc(time.Until(deadline), func() {
				w.mu.Lock()
				w.cond.Broadcast()
				w.mu.Unlock()
			})
		}
		w.cond.Wait()
	}
	stopTimer(timer)
	w.slotState[slot] = slotFilling
	w.slotChunk[slot] = idx
	f := w.f
	w.mu.Unlock()

	h := crc32.NewIEEE()
	n, err := utils.CopyWithBufferN(io.NewOffsetWriter(f, int64(slot)*w.chunkSize), io.TeeReader(r, h), length)
	if err == nil {
		// the body must contain exactly one chunk
		var b [1]byte
		if m, _ := io.ReadFull(r, b[:]); m > 0 {
			err = fmt.Errorf("chunk %d larger than expected %d bytes", idx, length)
		}
	} else {
		err = fmt.Errorf("incomplete chunk %d: got %d of %d bytes: %w", idx, n, length, err)
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	if w.slotState[slot] != slotFilling || w.slotChunk[slot] != idx {
		// the window was closed and reset the slot while we were writing
		if w.err != nil {
			return 0, w.err
		}
		return 0, ErrClosed
	}
	if err == nil && w.err != nil {
		err = w.err
	}
	if err != nil {
		w.slotState[slot] = slotFree
		w.slotChunk[slot] = -1
		return 0, err
	}
	w.slotState[slot] = slotReady
	w.crcs[idx] = h.Sum32()
	w.crcSet[idx] = true
	w.cond.Broadcast()
	return w.crcs[idx], nil
}

// Read serves the reassembled stream in order, blocking until the next chunk
// is available, the window is closed, or the stream ends (io.EOF).
func (w *Window) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	w.mu.Lock()
	for {
		if w.err != nil {
			w.mu.Unlock()
			return 0, w.err
		}
		if w.readPos >= w.totalSize {
			w.mu.Unlock()
			return 0, io.EOF
		}
		cur := int(w.readPos / w.chunkSize)
		slot := cur % w.slots
		if w.slotState[slot] == slotReady && w.slotChunk[slot] == cur {
			chunkStart := int64(cur) * w.chunkSize
			chunkEnd := chunkStart + w.ChunkLen(cur)
			n := int64(len(p))
			if avail := chunkEnd - w.readPos; n > avail {
				n = avail
			}
			off := int64(slot)*w.chunkSize + (w.readPos - chunkStart)
			f := w.f
			w.mu.Unlock()

			read, err := f.ReadAt(p[:n], off)

			w.mu.Lock()
			if read > 0 {
				w.readPos += int64(read)
				if w.readPos >= chunkEnd {
					w.slotState[slot] = slotFree
					w.slotChunk[slot] = -1
					w.cond.Broadcast() // writers may be parked waiting for this slot
				}
			}
			sticky := w.err
			w.mu.Unlock()
			if read > 0 {
				return read, nil
			}
			if sticky != nil {
				return 0, sticky
			}
			if err == nil {
				err = io.ErrUnexpectedEOF
			}
			return 0, err
		}
		w.cond.Wait()
	}
}

// Close makes all pending and future operations fail with ErrClosed and removes
// the ring file. It is invoked by op.Put via FileStream.Closers when the
// pipeline ends, and is safe to call multiple times.
func (w *Window) Close() error {
	return w.CloseWithError(ErrClosed)
}

// CloseWithError is Close with a caller-chosen sticky error. The session's
// abort path passes an error wrapping context.Canceled so that a driver woken
// up from a blocked Read treats the abort exactly like a canceled request
// (e.g. the local driver only removes partially written files in that case).
func (w *Window) CloseWithError(sticky error) error {
	w.mu.Lock()
	if w.err == nil {
		w.err = sticky
	}
	f := w.f
	w.f = nil
	for i := range w.slotState {
		w.slotState[i] = slotFree
		w.slotChunk[i] = -1
	}
	w.cond.Broadcast()
	w.mu.Unlock()
	if f == nil {
		return nil
	}
	err := f.Close()
	if rmErr := os.Remove(w.path); rmErr != nil && err == nil {
		err = rmErr
	}
	return err
}

// Snapshot reports the receiving state for status responses and resume discovery.
func (w *Window) Snapshot() Snapshot {
	w.mu.Lock()
	defer w.mu.Unlock()
	snap := Snapshot{Frontier: w.frontier(), ReadPos: w.readPos, ReceivedBytes: w.readPos}
	var ranges [][2]int
	if snap.Frontier > 0 {
		ranges = append(ranges, [2]int{0, snap.Frontier - 1})
	}
	for idx := snap.Frontier; idx < snap.Frontier+w.slots && idx < w.total; idx++ {
		slot := idx % w.slots
		if w.slotState[slot] != slotReady || w.slotChunk[slot] != idx {
			continue
		}
		start := int64(idx) * w.chunkSize
		var consumed int64
		if w.readPos > start {
			consumed = w.readPos - start
		}
		snap.ReceivedBytes += w.ChunkLen(idx) - consumed
		if len(ranges) > 0 && ranges[len(ranges)-1][1] == idx-1 {
			ranges[len(ranges)-1][1] = idx
		} else {
			ranges = append(ranges, [2]int{idx, idx})
		}
	}
	snap.Received = ranges
	return snap
}

// CRCs returns a copy of the per-chunk CRC32 table and which entries are set.
// It remains readable after Close, so the session can compare re-filled chunks
// against a previous attempt.
func (w *Window) CRCs() ([]uint32, []bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	crcs := make([]uint32, len(w.crcs))
	set := make([]bool, len(w.crcSet))
	copy(crcs, w.crcs)
	copy(set, w.crcSet)
	return crcs, set
}
