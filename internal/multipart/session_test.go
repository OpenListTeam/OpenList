package multipart

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// Tests in this file share the putFile seam and must not run in parallel.

func setupSessionTest(t *testing.T) *Manager {
	t.Helper()
	oldConf := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = oldConf })
	return &Manager{byID: make(map[string]*Session), byKey: make(map[string]string)}
}

func stubPut(t *testing.T, fn func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error) {
	t.Helper()
	orig := putFile
	putFile = fn
	t.Cleanup(func() { putFile = orig })
}

func testUser() *model.User { return &model.User{ID: 7, Username: "tester"} }

func initReq(user *model.User, size, chunkSize int64) InitReq {
	return InitReq{
		User:      user,
		Path:      "/local/test.bin",
		Size:      size,
		ChunkSize: chunkSize,
		Mimetype:  "application/octet-stream",
		Modified:  time.Unix(1700000000, 0),
	}
}

func sendChunk(t *testing.T, m *Manager, user *model.User, id string, data []byte, idx int, chunkSize int64) SessionSnapshot {
	t.Helper()
	snap, err := m.Chunk(user, id, idx, bytes.NewReader(chunkOf(data, idx, chunkSize)))
	if err != nil {
		t.Fatalf("Chunk(%d): %v", idx, err)
	}
	return snap
}

