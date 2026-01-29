package seafile

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type AuthTokenResp struct {
	Token string `json:"token"`
}

type RepoItemResp struct {
	Id         string `json:"id"`
	Type       string `json:"type"` // repo, dir, file
	Name       string `json:"name"`
	Size       int64  `json:"size"`
	Modified   int64  `json:"mtime"`
	Permission string `json:"permission"`

	path string
	model.ObjMask
	repoID string
}

func (l *RepoItemResp) IsDir() bool {
	return l.Type == "dir"
}
func (l *RepoItemResp) GetPath() string {
	return l.path
}
func (l *RepoItemResp) GetName() string {
	return l.Name
}
func (l *RepoItemResp) ModTime() time.Time {
	return time.Unix(l.Modified, 0)
}
func (l *RepoItemResp) CreateTime() time.Time {
	return l.ModTime()
}
func (l *RepoItemResp) GetSize() int64 {
	return l.Size
}
func (l *RepoItemResp) GetID() string {
	if l.repoID != "" {
		return l.repoID
	}
	return l.Id
}
func (l *RepoItemResp) GetHash() utils.HashInfo {
	return utils.HashInfo{}
}

var _ model.Obj = (*RepoItemResp)(nil)

type LibraryItemResp struct {
	RepoItemResp
	OwnerContactEmail    string `json:"owner_contact_email"`
	OwnerName            string `json:"owner_name"`
	Owner                string `json:"owner"`
	ModifierEmail        string `json:"modifier_email"`
	ModifierContactEmail string `json:"modifier_contact_email"`
	ModifierName         string `json:"modifier_name"`
	Virtual              bool   `json:"virtual"`
	MtimeRelative        string `json:"mtime_relative"`
	Encrypted            bool   `json:"encrypted"`
	Version              int    `json:"version"`
	HeadCommitId         string `json:"head_commit_id"`
	Root                 string `json:"root"`
	Salt                 string `json:"salt"`
	SizeFormatted        string `json:"size_formatted"`
}

type LibraryInfo struct {
	LibraryItemResp
	decryptedTime    time.Time
	decryptedSuccess bool
}

func (l *LibraryInfo) IsDir() bool {
	return true
}
