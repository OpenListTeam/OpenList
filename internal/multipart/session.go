package multipart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	stdpath "path"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/google/uuid"
)

type State string

const (
	StateReceiving       State = "receiving"
	StateCompleted       State = "completed"
	StateFailedRetriable State = "failed_retriable"
	StateFailedPermanent State = "failed_permanent"
	StateAborted         State = "aborted"
)

// WindowSlots bounds the per-session disk footprint to WindowSlots*ChunkSize.
var WindowSlots = 8

const (
	// defaultSessionTTL is the sliding inactivity timeout; it also serves as the
	// grace period during which finished sessions remain queryable.
	defaultSessionTTL = 30 * time.Minute
	gcInterval        = time.Minute
)

var (
	ErrSessionNotFound = errors.New("multipart upload session not found")
	ErrNotOwner        = errors.New("multipart upload session belongs to another user")
	// errAborted wraps context.Canceled so a driver blocked on the stream sees
	// the abort as a canceled request and runs its cancellation cleanup.
	errAborted = fmt.Errorf("multipart upload aborted: %w", context.Canceled)
)

// putFile is the pipeline tail: resolve the storage and run the regular upload
// path. It mirrors the checks of fs.putDirectly (internal/fs/put.go) but calls
// op.Put directly so the driver's progress callback can be observed.
// It is a variable so session tests can stub the storage layer out.
var putFile = func(ctx context.Context, dstDirPath string, fs *stream.FileStream, up driver.UpdateProgress) error {
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(dstDirPath)
	if err != nil {
		return err
	}
	if storage.Config().NoUpload {
		return errs.UploadNotSupported
	}
	return op.Put(ctx, storage, dstDirActualPath, fs, up)
}

// Session is one multipart upload: metadata survives pipeline attempts, the
// window (chunk data) does not.
type Session struct {
	ID        string
	Path      string // full destination path (dir + name), already user-joined
	DstDir    string
	Name      string
	Size      int64
	ChunkSize int64
	Total     int
	Mimetype  string
	Modified  time.Time
	Hashes    map[*utils.HashType]string
	Overwrite bool
	Creator   *model.User

	mu       sync.Mutex
	state    State
	err      error
	attempt  int
	win      *Window
	done     chan struct{}
	cancel   context.CancelFunc
	prevCRCs []uint32
	prevSet  []bool

	storagePct atomic.Uint64 // math.Float64bits of the driver progress (0-100)
	lastActive atomic.Int64  // unix nano
}

func (s *Session) touch() { s.lastActive.Store(time.Now().UnixNano()) }

func (s *Session) setStoragePct(p float64) { s.storagePct.Store(math.Float64bits(p)) }

// Snapshot is the wire representation of a session used by all endpoints.
type SessionSnapshot struct {
	ID              string   `json:"upload_id"`
	State           State    `json:"state"`
	Attempt         int      `json:"attempt"`
	Path            string   `json:"path"`
	Size            int64    `json:"size"`
	ChunkSize       int64    `json:"chunk_size"`
	TotalChunks     int      `json:"total_chunks"`
	Received        [][2]int `json:"received"`
	ReceivedBytes   int64    `json:"received_bytes"`
	Frontier        int      `json:"frontier"`
	StorageProgress float64  `json:"storage_progress"`
	Error           string   `json:"error,omitempty"`
}

func (s *Session) snapshotLocked() SessionSnapshot {
	snap := SessionSnapshot{
		ID:              s.ID,
		State:           s.state,
		Attempt:         s.attempt,
		Path:            s.Path,
		Size:            s.Size,
		ChunkSize:       s.ChunkSize,
		TotalChunks:     s.Total,
		Received:        [][2]int{},
		StorageProgress: math.Float64frombits(s.storagePct.Load()),
	}
	if s.err != nil {
		snap.Error = s.err.Error()
	}
	switch {
	case s.state == StateCompleted:
		snap.Received = [][2]int{{0, s.Total - 1}}
		snap.ReceivedBytes = s.Size
		snap.Frontier = s.Total
		snap.StorageProgress = 100
	case s.win != nil:
		ws := s.win.Snapshot()
		snap.Received = ws.Received
		snap.ReceivedBytes = ws.ReceivedBytes
		snap.Frontier = ws.Frontier
	}
	return snap
}

func (s *Session) Snapshot() SessionSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshotLocked()
}

// InitReq carries everything the handler parsed from the init request.
type InitReq struct {
	User      *model.User
	Path      string // full destination path, already user-joined
	Size      int64
	ChunkSize int64 // final chunk size in bytes, already clamped by the handler
	Mimetype  string
	Modified  time.Time
	Hashes    map[*utils.HashType]string
	Overwrite bool
}

