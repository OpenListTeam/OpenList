package multipart

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"hash/crc32"
	"io"
	"math/rand"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func genData(size int64) []byte {
	data := make([]byte, size)
	rnd := rand.New(rand.NewSource(size*7919 + 13))
	rnd.Read(data)
	return data
}

func newTestWindow(t *testing.T, chunkSize, totalSize int64, slots int) *Window {
	t.Helper()
	w, err := NewWindow(t.TempDir(), "test", chunkSize, totalSize, slots)
	if err != nil {
		t.Fatalf("NewWindow: %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	return w
}

func chunkOf(data []byte, idx int, chunkSize int64) []byte {
	start := int64(idx) * chunkSize
	end := start + chunkSize
	if end > int64(len(data)) {
		end = int64(len(data))
	}
	return data[start:end]
}

func writeChunkOK(t *testing.T, w *Window, data []byte, idx int) uint32 {
	t.Helper()
	crc, err := w.WriteChunk(idx, bytes.NewReader(chunkOf(data, idx, w.chunkSize)))
	if err != nil {
		t.Fatalf("WriteChunk(%d): %v", idx, err)
	}
	if want := crc32.ChecksumIEEE(chunkOf(data, idx, w.chunkSize)); crc != want {
		t.Fatalf("WriteChunk(%d) crc = %08x, want %08x", idx, crc, want)
	}
	return crc
}

// readAllWithin reads the whole stream in a goroutine and fails the test on timeout,
// so a reassembly bug cannot hang the suite.
func readAllWithin(t *testing.T, w *Window, timeout time.Duration) []byte {
	t.Helper()
	type result struct {
		data []byte
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		data, err := io.ReadAll(w)
		ch <- result{data, err}
	}()
	select {
	case res := <-ch:
		if res.err != nil {
			t.Fatalf("ReadAll: %v", res.err)
		}
		return res.data
	case <-time.After(timeout):
		t.Fatal("ReadAll timed out")
		return nil
	}
}

func TestSequentialReadWrite(t *testing.T) {
	const chunkSize = 64 * 1024
	totalSize := int64(4*chunkSize + 32*1024) // last chunk is short
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 8)

	go func() {
		for i := 0; i < w.TotalChunks(); i++ {
			if _, err := w.WriteChunk(i, bytes.NewReader(chunkOf(data, i, chunkSize))); err != nil {
				t.Errorf("WriteChunk(%d): %v", i, err)
				return
			}
			time.Sleep(time.Millisecond)
		}
	}()

	got := readAllWithin(t, w, 10*time.Second)
	if !bytes.Equal(got, data) {
		t.Fatalf("reassembled stream differs: got %d bytes, want %d", len(got), len(data))
	}
}

func TestOutOfOrderWrites(t *testing.T) {
	const chunkSize = 16 * 1024
	totalSize := int64(5*chunkSize - 100)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 8)

	for _, idx := range []int{3, 0, 4, 2, 1} {
		writeChunkOK(t, w, data, idx)
	}
	got := readAllWithin(t, w, 10*time.Second)
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled stream differs after out-of-order writes")
	}
}

func TestConcurrentWriters(t *testing.T) {
	const chunkSize = 32 * 1024
	totalSize := int64(8 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 8)

	var wg sync.WaitGroup
	for i := 0; i < w.TotalChunks(); i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if _, err := w.WriteChunk(idx, bytes.NewReader(chunkOf(data, idx, chunkSize))); err != nil {
				t.Errorf("WriteChunk(%d): %v", idx, err)
			}
		}(i)
	}
	got := readAllWithin(t, w, 10*time.Second)
	wg.Wait()
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled stream differs after concurrent writes")
	}
}

func setWaitTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := WindowWaitTimeout
	WindowWaitTimeout = d
	t.Cleanup(func() { WindowWaitTimeout = old })
}

