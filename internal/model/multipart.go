package model

import "time"

// MultipartUploadSession represents a multipart upload session
type MultipartUploadSession struct {
	UploadID      string              `json:"upload_id"`
	UserID        int64               `json:"user_id"`
	StorageID     uint                `json:"storage_id"`
	DstDirPath    string              `json:"dst_dir_path"`
	FileName      string              `json:"file_name"`
	FileSize      int64               `json:"file_size"`
	ChunkSize     int64               `json:"chunk_size"`
	TotalChunks   int                 `json:"total_chunks"`
	ContentType   string              `json:"content_type"`
	ChunkDir      string              `json:"chunk_dir"`
	UploadedChunks map[int]ChunkInfo  `json:"uploaded_chunks"`
	CreatedAt     time.Time           `json:"created_at"`
	ExpiresAt     time.Time           `json:"expires_at"`
}

// ChunkInfo represents information about an uploaded chunk
type ChunkInfo struct {
	Index      int       `json:"index"`
	Size       int64     `json:"size"`
	MD5        string    `json:"md5,omitempty"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// MultipartInitReq is the request body for initializing a multipart upload
type MultipartInitReq struct {
	Path      string `json:"path" form:"path"`
	FileName  string `json:"file_name" form:"file_name"`
	FileSize  int64  `json:"file_size" form:"file_size"`
	ChunkSize int64  `json:"chunk_size" form:"chunk_size"`
}

// MultipartInitResp is the response for initializing a multipart upload
type MultipartInitResp struct {
	UploadID    string `json:"upload_id"`
	ChunkSize   int64  `json:"chunk_size"`
	TotalChunks int    `json:"total_chunks"`
}

// ChunkUploadResp is the response for uploading a chunk
type ChunkUploadResp struct {
	ChunkIndex     int   `json:"chunk_index"`
	UploadedChunks []int `json:"uploaded_chunks"`
	UploadedBytes  int64 `json:"uploaded_bytes"`
}

// MultipartCompleteReq is the request body for completing a multipart upload
type MultipartCompleteReq struct {
	UploadID string `json:"upload_id" form:"upload_id"`
}

// MultipartCompleteResp is the response for completing a multipart upload
type MultipartCompleteResp struct {
	Object *Obj  `json:"object,omitempty"`
	Task   any   `json:"task,omitempty"`
}