// Manager owns all live sessions. Sessions are in-memory only (aligned with
// upload tasks not being persisted); a restart drops them and the ring files
// are swept on the next start.
type Manager struct {
	mu     sync.Mutex
	byID   map[string]*Session
	byKey  map[string]string
	gcOnce sync.Once
	ttl    time.Duration // 0 means defaultSessionTTL; tests shrink it per instance
}

func (m *Manager) sessionTTL() time.Duration {
	if m.ttl > 0 {
		return m.ttl
	}
	return defaultSessionTTL
}

var DefaultManager = &Manager{
	byID:  make(map[string]*Session),
	byKey: make(map[string]string),
}

func (m *Manager) dir() string {
	return filepath.Join(conf.Conf.TempDir, "multipart")
}

func sessionKey(userID uint, path string, size int64) string {
	return fmt.Sprintf("%d|%s|%d", userID, path, size)
}

// hashesQualifyResume reports whether two hash sets prove the client is
// re-uploading the same file: they must share at least one hash type and
// agree on every shared one. Path+size alone is NOT enough to resume into a
// receiving session — buffered chunks of a different same-sized file would be
// silently mixed into the result.
func hashesQualifyResume(old, new map[*utils.HashType]string) bool {
	shared := false
	for t, ov := range old {
		if nv, ok := new[t]; ok {
			if ov != nv {
				return false
			}
			shared = true
		}
	}
	return shared
}

// StartGC sweeps ring files orphaned by a previous run and starts the expiry
// loop. It is called at server startup so orphans are reclaimed even if no
// multipart upload ever happens again; Init also calls it, so embedders that
// skip the server wiring still get GC lazily.
func (m *Manager) StartGC() {
	m.ensureGC()
}

func (m *Manager) ensureGC() {
	m.gcOnce.Do(func() {
		// sweep ring files orphaned by a previous run; bootstrap's CleanTempDir
		// only runs when no transfer tasks are pending, so do not rely on it
		_ = os.RemoveAll(m.dir())
		go func() {
			ticker := time.NewTicker(gcInterval)
			for range ticker.C {
				m.gc()
			}
		}()
	})
}

func (m *Manager) gc() {
	deadline := time.Now().Add(-m.sessionTTL()).UnixNano()
	m.mu.Lock()
	var expired []*Session
	for _, s := range m.byID {
		if s.lastActive.Load() < deadline {
			expired = append(expired, s)
		}
	}
	m.mu.Unlock()
	for _, s := range expired {
		m.terminate(s, errors.New("multipart upload session expired"))
	}
}

// terminate aborts a session (if still receiving) and drops it from the maps.
func (m *Manager) terminate(s *Session, cause error) {
	s.mu.Lock()
	if s.state == StateReceiving {
		s.state = StateAborted
		s.err = cause
	}
	s.killAttemptLocked()
	s.mu.Unlock()
	m.remove(s)
}

// killAttemptLocked stops the running pipeline attempt: the context cancel
// interrupts drivers blocked on network I/O, and closing the window wakes a
// driver blocked in Read (context cancellation cannot interrupt cond.Wait).
// The caller must hold s.mu and must have set the final state first.
func (s *Session) killAttemptLocked() {
	if s.cancel != nil {
		s.cancel()
	}
	if s.win != nil {
		_ = s.win.CloseWithError(errAborted)
	}
}

func (m *Manager) remove(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.byID, s.ID)
	key := sessionKey(s.Creator.ID, s.Path, s.Size)
	if m.byKey[key] == s.ID {
		delete(m.byKey, key)
	}
}

func (m *Manager) get(user *model.User, id string) (*Session, error) {
	m.mu.Lock()
	s, ok := m.byID[id]
	m.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	if s.Creator.ID != user.ID {
		return nil, ErrNotOwner
	}
	return s, nil
}