func waitState(t *testing.T, m *Manager, user *model.User, id string, want State) SessionSnapshot {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		snap, err := m.Status(user, id)
		if err != nil {
			t.Fatalf("Status: %v", err)
		}
		if snap.State == want {
			return snap
		}
		if time.Now().After(deadline) {
			t.Fatalf("session state = %s, want %s (err: %s)", snap.State, want, snap.Error)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func TestSessionHappyPath(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(3*chunkSize + 300)
	data := genData(totalSize)
	user := testUser()

	got := make(chan []byte, 1)
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		if dst != "/local/" {
			return fmt.Errorf("unexpected dst dir %q", dst)
		}
		if fs.GetName() != "test.bin" || fs.GetSize() != totalSize {
			return fmt.Errorf("unexpected stream meta %s/%d", fs.GetName(), fs.GetSize())
		}
		b, err := io.ReadAll(fs)
		if err != nil {
			return err
		}
		up(100)
		got <- b
		return nil
	})

	snap, resumed, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil || resumed {
		t.Fatalf("Init = (resumed=%v, err=%v)", resumed, err)
	}
	if snap.TotalChunks != 4 || snap.State != StateReceiving {
		t.Fatalf("init snapshot = %+v", snap)
	}
	for _, idx := range []int{1, 0, 3, 2} { // out of order on purpose
		sendChunk(t, m, user, snap.ID, data, idx, chunkSize)
	}
	final, err := m.Complete(context.Background(), user, snap.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if final.State != StateCompleted || final.StorageProgress != 100 {
		t.Fatalf("final snapshot = %+v", final)
	}
	if !bytes.Equal(<-got, data) {
		t.Fatal("driver received different bytes")
	}
	if _, err := m.Status(user, snap.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("session should be removed after Complete, got %v", err)
	}
}

func TestSessionRetriableRefill(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	attempts := 0
	got := make(chan []byte, 1)
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		attempts++
		if attempts == 1 {
			buf := make([]byte, chunkSize)
			if _, err := io.ReadFull(fs, buf); err != nil {
				return err
			}
			return errors.New("transient storage hiccup")
		}
		b, err := io.ReadAll(fs)
		if err != nil {
			return err
		}
		got <- b
		return nil
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	failed := waitState(t, m, user, snap.ID, StateFailedRetriable)
	if failed.Attempt != 1 {
		t.Fatalf("attempt = %d, want 1", failed.Attempt)
	}
	if len(failed.Received) != 0 {
		t.Fatalf("failed session must report nothing received, got %v", failed.Received)
	}

	// chunks other than 0 are rejected until the client restarts the fill
	if _, err := m.Chunk(user, snap.ID, 1, bytes.NewReader(chunkOf(data, 1, chunkSize))); err == nil {
		t.Fatal("chunk 1 on failed_retriable session: expected error")
	}
	// re-fill from chunk 0 respawns the pipeline
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	sendChunk(t, m, user, snap.ID, data, 1, chunkSize)
	final, err := m.Complete(context.Background(), user, snap.ID)
	if err != nil {
		t.Fatalf("Complete after refill: %v", err)
	}
	if final.State != StateCompleted || attempts != 2 {
		t.Fatalf("state=%s attempts=%d, want completed/2", final.State, attempts)
	}
	if !bytes.Equal(<-got, data) {
		t.Fatal("driver received different bytes after refill")
	}
}

func TestSessionPermanentFailure(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	data := genData(chunkSize)
	user := testUser()

	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		return fmt.Errorf("denied: %w", errs.PermissionDenied)
	})

	snap, _, err := m.Init(initReq(user, chunkSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	waitState(t, m, user, snap.ID, StateFailedPermanent)
	if _, err := m.Chunk(user, snap.ID, 0, bytes.NewReader(data)); err == nil {
		t.Fatal("chunk on failed_permanent session: expected error")
	}
	if _, err := m.Complete(context.Background(), user, snap.ID); err == nil {
		t.Fatal("Complete on failed_permanent session: expected error")
	}
}

func TestRefillCRCMismatch(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	attempts := 0
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		attempts++
		if attempts == 1 {
			buf := make([]byte, chunkSize)
			if _, err := io.ReadFull(fs, buf); err != nil {
				return err
			}
			return errors.New("transient")
		}
		_, err := io.ReadAll(fs)
		return err
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	waitState(t, m, user, snap.ID, StateFailedRetriable)

	tampered := genData(chunkSize + 5)[:chunkSize] // different content, same length
	if _, err := m.Chunk(user, snap.ID, 0, bytes.NewReader(tampered)); err == nil {
		t.Fatal("re-fill with changed content: expected error")
	}
	waitState(t, m, user, snap.ID, StateFailedPermanent)
}

func TestCompleteRefusesIncomplete(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(3 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		_, err := io.ReadAll(fs)
		return err
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	if _, err := m.Complete(context.Background(), user, snap.ID); err == nil {
		t.Fatal("Complete with missing chunks: expected error")
	}
	// unblock the pipeline goroutine before the test tears down
	if err := m.Abort(user, snap.ID); err != nil {
		t.Fatalf("Abort: %v", err)
	}
}

func TestAbortAndOwnership(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(4 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		_, err := io.ReadAll(fs)
		return err
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)

	stranger := &model.User{ID: 99, Username: "stranger"}
	if _, err := m.Status(stranger, snap.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("stranger Status err = %v, want ErrNotOwner", err)
	}
	if err := m.Abort(stranger, snap.ID); !errors.Is(err, ErrNotOwner) {
		t.Fatalf("stranger Abort err = %v, want ErrNotOwner", err)
	}
	if err := m.Abort(user, snap.ID); err != nil {
		t.Fatalf("Abort: %v", err)
	}
	if _, err := m.Status(user, snap.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Status after Abort err = %v, want ErrSessionNotFound", err)
	}
}

func TestExpiry(t *testing.T) {
	m := setupSessionTest(t)
	m.ttl = 30 * time.Millisecond

	const chunkSize = 1024
	user := testUser()
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		_, err := io.ReadAll(fs)
		return err
	})

	snap, _, err := m.Init(initReq(user, 4*chunkSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	time.Sleep(60 * time.Millisecond)
	m.gc()
	if _, err := m.Status(user, snap.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Status after expiry err = %v, want ErrSessionNotFound", err)
	}
	// the terminated pipeline must release the ring file
	deadline := time.Now().Add(5 * time.Second)
	for {
		entries, _ := os.ReadDir(m.dir())
		if len(entries) == 0 {
			break
		}
		if time.Now().After(deadline) {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Fatalf("ring files not cleaned up after expiry: %v", names)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestInitResume(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(4 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		_, err := io.ReadAll(fs)
		return err
	})

	hashed := initReq(user, totalSize, chunkSize)
	hashed.Hashes = map[*utils.HashType]string{utils.MD5: "0123456789abcdef0123456789abcdef"}
	snap, _, err := m.Init(hashed)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	m.mu.Lock()
	firstSession := m.byID[snap.ID]
	m.mu.Unlock()
	sessions := []*Session{firstSession}
	t.Cleanup(func() {
		for _, session := range sessions {
			if session == nil {
				continue
			}
			m.terminate(session, context.Canceled)
			<-session.done
		}
	})
	sendChunk(t, m, user, snap.ID, data, 2, chunkSize)

	// same hashes prove the same file: resume with buffered chunks skippable
	again, resumed, err := m.Init(hashed)
	if err != nil || !resumed {
		t.Fatalf("hashed re-Init = (resumed=%v, err=%v), want resumed", resumed, err)
	}
	if again.ID != snap.ID {
		t.Fatalf("resumed session id = %s, want %s", again.ID, snap.ID)
	}
	if len(again.Received) != 1 || again.Received[0] != [2]int{2, 2} {
		t.Fatalf("resumed received = %v, want [[2,2]]", again.Received)
	}

	if _, err := m.Find(user, "/local/test.bin", totalSize); err != nil {
		t.Fatalf("Find: %v", err)
	}
	if _, err := m.Find(user, "/local/nope.bin", totalSize); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Find miss err = %v, want ErrSessionNotFound", err)
	}

	// a different size is a different file, which replaces the upload in
	// progress: one upload writes a destination at a time
	other, resumed, err := m.Init(initReq(user, totalSize+1, chunkSize))
	if err != nil || resumed || other.ID == snap.ID {
		t.Fatalf("different-size Init = (id=%s, resumed=%v, err=%v)", other.ID, resumed, err)
	}
	m.mu.Lock()
	sessions = append(sessions, m.byID[other.ID])
	m.mu.Unlock()
	if _, err := m.Find(user, "/local/test.bin", totalSize); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("Find of the replaced upload err = %v, want ErrSessionNotFound", err)
	}
	if _, err := m.Find(user, "/local/test.bin", totalSize+1); err != nil {
		t.Fatalf("Find: %v", err)
	}

	// without hashes, path+size cannot prove identity against a receiving
	// session holding buffered data — the old session must be dropped, or a
	// same-sized different file would be silently mixed into the result
	bare := initReq(user, totalSize, chunkSize)
	fresh, resumed, err := m.Init(bare)
	if err != nil || resumed || fresh.ID == snap.ID {
		t.Fatalf("bare re-Init = (id=%s, resumed=%v, err=%v), want a fresh session", fresh.ID, resumed, err)
	}
	m.mu.Lock()
	sessions = append(sessions, m.byID[fresh.ID])
	m.mu.Unlock()
	if _, err := m.Status(user, snap.ID); !errors.Is(err, ErrSessionNotFound) {
		t.Fatalf("superseded session err = %v, want ErrSessionNotFound", err)
	}
}

func TestResumeFailedRetriableWithoutHash(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	attempts := 0
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		attempts++
		if attempts == 1 {
			buf := make([]byte, chunkSize)
			if _, err := io.ReadFull(fs, buf); err != nil {
				return err
			}
			return errors.New("transient")
		}
		_, err := io.ReadAll(fs)
		return err
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	waitState(t, m, user, snap.ID, StateFailedRetriable)

	// a failed_retriable session keeps no chunk data, so identity proof is not
	// required to resume: the retry-button flow works without rapid hashing,
	// and the re-fill CRC check still catches a changed file
	again, resumed, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil || !resumed || again.ID != snap.ID {
		t.Fatalf("re-Init on failed_retriable = (id=%s, resumed=%v, err=%v), want resumed same session", again.ID, resumed, err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	sendChunk(t, m, user, snap.ID, data, 1, chunkSize)
	if _, err := m.Complete(context.Background(), user, snap.ID); err != nil {
		t.Fatalf("Complete after hashless refill: %v", err)
	}
}

func TestRapidUploadShortCircuit(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(6 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	wantMD5 := "0123456789abcdef0123456789abcdef"
	wantSHA1 := "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		// the hash provided at init must reach the driver through the stream,
		// that is what lets PutRapid-style drivers skip the transfer entirely
		if got := fs.GetHash().GetHash(utils.MD5); got != wantMD5 {
			return fmt.Errorf("md5 not propagated: %q", got)
		}
		if got := fs.GetHash().GetHash(utils.SHA1); got != wantSHA1 {
			return fmt.Errorf("sha1 not propagated: %q", got)
		}
		time.Sleep(50 * time.Millisecond) // simulated rapid-upload API round trip
		up(100)
		return nil // rapid upload hit: succeed without reading the stream
	})

	req := initReq(user, totalSize, chunkSize)
	req.Hashes = map[*utils.HashType]string{utils.MD5: wantMD5, utils.SHA1: wantSHA1}
	snap, _, err := m.Init(req)
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	// spam chunks across the completion moment: some land while receiving,
	// some race the window close, some arrive after completion — with the
	// completed-session absorption none of them may surface an error
	var wg sync.WaitGroup
	errCh := make(chan error, 64)
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func(w int) {
			defer wg.Done()
			for round := 0; round < 8; round++ {
				idx := (w*8 + round) % 6
				_, err := m.Chunk(user, snap.ID, idx, bytes.NewReader(chunkOf(data, idx, chunkSize)))
				// flow-control signals are part of the protocol, not failures
				if err != nil && !errors.Is(err, ErrChunkInFlight) && !errors.Is(err, ErrOutOfWindow) {
					errCh <- fmt.Errorf("worker %d chunk %d: %w", w, idx, err)
					return
				}
				time.Sleep(5 * time.Millisecond)
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}

	final, err := m.Complete(context.Background(), user, snap.ID)
	if err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if final.State != StateCompleted || final.StorageProgress != 100 {
		t.Fatalf("final snapshot = %+v", final)
	}
}

// TestChunkRacesRapidCompletion pins the exact race the completed-session
// absorption exists for: a chunk request grabs the live window, stalls while
// receiving its body, the pipeline completes off the hash alone (rapid
// upload) and closes the window — the stalled chunk must then succeed
// idempotently instead of surfacing "window closed" to a client whose upload
// in fact just finished.
func TestChunkRacesRapidCompletion(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	proceed := make(chan struct{})
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		<-proceed // the rapid-upload verdict arrives when the test says so
		return nil
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}

	gate := make(chan struct{})
	type result struct {
		snap SessionSnapshot
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		s, e := m.Chunk(user, snap.ID, 0,
			&gatedReader{release: gate, inner: bytes.NewReader(chunkOf(data, 0, chunkSize))})
		resCh <- result{s, e}
	}()

	// wait until the chunk writer holds slot 0 in the filling state
	m.mu.Lock()
	sess := m.byID[snap.ID]
	m.mu.Unlock()
	sess.mu.Lock()
	win := sess.win
	sess.mu.Unlock()
	deadline := time.Now().Add(5 * time.Second)
	for {
		win.mu.Lock()
		filling := win.slotState[0] == slotFilling
		win.mu.Unlock()
		if filling {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("chunk writer never reached the filling state")
		}
		time.Sleep(time.Millisecond)
	}

	close(proceed)                                 // rapid upload succeeds, window closes
	waitState(t, m, user, snap.ID, StateCompleted) // completion recorded
	close(gate)                                    // stalled chunk finishes against the closed window

	res := <-resCh
	if res.err != nil {
		t.Fatalf("in-flight chunk across rapid completion must be absorbed, got: %v", res.err)
	}
	if res.snap.State != StateCompleted {
		t.Fatalf("absorbed chunk snapshot state = %s, want completed", res.snap.State)
	}
}

// TestChunkDuringCompletionGap covers the moment between op.Put closing the
// window (its defer) and the verdict being recorded: a chunk hitting the
// closed window inside that gap must wait for the verdict and be absorbed,
// not bounce a "window closed" error at a client whose upload just succeeded.
func TestChunkDuringCompletionGap(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	windowClosed := make(chan struct{})
	allowVerdict := make(chan struct{})
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		_ = fs.Close() // what op.Put's defer does before Put returns
		close(windowClosed)
		<-allowVerdict // hold the pipeline return open: this IS the gap
		return nil
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	<-windowClosed

	type result struct {
		snap SessionSnapshot
		err  error
	}
	resCh := make(chan result, 1)
	go func() {
		s, e := m.Chunk(user, snap.ID, 0, bytes.NewReader(chunkOf(data, 0, chunkSize)))
		resCh <- result{s, e}
	}()

	select {
	case r := <-resCh:
		t.Fatalf("chunk inside the gap returned early with (%s, %v); it must wait for the verdict", r.snap.State, r.err)
	case <-time.After(150 * time.Millisecond):
		// still waiting on the verdict, as designed
	}

	close(allowVerdict)
	select {
	case r := <-resCh:
		if r.err != nil {
			t.Fatalf("gap chunk must be absorbed after completion, got: %v", r.err)
		}
		if r.snap.State != StateCompleted {
			t.Fatalf("gap chunk snapshot state = %s, want completed", r.snap.State)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("gap chunk never returned after the verdict")
	}
}

func TestInitRejectsBadSize(t *testing.T) {
	m := setupSessionTest(t)
	if _, _, err := m.Init(initReq(testUser(), 0, 1024)); err == nil {
		t.Fatal("Init with size 0: expected error")
	}
}

// A panicking driver must fail its upload attempt, not crash the server: the
// pipeline runs the driver on its own goroutine, outside gin's Recovery and
// the task workers' recover (see #3201 for such a driver panic).
func TestDriverPanicFailsTheAttempt(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()

	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		if _, err := io.ReadAll(fs); err != nil {
			return err
		}
		var result *struct{ FileID string } // the driver got a nil upload result
		return fmt.Errorf("uploaded %s", result.FileID)
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	sendChunk(t, m, user, snap.ID, data, 1, chunkSize)

	final, err := m.Complete(context.Background(), user, snap.ID)
	if err == nil || !strings.Contains(err.Error(), "upload pipeline panicked") {
		t.Fatalf("Complete err = %v, want the recovered panic", err)
	}
	if final.State != StateFailedRetriable {
		t.Fatalf("state = %s, want %s", final.State, StateFailedRetriable)
	}
}

// When the storage fails, a chunk caught in flight by the closing window must
// report the storage's error, not the generic "window closed" (#3309).
func TestChunkReportsStorageError(t *testing.T) {
	m := setupSessionTest(t)
	oldSlots := WindowSlots
	WindowSlots = 1
	t.Cleanup(func() { WindowSlots = oldSlots })
	const chunkSize = 1024
	totalSize := int64(4 * chunkSize)
	data := genData(totalSize)
	user := testUser()
	cause := errors.New("upload part 7 rejected by the storage")

	halfRead := make(chan struct{})
	fail := make(chan struct{})
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		buf := make([]byte, chunkSize/2)
		if _, err := io.ReadFull(fs, buf); err != nil {
			return err
		}
		close(halfRead)
		<-fail
		return cause
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	<-halfRead
	type result struct {
		snap SessionSnapshot
		err  error
	}
	parked := make(chan result, 1)
	go func() {
		// chunk 0 still holds the only slot (half read), so chunk 1 waits
		s, err := m.Chunk(user, snap.ID, 1, bytes.NewReader(chunkOf(data, 1, chunkSize)))
		parked <- result{s, err}
	}()
	time.Sleep(200 * time.Millisecond)
	close(fail)

	r := <-parked
	if r.err == nil {
		t.Fatal("chunk 1 succeeded on a failed upload")
	}
	if errors.Is(r.err, ErrClosed) || !strings.Contains(r.err.Error(), cause.Error()) {
		t.Fatalf("chunk error = %q, want it to carry the storage error %q", r.err, cause)
	}
}

func setAttemptIdleTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := AttemptIdleTimeout
	AttemptIdleTimeout = d
	t.Cleanup(func() { AttemptIdleTimeout = old })
}

// readAllPut is a driver that consumes the whole stream before succeeding.
func readAllPut(got chan<- []byte) func(context.Context, string, *stream.FileStream, driver.UpdateProgress) error {
	return func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		b, err := io.ReadAll(fs)
		if err != nil {
			return err
		}
		if got != nil {
			got <- b
		}
		return nil
	}
}

// An attempt whose client went silent is released after AttemptIdleTimeout,
// so it stops holding the destination for the whole session TTL; the session
// becomes failed_retriable and resumes through the refill protocol. The
// refill must not start before the released attempt has let go.
func TestIdleAttemptIsReleased(t *testing.T) {
	m := setupSessionTest(t)
	setAttemptIdleTimeout(t, 50*time.Millisecond)
	const chunkSize = 1024
	totalSize := int64(3 * chunkSize)
	data := genData(totalSize)
	user := testUser()
	got := make(chan []byte, 1)
	var running, overlaps atomic.Int32
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		if running.Add(1) > 1 {
			overlaps.Add(1)
		}
		defer running.Add(-1)
		b, err := io.ReadAll(fs)
		if err != nil {
			time.Sleep(50 * time.Millisecond) // a driver takes a moment to unwind
			return err
		}
		got <- b
		return nil
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 0, chunkSize)
	time.Sleep(100 * time.Millisecond) // the client goes silent
	m.gc()
	released := waitState(t, m, user, snap.ID, StateFailedRetriable)
	if !strings.Contains(released.Error, "no data from the client") {
		t.Fatalf("released session error = %q, want it to say why", released.Error)
	}

	// the client comes back and refills from chunk 0
	again, resumed, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil || !resumed || again.ID != snap.ID {
		t.Fatalf("re-Init = (id=%s, resumed=%v, err=%v), want the released session resumed", again.ID, resumed, err)
	}
	for idx := 0; idx < 3; idx++ {
		sendChunk(t, m, user, snap.ID, data, idx, chunkSize)
	}
	final, err := m.Complete(context.Background(), user, snap.ID)
	if err != nil || final.State != StateCompleted {
		t.Fatalf("Complete after refill = (%s, %v), want completed", final.State, err)
	}
	if b := <-got; !bytes.Equal(b, data) {
		t.Fatal("the refilled upload delivered different content")
	}
	if n := overlaps.Load(); n != 0 {
		t.Fatalf("the refill attempt overlapped the released one %d time(s)", n)
	}
}

// tricklingReader yields one byte per read, every interval.
type tricklingReader struct {
	data     []byte
	interval time.Duration
}

func (r *tricklingReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	time.Sleep(r.interval)
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

// A slow client that keeps sending is not idle, however long one chunk takes.
func TestSlowClientIsNotReleased(t *testing.T) {
	m := setupSessionTest(t)
	setAttemptIdleTimeout(t, 50*time.Millisecond)
	const chunkSize = 32
	totalSize := int64(2 * chunkSize)
	data := genData(totalSize)
	user := testUser()
	stubPut(t, readAllPut(nil))

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	sent := make(chan error, 1)
	go func() {
		_, err := m.Chunk(user, snap.ID, 0, &tricklingReader{data: chunkOf(data, 0, chunkSize), interval: 10 * time.Millisecond})
		sent <- err
	}()
	for i := 0; i < 12; i++ { // ~240ms of a 320ms trickle
		time.Sleep(20 * time.Millisecond)
		m.gc()
		if s, err := m.Status(user, snap.ID); err != nil || s.State != StateReceiving {
			t.Fatalf("a client that was still sending got released (state %s, err %v)", s.State, err)
		}
	}
	if err := <-sent; err != nil {
		t.Fatalf("trickled chunk: %v", err)
	}
	sendChunk(t, m, user, snap.ID, data, 1, chunkSize)
	if _, err := m.Complete(context.Background(), user, snap.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

// A storage that is slow to consume is backpressure, not an idle client:
// chunks parked behind a full window must not get the attempt released.
func TestBackpressureIsNotIdle(t *testing.T) {
	m := setupSessionTest(t)
	setAttemptIdleTimeout(t, 50*time.Millisecond)
	const chunkSize = 1024
	slots := WindowSlots
	totalSize := int64((slots + 2) * chunkSize)
	data := genData(totalSize)
	user := testUser()
	release := make(chan struct{})
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		<-release // the storage is busy and not reading yet
		_, err := io.ReadAll(fs)
		return err
	})

	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	for idx := 0; idx < slots; idx++ {
		sendChunk(t, m, user, snap.ID, data, idx, chunkSize)
	}
	time.Sleep(100 * time.Millisecond)
	m.gc()
	if s, err := m.Status(user, snap.ID); err != nil || s.State != StateReceiving {
		t.Fatalf("an attempt waiting on the storage got released (state %s, err %v)", s.State, err)
	}
	close(release)
	for idx := slots; idx < slots+2; idx++ {
		sendChunk(t, m, user, snap.ID, data, idx, chunkSize)
	}
	if _, err := m.Complete(context.Background(), user, snap.ID); err != nil {
		t.Fatalf("Complete: %v", err)
	}
}

func setSupersedeJoinTimeout(t *testing.T, d time.Duration) {
	t.Helper()
	old := supersedeJoinTimeout
	supersedeJoinTimeout = d
	t.Cleanup(func() { supersedeJoinTimeout = old })
}

// attemptLog records when pipeline attempts start and return, and how many
// write the destination at once.
type attemptLog struct {
	mu     sync.Mutex
	events []string
	active int
	peak   int
	n      int
}

func (l *attemptLog) start() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	l.active++
	l.peak = max(l.peak, l.active)
	l.events = append(l.events, fmt.Sprintf("start#%d", l.n))
	return l.n
}

func (l *attemptLog) end(n int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.active--
	l.events = append(l.events, fmt.Sprintf("return#%d", n))
}

func (l *attemptLog) snapshot() ([]string, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.events...), l.peak
}

