package cache

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
)

// UploadMetadata keeps hash info for a cached temp file.
type UploadMetadata struct {
	Size       int64    `json:"size"`
	SliceSize  int64    `json:"slice_size"`
	ContentMD5 string   `json:"content_md5"`
	SliceMD5   string   `json:"slice_md5"`
	BlockList  []string `json:"block_list"`
}

// UploadCache carries temp file information between task retries and drivers.
type UploadCache struct {
	cachedPath string
	tempFile   string
	keep       map[string]struct{}
	metadata   *UploadMetadata
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
	u.cachedPath = normalizePath(path)
	u.metadata = nil
	u.mu.Unlock()
}

// RegisterTemp records a temporary file generated during upload.
func (u *UploadCache) RegisterTemp(path string) {
	if path == "" {
		return
	}
	u.mu.Lock()
	u.tempFile = normalizePath(path)
	u.metadata = nil
	u.mu.Unlock()
}

// TempFile returns the last registered temporary file path.
func (u *UploadCache) TempFile() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.tempFile
}

// CurrentPath returns the path associated with this cache (temp if present, otherwise cached).
func (u *UploadCache) CurrentPath() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.currentPathLocked()
}

func (u *UploadCache) currentPathLocked() string {
	if u.tempFile != "" {
		return u.tempFile
	}
	return u.cachedPath
}

// MarkKeep marks the given path to be preserved after the current attempt.
func (u *UploadCache) MarkKeep(path string) {
	if path == "" {
		return
	}
	u.mu.Lock()
	if u.keep == nil {
		u.keep = make(map[string]struct{})
	}
	u.keep[normalizePath(path)] = struct{}{}
	u.mu.Unlock()
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

func (u *UploadCache) metadataPathLocked() string {
	path := u.currentPathLocked()
	if path == "" {
		return ""
	}
	return path + ".meta"
}

// MetadataPathFor returns the metadata sidecar path for the provided temp file path.
func MetadataPathFor(path string) string {
	if path == "" {
		return ""
	}
	return normalizePath(path) + ".meta"
}

// MetadataPath returns the sidecar metadata path for the cached file, if known.
func (u *UploadCache) MetadataPath() string {
	u.mu.RLock()
	defer u.mu.RUnlock()
	return u.metadataPathLocked()
}

// Metadata returns a copy of the cached metadata in memory, if any.
func (u *UploadCache) Metadata() *UploadMetadata {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.metadata == nil {
		return nil
	}
	return cloneMetadata(u.metadata)
}

func cloneMetadata(meta *UploadMetadata) *UploadMetadata {
	if meta == nil {
		return nil
	}
	bl := make([]string, len(meta.BlockList))
	copy(bl, meta.BlockList)
	return &UploadMetadata{
		Size:       meta.Size,
		SliceSize:  meta.SliceSize,
		ContentMD5: meta.ContentMD5,
		SliceMD5:   meta.SliceMD5,
		BlockList:  bl,
	}
}

// LoadMetadata loads metadata from disk into memory if not yet present.
func (u *UploadCache) LoadMetadata() (*UploadMetadata, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.metadata != nil {
		return cloneMetadata(u.metadata), nil
	}
	path := u.metadataPathLocked()
	if path == "" {
		return nil, os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var meta UploadMetadata
	if err := json.Unmarshal(data, &meta); err != nil {
		return nil, err
	}
	u.metadata = &meta
	return cloneMetadata(u.metadata), nil
}

// SaveMetadata stores metadata in memory and persists it alongside the temp file.
func (u *UploadCache) SaveMetadata(meta *UploadMetadata) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if meta == nil {
		u.metadata = nil
		path := u.metadataPathLocked()
		if path != "" {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	}
	u.metadata = cloneMetadata(meta)
	path := u.metadataPathLocked()
	if path == "" {
		return nil
	}
	data, err := json.Marshal(u.metadata)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// RemoveMetadataFile deletes the persisted metadata sidecar file if present.
func (u *UploadCache) RemoveMetadataFile() error {
	path := u.MetadataPath()
	if path == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// RemoveMetadataByPath deletes the metadata sidecar associated with the given temp file path.
func RemoveMetadataByPath(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(MetadataPathFor(path))
}

// UploadCacheFromContext extracts the UploadCache pointer from the provided context, if any.
func UploadCacheFromContext(ctx context.Context) *UploadCache {
	if ctx == nil {
		return nil
	}
	if v, ok := ctx.Value(conf.UploadCacheKey).(*UploadCache); ok {
		return v
	}
	return nil
}