// Init creates a session and starts its pipeline, or returns the live session
// for the same (user, path, size) so an interrupted client resumes implicitly.
func (m *Manager) Init(req InitReq) (SessionSnapshot, bool, error) {
	m.ensureGC()
	if req.Size <= 0 {
		return SessionSnapshot{}, false, fmt.Errorf("multipart upload requires a positive X-File-Size, got %d", req.Size)
	}
	if req.ChunkSize <= 0 {
		return SessionSnapshot{}, false, fmt.Errorf("invalid chunk size %d", req.ChunkSize)
	}
	key := sessionKey(req.User.ID, req.Path, req.Size)

	m.mu.Lock()
	if id, ok := m.byKey[key]; ok {
		if s, ok := m.byID[id]; ok {
			s.mu.Lock()
			st := s.state
			s.mu.Unlock()
			// failed_retriable resumes unconditionally: nothing of the old
			// attempt survives except CRCs, and the re-fill CRC check catches
			// a changed file. A receiving session still holds data, so it only
			// resumes when hashes prove it is the same file.
			if st == StateFailedRetriable ||
				(st == StateReceiving && hashesQualifyResume(s.Hashes, req.Hashes)) {
				m.mu.Unlock()
				s.touch()
				return s.Snapshot(), true, nil
			}
			// finished session, or same path+size without proof of identity:
			// drop the old session and start fresh
			m.mu.Unlock()
			m.terminate(s, errors.New("superseded by a new upload of the same path and size"))
			m.mu.Lock()
		}
	}
	m.mu.Unlock()

	dstDir, name := stdpath.Split(req.Path)
	s := &Session{
		ID:        uuid.NewString(),
		Path:      req.Path,
		DstDir:    dstDir,
		Name:      name,
		Size:      req.Size,
		ChunkSize: req.ChunkSize,
		Mimetype:  req.Mimetype,
		Modified:  req.Modified,
		Hashes:    req.Hashes,
		Overwrite: req.Overwrite,
		Creator:   req.User,
		state:     StateReceiving,
	}
	s.touch()

	s.mu.Lock()
	if err := m.startAttemptLocked(s); err != nil {
		s.mu.Unlock()
		return SessionSnapshot{}, false, err
	}
	s.Total = s.win.TotalChunks()
	snap := s.snapshotLocked()
	s.mu.Unlock()

	m.mu.Lock()
	m.byID[s.ID] = s
	m.byKey[key] = s.ID
	m.mu.Unlock()
	return snap, false, nil
}

// startAttemptLocked builds a fresh window and spawns the pipeline goroutine.
// The caller must hold s.mu.
func (m *Manager) startAttemptLocked(s *Session) error {
	win, err := NewWindow(m.dir(), fmt.Sprintf("%s.%d", s.ID, s.attempt), s.ChunkSize, s.Size, WindowSlots)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), conf.UserKey, s.Creator))
	done := make(chan struct{})
	s.win = win
	s.cancel = cancel
	s.done = done
	s.state = StateReceiving
	s.err = nil
	s.setStoragePct(0)

	fileStream := &stream.FileStream{
		Obj: &model.Object{
			Name:     s.Name,
			Size:     s.Size,
			Modified: s.Modified,
			HashInfo: utils.NewHashInfoByMap(s.Hashes),
		},
		Reader:   win,
		Mimetype: s.Mimetype,
	}
	fileStream.Add(win)

	dstDir := s.DstDir
	put := putFile // capture: the seam must not be read after spawn
	go func() {
		err := put(ctx, dstDir, fileStream, s.setStoragePct)
		s.finishAttempt(win, err)
		close(done)
	}()
	return nil
}

// finishAttempt records the pipeline outcome and harvests the CRC table for
// re-fill verification. The window's data is gone at this point (op.Put closed
// it); only metadata survives.
func (s *Session) finishAttempt(win *Window, err error) {
	crcs, set := win.CRCs()
	_ = win.Close() // op.Put already closed it; make sure the ring file is gone anyway

	s.mu.Lock()
	defer s.mu.Unlock()
	s.touch()
	if s.win == win {
		s.win = nil
	}
	s.prevCRCs, s.prevSet = crcs, set
	switch {
	case err == nil:
		s.state = StateCompleted
		s.err = nil
		s.setStoragePct(100)
	case s.state != StateReceiving:
		// Abort/expiry already labeled this attempt; keep that state.
		if s.err == nil {
			s.err = err
		}
	case isPermanentPutError(err):
		s.state = StateFailedPermanent
		s.err = err
	default:
		s.state = StateFailedRetriable
		s.err = err
		s.attempt++
	}
}

func isPermanentPutError(err error) bool {
	if errors.Is(err, context.Canceled) {
		return false
	}
	for _, target := range []error{
		errs.UploadNotSupported,
		errs.PermissionDenied,
		errs.StorageNotFound,
		errs.ObjectAlreadyExists,
		errs.RelativePath,
		errs.IgnoredSystemFile,
	} {
		if errors.Is(err, target) {
			return true
		}
	}
	return false
}

