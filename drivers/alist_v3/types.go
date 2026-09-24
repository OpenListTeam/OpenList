package alist_v3

import (
	"encoding/json"

	family "github.com/OpenListTeam/OpenList/v4/drivers/internal/openlist"
)

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
type DecompressReq = family.DecompressReq

type MeResp struct {
	Id         int      `json:"id"`
	Username   string   `json:"username"`
	Password   string   `json:"password"`
	BasePath   string   `json:"base_path"`
	Role       IntSlice `json:"role"`
	Disabled   bool     `json:"disabled"`
	Permission int      `json:"permission"`
	SsoId      string   `json:"sso_id"`
	Otp        bool     `json:"otp"`
}

type IntSlice []int

func (s *IntSlice) UnmarshalJSON(b []byte) error {
	var i int
	if json.Unmarshal(b, &i) == nil {
		*s = []int{i}
		return nil
	}
	return json.Unmarshal(b, (*[]int)(s))
}
