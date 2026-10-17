package openlist

import family "github.com/OpenListTeam/OpenList/v4/drivers/internal/openlist"

type ListReq = family.ListReq
type ObjResp = family.ObjResp
type FsListResp = family.FsListResp
type FsGetReq = family.FsGetReq
type FsGetResp = family.FsGetResp
type MkdirOrLinkReq = family.MkdirOrLinkReq
type MoveCopyReq = family.MoveCopyReq
type RenameReq = family.RenameReq
type RemoveReq = family.RemoveReq
type LoginResp = family.LoginResp
type ArchiveMetaReq = family.ArchiveMetaReq
type TreeResp = family.TreeResp
type ArchiveMetaResp = family.ArchiveMetaResp
type ArchiveListReq = family.ArchiveListReq
type ArchiveListResp = family.ArchiveListResp

type MeResp struct {
	Id         int    `json:"id"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	BasePath   string `json:"base_path"`
	Role       int    `json:"role"`
	Disabled   bool   `json:"disabled"`
	Permission int    `json:"permission"`
	SsoId      string `json:"sso_id"`
	Otp        bool   `json:"otp"`
}

type DecompressReq struct {
	ArchivePass   string   `json:"archive_pass"`
	CacheFull     bool     `json:"cache_full"`
	DstDir        string   `json:"dst_dir"`
	InnerPath     string   `json:"inner_path"`
	Name          []string `json:"name"`
	PutIntoNewDir bool     `json:"put_into_new_dir"`
	SrcDir        string   `json:"src_dir"`
	Overwrite     bool     `json:"overwrite"`
}