func (l *attemptLog) waitFor(t *testing.T, event string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		events, _ := l.snapshot()
		for _, e := range events {
			if e == event {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("no %s in %v", event, events)
		}
		time.Sleep(time.Millisecond)
	}
}

// stubSlowFirstAttempt makes the first attempt hold on to the destination
// until release is called, after its cancellation unless deaf, as a driver
// still unwinding or stuck on I/O would. Later attempts return once canceled.
func stubSlowFirstAttempt(t *testing.T, log *attemptLog, deaf bool) (running <-chan struct{}, release func()) {
	t.Helper()
	started := make(chan struct{})
	gate := make(chan struct{})
	var once sync.Once
	release = func() { once.Do(func() { close(gate) }) }
	t.Cleanup(release)
	stubPut(t, func(ctx context.Context, dst string, fs *stream.FileStream, up driver.UpdateProgress) error {
		defer fs.Close()
		n := log.start()
		defer log.end(n)
		if n == 1 {
			close(started)
			if !deaf {
				<-ctx.Done()
			}
			<-gate
			return ctx.Err()
		}
		<-ctx.Done()
		return ctx.Err()
	})
	return started, release
}

// The key of a superseded session stays taken until its attempt returns: an
// init arriving meanwhile waits too, instead of starting an upload next to it.
func TestSupersedeKeepsTheKeyUntilTheOldAttemptReturns(t *testing.T) {
	m := setupSessionTest(t)
	const chunkSize = 1024
	totalSize := int64(4 * chunkSize)
	user := testUser()
	var log attemptLog
	running, release := stubSlowFirstAttempt(t, &log, false)

	if _, _, err := m.Init(initReq(user, totalSize, chunkSize)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	<-running
	// two more inits of the same path and size, neither proving the same file
	type result struct {
		snap SessionSnapshot
		err  error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() {
			snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
			results <- result{snap, err}
		}()
	}
	time.Sleep(100 * time.Millisecond)
	if events, _ := log.snapshot(); len(events) != 1 {
		t.Fatalf("events = %v while the superseded attempt still runs, want no new attempt", events)
	}
	release()
	var ids []string
	for range 2 {
		r := <-results
		if r.err != nil {
			t.Fatalf("Init: %v", r.err)
		}
		ids = append(ids, r.snap.ID)
	}
	t.Cleanup(func() {
		for _, id := range ids {
			_ = m.Abort(user, id)
		}
	})
	log.waitFor(t, "start#3") // every attempt started, so the peak is final
	if events, peak := log.snapshot(); peak != 1 {
		t.Fatalf("events = %v: %d attempts wrote the destination at once", events, peak)
	}
	// the later init superseded the earlier one: a single session holds the key
	found, err := m.Find(user, "/local/test.bin", totalSize)
	if err != nil {
		t.Fatalf("Find: %v", err)
	}
	var live int
	for _, id := range ids {
		if _, err := m.Status(user, id); err == nil {
			live++
			if id != found.ID {
				t.Fatalf("live session %s does not hold the key (%s does)", id, found.ID)
			}
		}
	}
	if live != 1 {
		t.Fatalf("%d live sessions for one path and size, want 1", live)
	}
}

// An init gives up with a retriable error rather than start next to an attempt
// that ignores its cancellation, and the key stays taken for the next init
// until that attempt returns.
func TestSupersedeGivesUpWhileTheOldAttemptRuns(t *testing.T) {
	m := setupSessionTest(t)
	setSupersedeJoinTimeout(t, 100*time.Millisecond)
	const chunkSize = 1024
	totalSize := int64(4 * chunkSize)
	user := testUser()
	var log attemptLog
	running, release := stubSlowFirstAttempt(t, &log, true)

	if _, _, err := m.Init(initReq(user, totalSize, chunkSize)); err != nil {
		t.Fatalf("Init: %v", err)
	}
	<-running
	for range 2 {
		if snap, _, err := m.Init(initReq(user, totalSize, chunkSize)); !errors.Is(err, ErrPreviousUploadRunning) {
			_ = m.Abort(user, snap.ID)
			t.Fatalf("Init = %v, want ErrPreviousUploadRunning", err)
		}
	}
	if events, _ := log.snapshot(); len(events) != 1 {
		t.Fatalf("events = %v, want no attempt next to the running one", events)
	}
	release()
	log.waitFor(t, "return#1")
	snap, _, err := m.Init(initReq(user, totalSize, chunkSize))
	if err != nil {
		t.Fatalf("Init once the old attempt returned: %v", err)
	}
	t.Cleanup(func() { _ = m.Abort(user, snap.ID) })
	log.waitFor(t, "start#2")
	if events, peak := log.snapshot(); peak != 1 {
		t.Fatalf("events = %v: %d attempts wrote the destination at once", events, peak)
	}
	// and nothing is left holding a key
	deadline := time.Now().Add(time.Second)
	for {
		m.mu.Lock()
		n := len(m.retiring)
		m.mu.Unlock()
		if n == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("%d sessions still retiring after their attempts returned", n)
		}
		time.Sleep(time.Millisecond)
	}
}