func TestBackpressure(t *testing.T) {
	setWaitTimeout(t, 50*time.Millisecond) // assert the post-deadline rejection
	const chunkSize = 1024
	totalSize := int64(5 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 2)

	if _, err := w.WriteChunk(2, bytes.NewReader(chunkOf(data, 2, chunkSize))); !errors.Is(err, ErrOutOfWindow) {
		t.Fatalf("chunk 2 with frontier 0: err = %v, want ErrOutOfWindow", err)
	}
	writeChunkOK(t, w, data, 0)
	writeChunkOK(t, w, data, 1)
	// both slots occupied, frontier still 0
	if _, err := w.WriteChunk(2, bytes.NewReader(chunkOf(data, 2, chunkSize))); !errors.Is(err, ErrOutOfWindow) {
		t.Fatalf("chunk 2 with full window: err = %v, want ErrOutOfWindow", err)
	}
	// consume chunk 0 -> slot released, frontier advances
	buf := make([]byte, chunkSize)
	if _, err := io.ReadFull(w, buf); err != nil {
		t.Fatalf("ReadFull chunk 0: %v", err)
	}
	if !bytes.Equal(buf, chunkOf(data, 0, chunkSize)) {
		t.Fatal("chunk 0 content differs")
	}
	writeChunkOK(t, w, data, 2)
	// chunk 3 maps to the slot still holding buffered chunk 1
	if _, err := w.WriteChunk(3, bytes.NewReader(chunkOf(data, 3, chunkSize))); !errors.Is(err, ErrOutOfWindow) {
		t.Fatalf("chunk 3 with occupied slot: err = %v, want ErrOutOfWindow", err)
	}
}

// TestWriteChunkWaitsForSlot pins the browser-friendly flow control: a chunk
// whose slot is occupied parks until the reader frees it instead of bouncing
// with an immediate rejection (early responses read as network errors in
// browsers).
func TestWriteChunkWaitsForSlot(t *testing.T) {
	setWaitTimeout(t, 5*time.Second)
	const chunkSize = 1024
	totalSize := int64(5 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 2)

	writeChunkOK(t, w, data, 0)
	writeChunkOK(t, w, data, 1)

	done := make(chan error, 1)
	go func() {
		_, err := w.WriteChunk(2, bytes.NewReader(chunkOf(data, 2, chunkSize)))
		done <- err
	}()
	select {
	case err := <-done:
		t.Fatalf("chunk 2 should be parked while the window is full, returned %v", err)
	case <-time.After(100 * time.Millisecond):
		// parked, as designed
	}

	// consuming chunk 0 frees its slot and must wake the parked writer
	buf := make([]byte, chunkSize)
	if _, err := io.ReadFull(w, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("parked chunk after slot freed: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("parked chunk never admitted after its slot freed")
	}
}

func TestIdempotentResend(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)

	first := writeChunkOK(t, w, data, 0)
	again := writeChunkOK(t, w, data, 0) // buffered, not yet consumed
	if first != again {
		t.Fatalf("resend crc = %08x, want %08x", again, first)
	}
	buf := make([]byte, chunkSize)
	if _, err := io.ReadFull(w, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	consumed := writeChunkOK(t, w, data, 0) // already consumed
	if consumed != first {
		t.Fatalf("post-consume resend crc = %08x, want %08x", consumed, first)
	}
}

// gatedReader blocks the first Read until released, to hold a chunk in the filling state.
type gatedReader struct {
	release <-chan struct{}
	inner   io.Reader
}

func (g *gatedReader) Read(p []byte) (int, error) {
	<-g.release
	return g.inner.Read(p)
}

func TestInFlightConflict(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)

	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := w.WriteChunk(0, &gatedReader{release: release, inner: bytes.NewReader(chunkOf(data, 0, chunkSize))})
		done <- err
	}()

	// wait until the writer marked the slot as filling
	deadline := time.Now().Add(5 * time.Second)
	for {
		w.mu.Lock()
		filling := w.slotState[0] == slotFilling
		w.mu.Unlock()
		if filling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("writer never reached filling state")
		}
		time.Sleep(time.Millisecond)
	}

	if _, err := w.WriteChunk(0, bytes.NewReader(chunkOf(data, 0, chunkSize))); !errors.Is(err, ErrChunkInFlight) {
		t.Fatalf("concurrent same-chunk write: err = %v, want ErrChunkInFlight", err)
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("gated WriteChunk: %v", err)
	}
	writeChunkOK(t, w, data, 0) // idempotent after settle
}

