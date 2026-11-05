package model

type HttpDirectUploadInfo struct {
	UploadURL string            `json:"upload_url"`        // The URL to upload the file
	ChunkSize int64             `json:"chunk_size"`        // The chunk size for uploading, 0 means no chunking required
	Headers   map[string]string `json:"headers,omitempty"` // Optional headers to include in the upload request
	Method    string            `json:"method,omitempty"`  // HTTP method, default is PUT
	// UploadURLs lists one presigned URL per part, in order. When it is not
	// empty the client must upload part i to UploadURLs[i] instead of
	// UploadURL, because such storages issue a different signature per part.
	UploadURLs []string `json:"upload_urls,omitempty"`
	// CompleteURL is called once after the last part succeeds, carrying the
	// ETag of every part, so the server can finalize the multipart upload.
	// It is relative to the API base ("/fs/..." style) when it does not start
	// with a scheme, and it already carries the session token in its query.
	CompleteURL    string `json:"complete_url,omitempty"`
	CompleteMethod string `json:"complete_method,omitempty"`
}