// A negative chunk index is the client's mistake. It must be refused, not
// panic while the session lock is held: that would wedge the session and,
// through it, every later init of the same file and the garbage collector.
func TestChunkRejectsNegativeIndex(t *testing.T) {
	m := setupSessionTest(t)
	user := testUser()
	stubPut(t, readAllPut(nil))
	snap, _, err := m.Init(initReq(user, 2048, 1024))
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = m.Abort(user, snap.ID) })
	refused := make(chan error, 1)
	go func() { // a panic here ends the test binary at once
		_, err := m.Chunk(user, snap.ID, -1, bytes.NewReader(nil))
		refused <- err
	}()
	if err := <-refused; err == nil {
		t.Fatal("Chunk(-1) succeeded")
	}
	// the session still takes chunks
	sendChunk(t, m, user, snap.ID, genData(2048), 0, 1024)
}

// One upload writes a destination at a time, whatever its size or user: an init
// that does not resume supersedes every other session for the same path, and
// starts only once their attempts let go of the destination. Otherwise a
// dropped or abandoned upload of another size, cleaning up after its cancel,
// could delete the file that replaced it.
func TestOneUploadPerDestination(t *testing.T) {
	const chunkSize = 1024
	for _, tc := range []struct {
		name  string
		abort bool        // the client cancels the first upload first
		user  *model.User // who starts the second upload
	}{
		{name: "aborted, other size", abort: true},
		{name: "live, other size"},
		{name: "live, other user", user: &model.User{ID: 8, Username: "other"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := setupSessionTest(t)
			user := testUser()
			var log attemptLog
			running, release := stubSlowFirstAttempt(t, &log, false)

			first, _, err := m.Init(initReq(user, 4*chunkSize, chunkSize))
			if err != nil {
				t.Fatalf("Init: %v", err)
			}
			<-running
			if tc.abort {
				if err := m.Abort(user, first.ID); err != nil {
					t.Fatalf("Abort: %v", err)
				}
			}
			second := user
			size := int64(3 * chunkSize)
			if tc.user != nil {
				second, size = tc.user, 4*chunkSize
			}
			done := make(chan SessionSnapshot, 1)
			go func() {
				snap, _, err := m.Init(initReq(second, size, chunkSize))
				if err != nil {
					t.Errorf("Init: %v", err)
				}
				done <- snap
			}()
			time.Sleep(100 * time.Millisecond)
			if events, _ := log.snapshot(); len(events) != 1 {
				t.Fatalf("events = %v while the first upload's attempt still runs, want no new attempt", events)
			}
			if _, err := m.Status(user, first.ID); !errors.Is(err, ErrSessionNotFound) {
				t.Fatalf("Status of the first upload = %v, want it superseded", err)
			}
			release()
			snap := <-done
			t.Cleanup(func() { _ = m.Abort(second, snap.ID) })
			log.waitFor(t, "start#2")
			if events, peak := log.snapshot(); peak != 1 {
				t.Fatalf("events = %v: %d attempts wrote the destination at once", events, peak)
			}
		})
	}
}