func TestShortBodyRecovers(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)

	if _, err := w.WriteChunk(0, bytes.NewReader(chunkOf(data, 0, chunkSize)[:100])); err == nil {
		t.Fatal("short body: expected error")
	}
	writeChunkOK(t, w, data, 0) // slot must have been recycled
}

func TestOversizeBodyRejected(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)

	oversize := append(append([]byte{}, chunkOf(data, 0, chunkSize)...), 0xFF)
	if _, err := w.WriteChunk(0, bytes.NewReader(oversize)); err == nil {
		t.Fatal("oversize body: expected error")
	}
	writeChunkOK(t, w, data, 0)

	// the short last chunk must also reject a full-size body
	last := w.TotalChunks() - 1
	if last == 0 {
		t.Fatal("test needs at least 2 chunks")
	}
}

func TestLastChunkShortStrict(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(chunkSize + 100)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)

	if _, err := w.WriteChunk(1, bytes.NewReader(genData(chunkSize))); err == nil {
		t.Fatal("full-size body for short last chunk: expected error")
	}
	writeChunkOK(t, w, data, 1)
	writeChunkOK(t, w, data, 0)
	got := readAllWithin(t, w, 10*time.Second)
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled stream differs")
	}
}

func TestCloseUnblocksReader(t *testing.T) {
	w := newTestWindow(t, 1024, 4096, 4)
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := w.Read(buf)
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = w.Close()
	select {
	case err := <-errCh:
		if !errors.Is(err, ErrClosed) {
			t.Fatalf("blocked Read after Close: err = %v, want ErrClosed", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read still blocked after Close")
	}
}

func TestCloseWithErrorPropagatesSticky(t *testing.T) {
	w := newTestWindow(t, 1024, 4096, 4)
	cause := errors.New("aborted for a reason")
	errCh := make(chan error, 1)
	go func() {
		buf := make([]byte, 16)
		_, err := w.Read(buf)
		errCh <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = w.CloseWithError(cause)
	select {
	case err := <-errCh:
		if !errors.Is(err, cause) {
			t.Fatalf("blocked Read after CloseWithError: err = %v, want %v", err, cause)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Read still blocked after CloseWithError")
	}
	if _, err := w.WriteChunk(0, bytes.NewReader(make([]byte, 1024))); !errors.Is(err, cause) {
		t.Fatalf("WriteChunk after CloseWithError: err = %v, want %v", err, cause)
	}
}

func TestWriteAfterClose(t *testing.T) {
	w := newTestWindow(t, 1024, 4096, 4)
	_ = w.Close()
	if _, err := w.WriteChunk(0, bytes.NewReader(make([]byte, 1024))); !errors.Is(err, ErrClosed) {
		t.Fatalf("WriteChunk after Close: err = %v, want ErrClosed", err)
	}
}

func TestCloseUnblocksInFlightWriter(t *testing.T) {
	const chunkSize = 1024
	data := genData(2 * chunkSize)
	w := newTestWindow(t, chunkSize, 2*chunkSize, 4)

	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := w.WriteChunk(0, &gatedReader{release: release, inner: bytes.NewReader(chunkOf(data, 0, chunkSize))})
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	_ = w.Close()
	close(release)
	if err := <-done; !errors.Is(err, ErrClosed) {
		t.Fatalf("in-flight WriteChunk across Close: err = %v, want ErrClosed", err)
	}
}

func TestEOFExact(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(chunkSize + 5)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)
	writeChunkOK(t, w, data, 0)
	writeChunkOK(t, w, data, 1)

	got := readAllWithin(t, w, 10*time.Second)
	if !bytes.Equal(got, data) {
		t.Fatal("reassembled stream differs")
	}
	buf := make([]byte, 1)
	if n, err := w.Read(buf); n != 0 || err != io.EOF {
		t.Fatalf("Read at EOF = (%d, %v), want (0, io.EOF)", n, err)
	}
}

func TestManyLapsSmallWindow(t *testing.T) {
	const chunkSize = 8 * 1024
	const chunks = 64
	totalSize := int64(chunks*chunkSize - 777)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 3)

	var next atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				idx := int(next.Add(1) - 1)
				if idx >= w.TotalChunks() {
					return
				}
				for {
					_, err := w.WriteChunk(idx, bytes.NewReader(chunkOf(data, idx, chunkSize)))
					if err == nil {
						break
					}
					if errors.Is(err, ErrOutOfWindow) || errors.Is(err, ErrChunkInFlight) {
						time.Sleep(200 * time.Microsecond)
						continue
					}
					t.Errorf("WriteChunk(%d): %v", idx, err)
					return
				}
			}
		}()
	}

	got := readAllWithin(t, w, 30*time.Second)
	wg.Wait()
	if wantSum, gotSum := sha256.Sum256(data), sha256.Sum256(got); wantSum != gotSum {
		t.Fatalf("reassembled stream differs: got %d bytes, want %d", len(got), len(data))
	}
}

