package model

type HttpDirectUploadInfo struct {
	UploadURL string            `json:"upload_url"`        // The URL to upload the file
	ChunkSize int64             `json:"chunk_size"`        // The chunk size for uploading, 0 means no chunking required
	Headers   map[string]string `json:"headers,omitempty"` // Optional headers to include in the upload request
	Method    string            `json:"method,omitempty"`  // HTTP method, default is PUT
}

// S3MultipartDirectUploadInfo contains presigned URLs for an S3 multipart upload.
// Parts, completion, and cancellation are sent directly to object storage.
type S3MultipartDirectUploadInfo struct {
	ChunkSize   int64    `json:"chunk_size"`
	UploadURLs  []string `json:"upload_urls"`
	CompleteURL string   `json:"complete_url"`
	AbortURL    string   `json:"abort_url"`
}