// Chunk feeds one chunk into the session. Re-sending chunk 0 to a
// failed_retriable session re-fills it: a fresh window and pipeline attempt.
func (m *Manager) Chunk(user *model.User, id string, idx int, body io.Reader) (SessionSnapshot, error) {
	s, err := m.get(user, id)
	if err != nil {
		return SessionSnapshot{}, err
	}
	s.touch()

	s.mu.Lock()
	switch s.state {
	case StateReceiving:
	case StateFailedRetriable:
		if idx != 0 {
			snap := s.snapshotLocked()
			s.mu.Unlock()
			return snap, fmt.Errorf("upload attempt failed, resend from chunk 0 to retry: %w", s.err)
		}
		if err := m.startAttemptLocked(s); err != nil {
			snap := s.snapshotLocked()
			s.mu.Unlock()
			return snap, err
		}
	case StateCompleted:
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, nil // idempotent: stragglers after rapid-upload/finish succeed
	default:
		snap := s.snapshotLocked()
		s.mu.Unlock()
		return snap, fmt.Errorf("session is %s: %w", s.state, s.err)
	}
	win := s.win
	var prevCRC uint32
	hasPrev := false
	if idx < len(s.prevSet) && s.prevSet[idx] {
		prevCRC, hasPrev = s.prevCRCs[idx], true
	}
	s.mu.Unlock()

	crc, err := win.WriteChunk(idx, body)
	if err != nil {
		// A closed window means the pipeline ended while this chunk was in
		// flight — rapid upload makes this the NORMAL case: the driver
		// succeeds off the hash alone with chunks still arriving. The window
		// closes (op.Put's defer) moments before the verdict is recorded, so
		// wait for the verdict instead of racing it, then absorb the chunk
		// idempotently if the upload in fact succeeded.
		if errors.Is(err, ErrClosed) || errors.Is(err, context.Canceled) {
			s.mu.Lock()
			done := s.done
			s.mu.Unlock()
			select {
			case <-done:
			case <-time.After(10 * time.Second): // pipeline teardown is µs-scale; never expected
			}
		}
		s.mu.Lock()
		completed := s.state == StateCompleted
		snap := s.snapshotLocked()
		s.mu.Unlock()
		if completed {
			return snap, nil
		}
		return snap, err
	}
	if hasPrev && crc != prevCRC {
		err := fmt.Errorf("chunk %d content changed between attempts, aborting", idx)
		s.mu.Lock()
		s.state = StateFailedPermanent
		s.err = err
		s.killAttemptLocked()
		s.mu.Unlock()
		return s.Snapshot(), err
	}
	return s.Snapshot(), nil
}

// Complete waits for the pipeline outcome. It refuses to block while chunks
// are still missing, so a buggy client cannot park a connection for the TTL.
func (m *Manager) Complete(ctx context.Context, user *model.User, id string) (SessionSnapshot, error) {
	s, err := m.get(user, id)
	if err != nil {
		return SessionSnapshot{}, err
	}
	s.touch()
	for {
		s.mu.Lock()
		st := s.state
		done := s.done
		if st == StateReceiving && s.win != nil {
			if ws := s.win.Snapshot(); ws.ReceivedBytes < s.Size {
				snap := s.snapshotLocked()
				s.mu.Unlock()
				return snap, fmt.Errorf("cannot complete: %d of %d bytes received", ws.ReceivedBytes, s.Size)
			}
		}
		s.mu.Unlock()
		if st != StateReceiving {
			break
		}
		select {
		case <-done:
		case <-ctx.Done():
			return s.Snapshot(), ctx.Err()
		}
	}
	s.touch()
	s.mu.Lock()
	st, serr := s.state, s.err
	snap := s.snapshotLocked()
	s.mu.Unlock()
	if st == StateCompleted {
		m.remove(s) // served its purpose; frees the key for future uploads
		return snap, nil
	}
	return snap, fmt.Errorf("upload failed (%s): %w", st, serr)
}

// Status looks a session up by id.
func (m *Manager) Status(user *model.User, id string) (SessionSnapshot, error) {
	s, err := m.get(user, id)
	if err != nil {
		return SessionSnapshot{}, err
	}
	s.touch()
	return s.Snapshot(), nil
}

// Find looks a live session up by destination path and size, for resume discovery.
func (m *Manager) Find(user *model.User, path string, size int64) (SessionSnapshot, error) {
	m.mu.Lock()
	id, ok := m.byKey[sessionKey(user.ID, path, size)]
	m.mu.Unlock()
	if !ok {
		return SessionSnapshot{}, ErrSessionNotFound
	}
	return m.Status(user, id)
}

// Abort cancels the pipeline and forgets the session immediately.
func (m *Manager) Abort(user *model.User, id string) error {
	s, err := m.get(user, id)
	if err != nil {
		return err
	}
	m.terminate(s, errors.New("multipart upload aborted by client"))
	return nil
}
