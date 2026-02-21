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
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/google/uuid"
	pkgerrors "github.com/pkg/errors"
)

const (
	DefaultChunkSize     = 5 * 1024 * 1024
	SessionMaxLifetime   = 2 * time.Hour
)

type multipartSessionManager struct {
	sessions map[string]*model.MultipartUploadSession
	mu       sync.RWMutex
}

var MultipartSessionManager = &multipartSessionManager{
	sessions: make(map[string]*model.MultipartUploadSession),
}

// InitOrGetSession initializes a new session or returns existing one
func (m *multipartSessionManager) InitOrGetSession(
	uploadID string,
	dstDirPath string,
	fileName string,
	fileSize int64,
	chunkSize int64,
	contentType string,
	overwrite bool,
) (*model.MultipartUploadSession, error) {
	// If uploadID provided, try to get existing session
	if uploadID != "" {
		session, err := m.GetSession(uploadID)
		if err != nil {
			return nil, err
		}
		return session, nil
	}

	// Create new session
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

	newUploadID := uuid.New().String()
	chunkDir := filepath.Join(conf.Conf.TempDir, "multipart", newUploadID)
	if err := utils.CreateNestedDirectory(chunkDir); err != nil {
		return nil, pkgerrors.Wrap(err, "failed to create chunk directory")
	}

	now := time.Now()
	session := &model.MultipartUploadSession{
		UploadID:       newUploadID,
		DstDirPath:     dstDirPath,
		FileName:       fileName,
		FileSize:       fileSize,
		ChunkSize:      chunkSize,
		TotalChunks:    totalChunks,
		ContentType:    contentType,
		ChunkDir:       chunkDir,
		UploadedChunks: make(map[int]model.ChunkInfo),
		Overwrite:      overwrite,
		CreatedAt:      now,
		ExpiresAt:      now.Add(SessionMaxLifetime),
	}

	m.mu.Lock()
	m.sessions[newUploadID] = session
	m.mu.Unlock()

	go m.cleanupAfterExpiry(newUploadID, session.ExpiresAt)

	return session, nil
}

// UploadChunk uploads a single chunk (idempotent)
func (m *multipartSessionManager) UploadChunk(
	uploadID string,
	chunkIndex int,
	chunkSize int64,
	reader io.Reader,
) (*model.ChunkUploadResp, error) {
	session, err := m.GetSession(uploadID)
	if err != nil {
		return nil, err
	}

	if chunkIndex < 0 || chunkIndex >= session.TotalChunks {
		return nil, pkgerrors.New("chunk index out of range")
	}

	m.mu.RLock()
	_, exists := session.UploadedChunks[chunkIndex]
	m.mu.RUnlock()

	// Idempotent: if chunk already uploaded, return success
	if exists {
		return m.buildResponse(session), nil
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

	isLastChunk := chunkIndex == session.TotalChunks-1
	if isLastChunk {
		// For the last chunk, allow a smaller size (the file may not divide evenly by the chunk size),
		// but still reject chunks that exceed the expected size.
		if written > chunkSize {
			os.Remove(chunkFilePath)
			return nil, pkgerrors.Errorf("chunk size mismatch: expected at most %d, got %d", chunkSize, written)
		}
	} else {
		// For non-final chunks, enforce strict equality with the expected chunk size.
		if written != chunkSize {
			os.Remove(chunkFilePath)
			return nil, pkgerrors.Errorf("chunk size mismatch: expected %d, got %d", chunkSize, written)
		}
	}

	m.mu.Lock()
	session.UploadedChunks[chunkIndex] = model.ChunkInfo{
		Index:      chunkIndex,
		Size:       written,
		UploadedAt: time.Now(),
	}
	m.mu.Unlock()

	return m.buildResponse(session), nil
}

// CompleteUpload merges all chunks and uploads to storage
func (m *multipartSessionManager) CompleteUpload(
	ctx context.Context,
	uploadID string,
	asTask bool,
) (task.TaskExtensionInfo, error) {
	session, err := m.GetSession(uploadID)
	if err != nil {
		return nil, err
	}

	// Protect access to session.UploadedChunks to avoid races with concurrent chunk uploads.
	m.mu.RLock()
	if len(session.UploadedChunks) != session.TotalChunks {
		m.mu.RUnlock()
		return nil, pkgerrors.Errorf("incomplete upload: %d/%d chunks uploaded",
			len(session.UploadedChunks), session.TotalChunks)
	}

	mergedReader, err := newChunkMergedReader(session)
	m.mu.RUnlock()
	if err != nil {
		return nil, err
	}

	fileStream := &stream.FileStream{
		Obj: &model.Object{
			Name:     session.FileName,
			Size:     session.FileSize,
			Modified: time.Now(),
		},
		Reader:       mergedReader,
		Mimetype:     session.ContentType,
		WebPutAsTask: asTask,
		Closers:      utils.NewClosers(mergedReader),
	}

	var t task.TaskExtensionInfo
	if asTask {
		t, err = PutAsTask(ctx, session.DstDirPath, fileStream)
	} else {
		err = PutDirectly(ctx, session.DstDirPath, fileStream)
	}

	if err != nil {
		mergedReader.Close()
		return nil, err
	}

	// Cleanup session
	m.mu.Lock()
	delete(m.sessions, uploadID)
	m.mu.Unlock()

	m.cleanupSessionFiles(session)
	return t, nil
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

func (m *multipartSessionManager) buildResponse(session *model.MultipartUploadSession) *model.ChunkUploadResp {
	m.mu.RLock()
	defer m.mu.RUnlock()

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
		UploadID:       session.UploadID,
		UploadedChunks: indices,
		UploadedBytes:  totalBytes,
		TotalChunks:    session.TotalChunks,
	}
}

func (m *multipartSessionManager) cleanupSessionFiles(session *model.MultipartUploadSession) {
	if session.ChunkDir != "" {
		os.RemoveAll(session.ChunkDir)
	}
}

func (m *multipartSessionManager) cleanupAfterExpiry(uploadID string, expiresAt time.Time) {
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
type chunkMergedReader struct {
	session       *model.MultipartUploadSession
	readers       []io.ReadCloser
	current       int
	currentReader io.Reader
}

func newChunkMergedReader(session *model.MultipartUploadSession) (*chunkMergedReader, error) {
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

	return &chunkMergedReader{
		session:       session,
		readers:       readers,
		current:       0,
		currentReader: nil,
	}, nil
}

func (r *chunkMergedReader) Read(p []byte) (n int, err error) {
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

func (r *chunkMergedReader) Close() error {
	for _, reader := range r.readers {
		if reader != nil {
			reader.Close()
		}
	}
	return nil
}
