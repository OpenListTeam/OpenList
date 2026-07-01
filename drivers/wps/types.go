package wps

import (
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type apiResult struct {
	Result string `json:"result"`
	Msg    string `json:"msg"`
}

type loginState struct {
	AccountNum       int    `json:"account_num"`
	CompanyID        int64  `json:"companyid"`
	CurrentCompanyID int64  `json:"current_companyid"`
	IsCompanyAccount bool   `json:"is_company_account"`
	IsPlus           bool   `json:"is_plus"`
	LoginMode        string `json:"loginmode"`
	UserID           int64  `json:"userid"`
}

type Group struct {
	CompanyID int64  `json:"company_id"`
	GroupID   int64  `json:"group_id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
}

type groupsResp struct {
	Groups []Group `json:"groups"`
}

type personalGroupsResp struct {
	apiResult
	Groups []struct {
		ID   int64  `json:"id"`
		Name string `json:"name"`
	} `json:"groups"`
}

type filePerms struct {
	Download int `json:"download"`
}

type FileInfo struct {
	GroupID   int64     `json:"groupid"`
	ParentID  int64     `json:"parentid"`
	Name      string    `json:"fname"`
	Size      int64     `json:"fsize"`
	Type      string    `json:"ftype"`
	Ctime     int64     `json:"ctime"`
	Mtime     int64     `json:"mtime"`
	ID        int64     `json:"id"`
	Deleted   bool      `json:"deleted"`
	FilePerms filePerms `json:"file_perms_acl"`
}

func (f *FileInfo) canDownload(isPersonal bool) bool {
	if f == nil || f.Type == "folder" {
		return false
	}
	if f.FilePerms.Download != 0 {
		return true
	}
	return isPersonal
}

func (f FileInfo) fileToObj(basePath string, isPersonal bool) *Obj {
	name := f.Name
	path := joinPath(basePath, name)
	kind := "file"
	if f.Type == "folder" {
		kind = "folder"
	}
	obj := &Obj{
		Obj: &model.Object{
			ID:       strconv.FormatInt(f.ID, 10),
			Path:     path,
			Name:     name,
			Size:     f.Size,
			Modified: parseTime(f.Mtime),
			Ctime:    parseTime(f.Ctime),
			IsFolder: f.Type == "folder",
		},
		Kind:        kind,
		FileID:      f.ID,
		GroupID:     f.GroupID,
		HasFile:     true,
		CanDownload: f.canDownload(isPersonal),
	}
	return obj
}

func (g Group) groupToObj(basePath string) *Obj {
	return &Obj{
		Obj: &model.Object{
			ID:       strconv.FormatInt(g.GroupID, 10),
			Path:     joinPath(basePath, g.Name),
			Name:     g.Name,
			IsFolder: true,
		},
		Kind:    "group",
		GroupID: g.GroupID,
	}
}

type filesResp struct {
	Files      []FileInfo `json:"files"`
	NextOffset int        `json:"next_offset"`
}

type downloadResp struct {
	URL    string `json:"url"`
	Result string `json:"result"`
}

type spacesResp struct {
	Id        int64  `json:"id"`
	Name      string `json:"name"`
	Result    string `json:"result"`
	Total     int64  `json:"total"`
	Used      int64  `json:"used"`
	UsedParts []struct {
		Type string `json:"type"`
		Used int64  `json:"used"`
	} `json:"used_parts"`
}

type serviceSpaceResp struct {
	Info []struct {
		ID         int64 `json:"id"`
		SpaceTotal int64 `json:"space_total"`
		SpaceUsed  int64 `json:"space_used"`
	} `json:"info"`
}

type uploadCreateUpdateResp struct {
	apiResult
	Method  string `json:"method"`
	URL     string `json:"url"`
	Store   string `json:"store"`
	Request struct {
		Headers  map[string]string `json:"headers"`
		FormData map[string]string `json:"formData"`
	} `json:"request"`
	Response struct {
		ExpectCode []int  `json:"expect_code"`
		ArgsETag   string `json:"args_etag"`
		ArgsKey    string `json:"args_key"`
	} `json:"response"`
}

type uploadPutResp struct {
	NewFilename string `json:"newfilename"`
	Sha1        string `json:"sha1"`
	MD5         string `json:"md5"`
}

type Obj struct {
	model.Obj
	Kind        string // root / group / file / folder
	FileID      int64
	GroupID     int64
	HasFile     bool // only FileInfo has file, otherwise the FileID is 0
	CanDownload bool
}
