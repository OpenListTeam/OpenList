package modelscope

import (
	stdpath "path"
	"strings"
	"time"
)

// FileEntry is a single entry of the ModelScope file-tree listing.
//
// ModelScope's legacy API returns entries with PascalCase keys (Path, Size,
// Sha256, Type, ...) but the OpenAPI uses snake_case. We accept both so the
// driver keeps working whichever endpoint shape the server returns.
type FileEntry struct {
	Path          string `json:"Path"`
	Name          string `json:"Name"`
	Type          string `json:"Type"`
	Size          int64  `json:"Size"`
	Sha256        string `json:"Sha256"`
	LastModified  any    `json:"LastModified"`
	CommittedDate any    `json:"CommittedDate"`
	IsLFS         bool   `json:"IsLFS"`

	// snake_case fallbacks (OpenAPI-style)
	PathSnake         string `json:"path"`
	NameSnake         string `json:"name"`
	TypeSnake         string `json:"type"`
	SizeSnake         int64  `json:"size"`
	Sha256Snake       string `json:"sha256"`
	LastModifiedSnake any    `json:"last_modified"`
}

func (f *FileEntry) getPath() string {
	if f.Path != "" {
		return f.Path
	}
	return f.PathSnake
}

func (f *FileEntry) getType() string {
	if f.Type != "" {
		return f.Type
	}
	return f.TypeSnake
}

func (f *FileEntry) getSize() int64 {
	if f.Size != 0 {
		return f.Size
	}
	return f.SizeSnake
}

func (f *FileEntry) isDir() bool {
	t := f.getType()
	return t == "tree" || t == "dir"
}

// modified returns the best-available modification timestamp.
func (f *FileEntry) modified() any {
	if f.LastModified != nil {
		return f.LastModified
	}
	if f.CommittedDate != nil {
		return f.CommittedDate
	}
	return f.LastModifiedSnake
}

// fileListResp is the unwrapped body of a file-tree listing. ModelScope wraps
// the payload in a {"Data": {...}} envelope; the outer pagination fields
// (TotalCount/PageNumber/PageSize) live outside Data on the top-level object.
type fileListResp struct {
	Files      []FileEntry `json:"Files"`
	TotalCount int         `json:"TotalCount"`
	PageNumber int         `json:"PageNumber"`
	PageSize   int         `json:"PageSize"`
}

// fileListEnvelope is the full top-level response including the Data wrapper.
type fileListEnvelope struct {
	Data       fileListResp `json:"Data"`
	TotalCount int          `json:"TotalCount"`
	PageNumber int          `json:"PageNumber"`
	PageSize   int          `json:"PageSize"`
}

// rawFileList is used when the listing body is a bare JSON array.
type rawFileList []FileEntry

type errResp struct {
	Code    int    `json:"Code"`
	Message string `json:"Message"`
	Msg     string `json:"message"`
}

func (e *errResp) text() string {
	if e.Message != "" {
		return e.Message
	}
	return e.Msg
}

// lfsBatchReq is the LFS batch API request body.
type lfsBatchReq struct {
	Operation string      `json:"operation"`
	Objects   []lfsObject `json:"objects"`
}

type lfsObject struct {
	Oid  string `json:"oid"`
	Size int64  `json:"size"`
}

type lfsAction struct {
	Href string `json:"href"`
}

type lfsRespObject struct {
	Oid     string               `json:"oid"`
	Actions map[string]lfsAction `json:"actions"`
}

type lfsBatchResp struct {
	Objects []lfsRespObject `json:"objects"`
	Data    lfsBatchData    `json:"Data"`
}

// objects returns the blob objects, preferring the top-level "objects" field
// and falling back to the "Data.objects" envelope.
func (r *lfsBatchResp) objects() []lfsRespObject {
	if len(r.Objects) > 0 {
		return r.Objects
	}
	return r.Data.Objects
}

type lfsBatchData struct {
	Objects []lfsRespObject `json:"objects"`
}

// commitAction is one file operation inside a commit request.
//
// action: create | update | delete
// type:   normal | lfs
type commitAction struct {
	Action   string `json:"action"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Size     int64  `json:"size"`
	Sha256   string `json:"sha256"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
}

type commitReq struct {
	CommitMessage string         `json:"commit_message"`
	Actions       []commitAction `json:"actions"`
}

// normalizePath returns a clean, repo-relative path (no leading slash) with
// forward slashes. An empty input or "/" maps to "" (the repository root).
// ModelScope repo paths are git-style relative paths, so the leading slash
// must be stripped.
func normalizePath(p string) string {
	p = strings.ReplaceAll(p, "\\", "/")
	p = stdpath.Clean(p)
	// Strip a single leading slash to get a repo-relative path.
	p = strings.TrimPrefix(p, "/")
	if p == "." {
		return ""
	}
	return p
}

// joinRepoPath joins a parent directory with a child name into a repo-relative
// path (no leading slash).
func joinRepoPath(parent, name string) string {
	if parent == "" {
		return normalizePath(name)
	}
	return normalizePath(parent + "/" + name)
}

// toTime converts the flexible LastModified field into a time.Time.
func toTime(v any) time.Time {
	switch t := v.(type) {
	case time.Time:
		return t
	case int64:
		return time.Unix(t, 0)
	case float64:
		// ModelScope may return seconds or milliseconds.
		if t > 1e12 {
			return time.UnixMilli(int64(t))
		}
		return time.Unix(int64(t), 0)
	case string:
		if t == "" {
			return time.Time{}
		}
		parsed, err := time.Parse(time.RFC3339, t)
		if err != nil {
			return time.Time{}
		}
		return parsed
	default:
		return time.Time{}
	}
}
