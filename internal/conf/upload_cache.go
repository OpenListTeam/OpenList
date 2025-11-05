package conf

import (
	"context"
	"path/filepath"
	"sync"
)

// UploadCache carries temp file information between task retries and drivers.
type UploadCache struct {
	cachedPath string
	tempFile   string
	keep       map[string]struct{}
	mu         sync.RWMutex
}

// NewUploadCache creates a cache holder with an optional existing cached file path.
func NewUploadCache(path string) *UploadCache {
	uc := &UploadCache{}
	if path != "" {
		uc.cachedPath = normalizePath(path)
	}
	return uc
}

// CachedPath returns the reusable cached file path if present.
func (u *UploadCache) CachedPath() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.cachedPath
}

// SetCachedPath updates the cached file path (used when a new cache is established).
func (u *UploadCache) SetCachedPath(path string) {
	if path == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cachedPath = normalizePath(path)
}

// RegisterTemp records a temporary file generated during upload.
func (u *UploadCache) RegisterTemp(path string) {
	if path == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	u.tempFile = normalizePath(path)
}

// TempFile returns the last registered temporary file path.
func (u *UploadCache) TempFile() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.tempFile
}

// MarkKeep marks the given path to be preserved after the current attempt.
func (u *UploadCache) MarkKeep(path string) {
	if path == "" {
		return
	}
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.keep == nil {
		u.keep = make(map[string]struct{})
	}
	u.keep[normalizePath(path)] = struct{}{}
}

// ShouldKeep reports whether the specified path should be preserved.
func (u *UploadCache) ShouldKeep(path string) bool {
	if path == "" {
		return false
	}
	nPath := normalizePath(path)
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.cachedPath != "" && u.cachedPath == nPath {
		return true
	}
	if _, ok := u.keep[nPath]; ok {
		return true
	}
	return false
}

func normalizePath(path string) string {
	if abs, err := filepath.Abs(path); err == nil {
		return abs
	}
	return path
}

// UploadCacheFromContext extracts the UploadCache pointer from the provided context, if any.
func UploadCacheFromContext(ctx context.Context) *UploadCache {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(UploadCacheKey).(*UploadCache); ok {
		return v
	}
	return nil
}



