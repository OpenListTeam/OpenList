package fs

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/google/uuid"
	pkgerrors "github.com/pkg/errors"
)

const (
	// DefaultChunkSize is the default chunk size (5MB)
	DefaultChunkSize = 5 * 1024 * 1024
	// SessionMaxLifetime is the maximum lifetime of a multipart upload session
	SessionMaxLifetime = 2 * time.Hour
)

// multipartSessionManager manages multipart upload sessions
type multipartSessionManager struct {
	sessions map[string]*model.MultipartUploadSession
	mu       sync.RWMutex
}

// Global multipart session manager
var MultipartSessionManager = NewMultipartSessionManager()

// NewMultipartSessionManager creates a new session manager
func NewMultipartSessionManager() *multipartSessionManager {
	return &multipartSessionManager{
		sessions: make(map[string]*model.MultipartUploadSession),
	}
}

// InitMultipartUpload initializes a new multipart upload session
func (m *multipartSessionManager) InitMultipartUpload(
	ctx context.Context,
	storageID uint,
	dstDirPath string,
	fileName string,
	fileSize int64,
	chunkSize int64,
	contentType string,
) (*model.MultipartUploadSession, error) {
	if chunkSize <= 0 {
		chunkSize = DefaultChunkSize
	}
	if chunkSize < 1024 {
		chunkSize = 1024
	}

	totalChunks := int((fileSize + chunkSize - 1) / chunkSize)
	if totalChunks == 0 {
		totalChunks = 1
	}

	uploadID := uuid.New().String()

	chunkDir := filepath.Join(conf.Conf.TempDir, "uploads", uploadID)
	if err := utils.CreateNestedDirectory(chunkDir); err != nil {
		return nil, pkgerrors.Wrap(err, "failed to create chunk directory")
	}

	now := time.Now()
	session := &model.MultipartUploadSession{
		UploadID:       uploadID,
		StorageID:      storageID,
		DstDirPath:     dstDirPath,
		FileName:       fileName,
		FileSize:       fileSize,
		ChunkSize:      chunkSize,
		TotalChunks:    totalChunks,
		ContentType:    contentType,
		ChunkDir:       chunkDir,
		UploadedChunks: make(map[int]model.ChunkInfo),
		CreatedAt:      now,
		ExpiresAt:      now.Add(SessionMaxLifetime),
	}

	m.mu.Lock()
	m.sessions[uploadID] = session
	m.mu.Unlock()

	go m.cleanupSession(uploadID, session.ExpiresAt)

	return session, nil
}

// UploadChunk uploads a single chunk
func (m *multipartSessionManager) UploadChunk(
	ctx context.Context,
	uploadID string,
	chunkIndex int,
	chunkSize int64,
	reader io.Reader,
	md5 string,
) (*model.ChunkUploadResp, error) {
	session, err := m.GetSession(uploadID)
	if err != nil {
		return nil, err
	}

	if chunkIndex < 0 || chunkIndex >= session.TotalChunks {
		return nil, pkgerrors.New("chunk index out of range")
	}

	// Idempotent: if chunk already uploaded, return success
	if _, exists := session.UploadedChunks[chunkIndex]; exists {
		return m.getUploadResponse(session), nil
	}

	chunkFileName := fmt.Sprintf("%d.chunk", chunkIndex)
	chunkFilePath := filepath.Join(session.ChunkDir, chunkFileName)
	chunkFile, err := utils.CreateNestedFile(chunkFilePath)
	if err != nil {
		return nil, pkgerrors.Wrap(err, "failed to create chunk file")
	}
	defer chunkFile.Close()

	written, err := utils.CopyWithBuffer(chunkFile, reader)
	if err != nil {
		os.Remove(chunkFilePath)
		return nil, pkgerrors.Wrap(err, "failed to write chunk")
	}

	if written != chunkSize {
		os.Remove(chunkFilePath)
		return nil, pkgerrors.New("chunk size mismatch")
	}

	m.mu.Lock()
	session.UploadedChunks[chunkIndex] = model.ChunkInfo{
		Index:      chunkIndex,
		Size:       chunkSize,
		MD5:        md5,
		UploadedAt: time.Now(),
	}
	m.mu.Unlock()

	return m.getUploadResponse(session), nil
}

