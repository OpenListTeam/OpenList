package teldrive_v2

import (
	"fmt"
	"time"
)

// ErrResp is the v2 error envelope: {"error":{"code":"...","message":"..."}}
type ErrResp struct {
	Detail struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details,omitempty"`
	} `json:"error"`
	status int
}

func (e *ErrResp) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("[TeldriveV2] %d %s: %s", e.status, e.Detail.Code, e.Detail.Message)
}

func (e *ErrResp) IsNotFound() bool { return e != nil && e.status == 404 }

// FileKind values
const (
	KindFile   = "file"
	KindFolder = "folder"
)

// NameConflictPolicy values
const (
	PolicyFail    = "fail"
	PolicyReplace = "replace"
	PolicyRename  = "rename"
)

type FileHash struct {
	Algorithm string `json:"algorithm"`
	Value     string `json:"value"`
}

// FileEntry is the v2 replacement for the v1 Object.
// Note the renames: type -> kind, updatedAt -> modTime.
type FileEntry struct {
	ID         string    `json:"id"`
	ParentID   string    `json:"parentId,omitempty"`
	Name       string    `json:"name"`
	Kind       string    `json:"kind"`
	MimeType   string    `json:"mimeType,omitempty"`
	Size       int64     `json:"size,omitempty"`
	Hash       *FileHash `json:"hash,omitempty"`
	Encryption bool      `json:"encryption"`
	Status     string    `json:"status"`
	ModTime    time.Time `json:"modTime"`
	Generation int64     `json:"generation"`
	CreatedAt  time.Time `json:"createdAt"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

// ListResp is cursor-paginated; v1's meta.totalPages is gone.
type ListResp struct {
	Items      []FileEntry `json:"items"`
	NextCursor string      `json:"nextCursor,omitempty"`
}

type UploadSession struct {
	ID             string    `json:"id"`
	ParentID       string    `json:"parentId,omitempty"`
	Name           string    `json:"name"`
	ExpectedSize   int64     `json:"expectedSize"`
	MimeType       string    `json:"mimeType,omitempty"`
	ModTime        time.Time `json:"modTime"`
	Encryption     bool      `json:"encryption"`
	ConflictPolicy string    `json:"conflictPolicy"`
	PartSize       int64     `json:"partSize"`
	State          string    `json:"state"`
	ExpiresAt      time.Time `json:"expiresAt"`
	CreatedAt      time.Time `json:"createdAt"`
}

type UploadPart struct {
	UploadID   string `json:"uploadId"`
	PartNo     int    `json:"partNo"`
	State      string `json:"state"`
	PlainSize  int64  `json:"plainSize"`
	StoredSize int64  `json:"storedSize,omitempty"`
	Checksum   string `json:"checksum,omitempty"`
}

type UploadPartList struct {
	Items      []UploadPart `json:"items"`
	NextCursor string       `json:"nextCursor,omitempty"`
}

// ShareCreated is returned only once, at creation time.
type ShareCreated struct {
	ID                string    `json:"id"`
	FileID            string    `json:"fileId"`
	Token             string    `json:"token"`
	PublicURL         string    `json:"publicUrl"`
	PasswordProtected bool      `json:"passwordProtected"`
	Permission        string    `json:"permission"`
	ExpiresAt         time.Time `json:"expiresAt,omitempty"`
	CreatedAt         time.Time `json:"createdAt"`
}

type UploadSessionList struct {
	Items      []UploadSession `json:"items"`
	NextCursor string          `json:"nextCursor,omitempty"`
}

// UploadState values
const (
	UploadOpen       = "open"
	UploadPartStored = "stored"
)
