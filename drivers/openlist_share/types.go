package openlist_share

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type ListReq struct {
	model.PageReq
	Path     string `json:"path" form:"path"`
	Password string `json:"password" form:"password"`
	Refresh  bool   `json:"refresh"`
}

type ObjResp struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	IsDir    bool      `json:"is_dir"`
	Modified time.Time `json:"modified"`
	Created  time.Time `json:"created"`
	Sign     string    `json:"sign"`
	Thumb    string    `json:"thumb"`
	Type     int       `json:"type"`
	HashInfo string    `json:"hashinfo"`
}

type FsListResp struct {
	Content  []ObjResp `json:"content"`
	Total    int64     `json:"total"`
	Readme   string    `json:"readme"`
	Write    bool      `json:"write"`
	Provider string    `json:"provider"`
}

type ArchiveMetaReq struct {
	ArchivePass string `json:"archive_pass"`
	Password    string `json:"password"`
	Path        string `json:"path"`
	Refresh     bool   `json:"refresh"`
}

type TreeResp struct {
	ObjResp
	Children  []TreeResp `json:"children"`
	hashCache *utils.HashInfo
}

func (t *TreeResp) GetSize() int64 {
	return t.Size
}

func (t *TreeResp) GetName() string {
	return t.Name
}

func (t *TreeResp) ModTime() time.Time {
	return t.Modified
}

func (t *TreeResp) CreateTime() time.Time {
	return t.Created
}

func (t *TreeResp) IsDir() bool {
	return t.ObjResp.IsDir
}

func (t *TreeResp) GetHash() utils.HashInfo {
	return utils.FromString(t.HashInfo)
}

func (t *TreeResp) GetID() string {
	return ""
}

func (t *TreeResp) GetPath() string {
	return ""
}

func (t *TreeResp) GetChildren() []model.ObjTree {
	ret := make([]model.ObjTree, 0, len(t.Children))
	for _, child := range t.Children {
		ret = append(ret, &child)
	}
	return ret
}

func (t *TreeResp) Thumb() string {
	return t.ObjResp.Thumb
}

type ArchiveMetaResp struct {
	Comment   string     `json:"comment"`
	Encrypted bool       `json:"encrypted"`
	Content   []TreeResp `json:"content"`
	RawURL    string     `json:"raw_url"`
	Sign      string     `json:"sign"`
}

type ArchiveListReq struct {
	model.PageReq
	ArchiveMetaReq
	InnerPath string `json:"inner_path"`
}

type ArchiveListResp struct {
	Content []ObjResp `json:"content"`
	Total   int64     `json:"total"`
}