// CompleteMultipartUpload merges all chunks and uploads to storage
func (m *multipartSessionManager) CompleteMultipartUpload(
	ctx context.Context,
	uploadID string,
) error {
	session, err := m.GetSession(uploadID)
	if err != nil {
		return err
	}

	if len(session.UploadedChunks) != session.TotalChunks {
		return pkgerrors.New(fmt.Sprintf("incomplete upload: %d/%d chunks uploaded",
			len(session.UploadedChunks), session.TotalChunks))
	}

	mergedReader, err := NewChunkMergedReader(session)
	if err != nil {
		return err
	}
	defer mergedReader.Close()

	fileStream := &stream.FileStream{
		Obj: &model.Object{
			Name:     session.FileName,
			Size:     session.FileSize,
			Modified: time.Now(),
		},
		Reader:   mergedReader,
		Mimetype: session.ContentType,
	}

	err = putDirectly(ctx, session.DstDirPath, fileStream)
	if err != nil {
		return err
	}

	m.mu.Lock()
	delete(m.sessions, uploadID)
	m.mu.Unlock()

	m.cleanupSessionFiles(session)
	return nil
}

// GetSession retrieves a session by upload ID
func (m *multipartSessionManager) GetSession(uploadID string) (*model.MultipartUploadSession, error) {
	m.mu.RLock()
	session, exists := m.sessions[uploadID]
	m.mu.RUnlock()

	if !exists {
		return nil, pkgerrors.New("multipart upload session not found")
	}

	if time.Now().After(session.ExpiresAt) {
		m.cleanupSessionFiles(session)
		m.mu.Lock()
		delete(m.sessions, uploadID)
		m.mu.Unlock()
		return nil, pkgerrors.New("multipart upload session expired")
	}

	return session, nil
}

func (m *multipartSessionManager) getUploadResponse(session *model.MultipartUploadSession) *model.ChunkUploadResp {
	indices := make([]int, 0, len(session.UploadedChunks))
	for i := range session.UploadedChunks {
		indices = append(indices, i)
	}
	sort.Ints(indices)

	var totalBytes int64
	for _, info := range session.UploadedChunks {
		totalBytes += info.Size
	}

	return &model.ChunkUploadResp{
		ChunkIndex:     indices[len(indices)-1],
		UploadedChunks: indices,
		UploadedBytes:  totalBytes,
	}
}

func (m *multipartSessionManager) cleanupSessionFiles(session *model.MultipartUploadSession) {
	if session.ChunkDir != "" {
		os.RemoveAll(session.ChunkDir)
	}
}

func (m *multipartSessionManager) cleanupSession(uploadID string, expiresAt time.Time) {
	time.Sleep(time.Until(expiresAt))

	m.mu.Lock()
	session, exists := m.sessions[uploadID]
	if exists {
		delete(m.sessions, uploadID)
		m.mu.Unlock()
		m.cleanupSessionFiles(session)
	} else {
		m.mu.Unlock()
	}
}

// ChunkMergedReader reads chunks in order and merges them
type ChunkMergedReader struct {
	session      *model.MultipartUploadSession
	readers      []io.ReadCloser
	current      int
	currentReader io.Reader
}

func NewChunkMergedReader(session *model.MultipartUploadSession) (*ChunkMergedReader, error) {
	readers := make([]io.ReadCloser, session.TotalChunks)

	for i := 0; i < session.TotalChunks; i++ {
		chunkFileName := fmt.Sprintf("%d.chunk", i)
		chunkFilePath := filepath.Join(session.ChunkDir, chunkFileName)

		f, err := os.Open(chunkFilePath)
		if err != nil {
			for j := 0; j < i; j++ {
				readers[j].Close()
			}
			return nil, pkgerrors.Wrap(err, "failed to open chunk file")
		}
		readers[i] = f
	}

	return &ChunkMergedReader{
		session:      session,
		readers:      readers,
		current:      0,
		currentReader: nil,
	}, nil
}

func (r *ChunkMergedReader) Read(p []byte) (n int, err error) {
	for r.current < len(r.readers) {
		if r.currentReader == nil {
			r.currentReader = r.readers[r.current]
		}

		n, err = r.currentReader.Read(p)
		if n > 0 {
			return n, err
		}

		if err == io.EOF {
			r.currentReader = nil
			r.current++
			continue
		}

		return n, err
	}

	return 0, io.EOF
}

func (r *ChunkMergedReader) Close() error {
	for _, reader := range r.readers {
		if reader != nil {
			reader.Close()
		}
	}
	return nil
}
