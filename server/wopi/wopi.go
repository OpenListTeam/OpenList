package wopi

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
	"sync"
	"time"
)

const (
	WopiHeaderPrefix      = "X-WOPI-"
	OverwriteHeader       = WopiHeaderPrefix + "Override"
	ServerErrorHeader     = WopiHeaderPrefix + "ServerError"
	RenameRequestHeader   = WopiHeaderPrefix + "RequestedName"
	LockTokenHeader       = WopiHeaderPrefix + "Lock"
	ItemVersionHeader     = WopiHeaderPrefix + "ItemVersion"
	SuggestedTargetHeader = WopiHeaderPrefix + "SuggestedTarget"
	InvalidFileNameHeader = WopiHeaderPrefix + "InvalidFileNameError"
	AccessTokenQuery      = "access_token"
	LockDuration          = 30 * time.Minute
	SessionTTL            = 30 * time.Minute
)

const (
	MethodLock        = "LOCK"
	MethodUnlock      = "UNLOCK"
	MethodRefreshLock = "REFRESH_LOCK"
	MethodPutRelative = "PUT_RELATIVE"
)

type WopiSession struct {
	ID          string `json:"id"`
	AccessToken string `json:"access_token"`
	Expires     int64  `json:"expires"`
	Path        string `json:"path"`
	UserID      uint   `json:"user_id"`
	CanEdit     bool   `json:"can_edit"`
}

type SessionCache struct {
	AccessToken string
	Path        string
	UserID      uint
	CanEdit     bool
	ExpiresAt   time.Time
}

type WopiFileInfo struct {
	// Required
	BaseFileName string `json:"BaseFileName"`
	Size         int64  `json:"Size"`
	Version      string `json:"Version"`
	// User metadata
	OwnerId          string `json:"OwnerId"`
	UserId           string `json:"UserId"`
	UserFriendlyName string `json:"UserFriendlyName"`
	IsAnonymousUser  bool   `json:"IsAnonymousUser"`
	// User permissions
	ReadOnly            bool `json:"ReadOnly"`
	UserCanWrite        bool `json:"UserCanWrite"`
	UserCanRename       bool `json:"UserCanRename"`
	UserCanReview       bool `json:"UserCanReview"`
	UserCanNotWriteRelative bool `json:"UserCanNotWriteRelative"`
	// Host capabilities
	SupportsLocks       bool `json:"SupportsLocks"`
	SupportsGetLock     bool `json:"SupportsGetLock"`
	SupportsUpdate      bool `json:"SupportsUpdate"`
	SupportsRename      bool `json:"SupportsRename"`
	SupportsReviewing   bool `json:"SupportsReviewing"`
	// PostMessage
	PostMessageOrigin      string `json:"PostMessageOrigin"`
	ClosePostMessage       bool   `json:"ClosePostMessage"`
	EditModePostMessage    bool   `json:"EditModePostMessage"`
	FileSharingPostMessage bool   `json:"FileSharingPostMessage"`
	FileVersionPostMessage bool   `json:"FileVersionPostMessage"`
	// Breadcrumb
	BreadcrumbBrandName  string `json:"BreadcrumbBrandName"`
	BreadcrumbBrandUrl   string `json:"BreadcrumbBrandUrl"`
	BreadcrumbFolderName string `json:"BreadcrumbFolderName"`
	BreadcrumbFolderUrl  string `json:"BreadcrumbFolderUrl"`
	// Other
	FileNameMaxLength int    `json:"FileNameMaxLength"`
	FileExtension     string `json:"FileExtension"`
	LastModifiedTime  string `json:"LastModifiedTime"`
	DisablePrint      bool   `json:"DisablePrint"`
}

type PutRelativeResponse struct {
	Name string `json:"Name"`
	Url  string `json:"Url"`
}

// PathLockStore stores file locks keyed by normalized path
type PathLockStore struct {
	mu    sync.RWMutex
	locks map[string]pathLock
}

type pathLock struct {
	Token    string
	ExpireAt time.Time
}

var GlobalLockStore = &PathLockStore{
	locks: make(map[string]pathLock),
}

func (s *PathLockStore) LockPath(filePath, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.locks[filePath] = pathLock{Token: token, ExpireAt: time.Now().Add(LockDuration)}
}

func (s *PathLockStore) UnlockPath(filePath, token string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.locks[filePath]; ok && existing.Token == token {
		delete(s.locks, filePath)
	}
}

func (s *PathLockStore) RefreshPath(filePath, token string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.locks[filePath]; ok && existing.Token == token {
		if time.Now().Before(existing.ExpireAt) {
			s.locks[filePath] = pathLock{Token: token, ExpireAt: time.Now().Add(LockDuration)}
			return true
		}
		delete(s.locks, filePath)
	}
	return false
}

func (s *PathLockStore) GetLockForPath(filePath string) string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if lock, ok := s.locks[filePath]; ok {
		if time.Now().Before(lock.ExpireAt) {
			return lock.Token
		}
		delete(s.locks, filePath)
	}
	return ""
}

// Session store
type WopiSessionStore struct {
	mu       sync.RWMutex
	sessions map[string]*SessionCache
}

var GlobalSessionStore = &WopiSessionStore{
	sessions: make(map[string]*SessionCache),
}

func (s *WopiSessionStore) CreateSession(path string, userID uint, canEdit bool) *WopiSession {
	sessionID := generateRandomHex(16)
	token := generateRandomHex(64)
	accessToken := sessionID + "." + token
	expiresAt := time.Now().Add(SessionTTL)

	session := &WopiSession{
		ID:          sessionID,
		AccessToken: accessToken,
		Expires:     expiresAt.UnixMilli(),
		Path:        path,
		UserID:      userID,
		CanEdit:     canEdit,
	}

	s.mu.Lock()
	s.sessions[sessionID] = &SessionCache{
		AccessToken: accessToken,
		Path:        path,
		UserID:      userID,
		CanEdit:     canEdit,
		ExpiresAt:   expiresAt,
	}
	s.mu.Unlock()

	go s.cleanup()
	return session
}

func (s *WopiSessionStore) GetSession(accessToken string) (*SessionCache, bool) {
	sessionID, _, ok := strings.Cut(accessToken, ".")
	if !ok || sessionID == "" {
		return nil, false
	}

	s.mu.RLock()
	session, exists := s.sessions[sessionID]
	s.mu.RUnlock()

	if !exists {
		return nil, false
	}

	if time.Now().After(session.ExpiresAt) {
		s.DeleteSession(sessionID)
		return nil, false
	}

	if session.AccessToken != accessToken {
		return nil, false
	}

	return session, true
}

func (s *WopiSessionStore) DeleteSession(sessionID string) {
	s.mu.Lock()
	delete(s.sessions, sessionID)
	s.mu.Unlock()
}

func (s *WopiSessionStore) cleanup() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for id, session := range s.sessions {
		if now.After(session.ExpiresAt) {
			delete(s.sessions, id)
		}
	}
}

func generateRandomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
