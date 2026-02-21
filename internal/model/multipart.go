package model

import "time"

// MultipartUploadSession represents a multipart upload session
type MultipartUploadSession struct {
	UploadID       string             `json:"upload_id"`
	DstDirPath     string             `json:"dst_dir_path"`
	FileName       string             `json:"file_name"`
	FileSize       int64              `json:"file_size"`
	ChunkSize      int64              `json:"chunk_size"`
	TotalChunks    int                `json:"total_chunks"`
	ContentType    string             `json:"content_type"`
	ChunkDir       string             `json:"chunk_dir"`
	UploadedChunks map[int]ChunkInfo  `json:"uploaded_chunks"`
	Overwrite      bool               `json:"overwrite"`
	CreatedAt      time.Time          `json:"created_at"`
	ExpiresAt      time.Time          `json:"expires_at"`
}

// ChunkInfo represents information about an uploaded chunk
type ChunkInfo struct {
	Index      int       `json:"index"`
	Size       int64     `json:"size"`
	UploadedAt time.Time `json:"uploaded_at"`
}

// ChunkUploadResp is the response for uploading a chunk
type ChunkUploadResp struct {
	UploadID       string `json:"upload_id"`
	ChunkIndex     int    `json:"chunk_index"`
	UploadedChunks []int  `json:"uploaded_chunks"`
	UploadedBytes  int64  `json:"uploaded_bytes"`
	TotalChunks    int    `json:"total_chunks"`
}
