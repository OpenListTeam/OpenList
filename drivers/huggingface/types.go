package huggingface

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type TreeEntry struct {
	Type string `json:"type"`
	Path string `json:"path"`
	Size int64  `json:"size"`
	SHA  string `json:"sha"`
}

func (e *TreeEntry) toModelObj() *model.Object {
	obj := &model.Object{
		Name:     e.Name(),
		Size:     e.Size,
		Modified: time.Unix(0, 0),
		IsFolder: e.Type == "directory",
		Path:     utils.FixAndCleanPath(e.Path),
	}
	if obj.IsFolder {
		obj.Size = 0
	}
	return obj
}

func (e *TreeEntry) Name() string {
	if idx := lastIndexByte(e.Path, '/'); idx >= 0 {
		return e.Path[idx+1:]
	}
	return e.Path
}

type ErrorResponse struct {
	Error string `json:"error"`
}

type PreuploadFile struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	Sample string `json:"sample"`
}

type PreuploadResponseEntry struct {
	Path        string `json:"path"`
	UploadMode  string `json:"uploadMode"`
	ShouldIgnore bool  `json:"shouldIgnore"`
	OID         string `json:"oid"`
}

type PreuploadResponse struct {
	Files []PreuploadResponseEntry `json:"files"`
}

type LFSRef struct {
	Name string `json:"name"`
}

type LFSObject struct {
	OID  string `json:"oid"`
	Size int64  `json:"size"`
}

type LFSBatchRequest struct {
	Operation string      `json:"operation"`
	Transfers []string    `json:"transfers"`
	HashAlgo  string      `json:"hash_algo"`
	Ref       LFSRef      `json:"ref"`
	Objects   []LFSObject `json:"objects"`
}

type LFSAction struct {
	Href   string            `json:"href"`
	Header map[string]interface{} `json:"header"`
}

type LFSBatchObject struct {
	OID     string                `json:"oid"`
	Size    int64                 `json:"size"`
	Actions map[string]LFSAction  `json:"actions"`
}

type LFSBatchResponse struct {
	Transfer string          `json:"transfer"`
	Objects  []LFSBatchObject `json:"objects"`
}

func lastIndexByte(s string, c byte) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c {
			return i
		}
	}
	return -1
}
