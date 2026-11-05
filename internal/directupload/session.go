package directupload

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	// defaultTTL bounds how long a half-finished client-side upload may stay
	// unknown to the server. The storage session usually expires earlier.
	defaultTTL = 12 * time.Hour
	gcInterval = 10 * time.Minute
)

var (
	ErrSessionNotFound = errors.New("direct upload session not found")
	ErrSessionExpired  = errors.New("direct upload session expired")
	ErrNotOwner        = errors.New("direct upload session belongs to another user")
)

// Session tracks a client-side (browser) upload whose payload never passes
// through this server. The client uploads the parts straight to the storage
// backend, then reports the collected part ETags so Finish can complete the
// multipart upload and run any storage-specific callback.
type Session struct {
	Token  string
	UserID uint
	// Path is the full virtual destination path (mount path included).
	Path string
	// Expires is the moment after which the session is dropped.
	Expires time.Time
	// Finish finalizes the remote upload with the part ETags collected by the
	// client. It is kept reusable so an attempt that failed before touching the
	// storage can be retried until the session is deleted.
	Finish func(ctx context.Context, etags []string) error
	// Abort releases the remote side of a session the client never finished.
	// It is called once when the session expires and may be nil.
	Abort func(ctx context.Context)
}

type registry struct {
	mu       sync.Mutex
	sessions map[string]*Session
	once     sync.Once
}

var manager = &registry{sessions: make(map[string]*Session)}

// Register stores the session and returns the opaque token the client has to
// present when finishing the upload.
func Register(s *Session) (string, error) {
	if s == nil || s.Finish == nil {
		return "", errors.New("direct upload session requires a finish handler")
	}
	manager.once.Do(manager.startGC)
	token := uuid.NewString()
	s.Token = token
	if s.Expires.IsZero() {
		s.Expires = time.Now().Add(defaultTTL)
	}
	manager.mu.Lock()
	manager.sessions[token] = s
	manager.mu.Unlock()
	return token, nil
}

// Get returns the session bound to token if it is still valid and owned by
// userID.
func Get(token string, userID uint) (*Session, error) {
	manager.mu.Lock()
	s, ok := manager.sessions[token]
	manager.mu.Unlock()
	if !ok {
		return nil, ErrSessionNotFound
	}
	if time.Now().After(s.Expires) {
		Delete(token)
		return nil, ErrSessionExpired
	}
	if s.UserID != userID {
		return nil, ErrNotOwner
	}
	return s, nil
}

// Delete drops a finished or abandoned session.
func Delete(token string) {
	manager.mu.Lock()
	delete(manager.sessions, token)
	manager.mu.Unlock()
}

func (r *registry) startGC() {
	go func() {
		ticker := time.NewTicker(gcInterval)
		defer ticker.Stop()
		for range ticker.C {
			r.expire(time.Now())
		}
	}()
}

// expire drops the sessions that are past their deadline and releases their
// remote side, so an abandoned upload does not keep a multipart upload open on
// the storage backend.
func (r *registry) expire(now time.Time) {
	var expired []*Session
	r.mu.Lock()
	for token, s := range r.sessions {
		if now.After(s.Expires) {
			delete(r.sessions, token)
			expired = append(expired, s)
		}
	}
	r.mu.Unlock()
	for _, s := range expired {
		if s.Abort != nil {
			s.Abort(context.Background())
		}
	}
}