func TestSnapshot(t *testing.T) {
	const chunkSize = 1024
	totalSize := int64(5*chunkSize + 512)
	data := genData(totalSize)
	w := newTestWindow(t, chunkSize, totalSize, 4)

	writeChunkOK(t, w, data, 0)
	writeChunkOK(t, w, data, 1)
	writeChunkOK(t, w, data, 3)

	snap := w.Snapshot()
	if snap.Frontier != 0 || snap.ReadPos != 0 {
		t.Fatalf("snapshot frontier/readPos = %d/%d, want 0/0", snap.Frontier, snap.ReadPos)
	}
	wantRanges := [][2]int{{0, 1}, {3, 3}}
	if len(snap.Received) != len(wantRanges) || snap.Received[0] != wantRanges[0] || snap.Received[1] != wantRanges[1] {
		t.Fatalf("snapshot received = %v, want %v", snap.Received, wantRanges)
	}
	if snap.ReceivedBytes != 3*chunkSize {
		t.Fatalf("snapshot receivedBytes = %d, want %d", snap.ReceivedBytes, 3*chunkSize)
	}

	buf := make([]byte, chunkSize)
	if _, err := io.ReadFull(w, buf); err != nil {
		t.Fatalf("ReadFull: %v", err)
	}
	snap = w.Snapshot()
	if snap.Frontier != 1 || snap.ReadPos != chunkSize {
		t.Fatalf("snapshot frontier/readPos = %d/%d, want 1/%d", snap.Frontier, snap.ReadPos, chunkSize)
	}
	if len(snap.Received) != 2 || snap.Received[0] != [2]int{0, 1} || snap.Received[1] != [2]int{3, 3} {
		t.Fatalf("snapshot received = %v, want [[0,1],[3,3]]", snap.Received)
	}
	if snap.ReceivedBytes != 3*chunkSize {
		t.Fatalf("snapshot receivedBytes = %d, want %d", snap.ReceivedBytes, 3*chunkSize)
	}
}

func TestIndexOutOfRange(t *testing.T) {
	w := newTestWindow(t, 1024, 4096, 4)
	if _, err := w.WriteChunk(-1, bytes.NewReader(nil)); err == nil {
		t.Fatal("negative index: expected error")
	}
	if _, err := w.WriteChunk(4, bytes.NewReader(nil)); err == nil {
		t.Fatal("index == total: expected error")
	}
}

func TestCRCsSurviveClose(t *testing.T) {
	const chunkSize = 1024
	data := genData(2 * chunkSize)
	w := newTestWindow(t, chunkSize, 2*chunkSize, 4)
	want := writeChunkOK(t, w, data, 0)
	_ = w.Close()
	crcs, set := w.CRCs()
	if !set[0] || crcs[0] != want {
		t.Fatalf("CRCs after Close = (%08x, %v), want (%08x, true)", crcs[0], set[0], want)
	}
	if set[1] {
		t.Fatal("chunk 1 crc should not be set")
	}
}
