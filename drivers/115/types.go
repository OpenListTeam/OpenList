package _115

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/SheltonZhu/115driver/pkg/driver"
)

var _ model.Obj = (*FileObj)(nil)

type FileObj struct {
	driver.File
}

func (f *FileObj) CreateTime() time.Time {
	return f.File.CreateTime
}

func (f *FileObj) GetHash() utils.HashInfo {
	return utils.NewHashInfo(utils.SHA1, f.Sha1)
}

func (f *FileObj) Thumb() string {
	return f.ThumbURL
}

type UploadResult struct {
	driver.BasicResp
	Data struct {
		PickCode string `json:"pick_code"`
		FileSize int    `json:"file_size"`
		FileID   string `json:"file_id"`
		ThumbURL string `json:"thumb_url"`
		Sha1     string `json:"sha1"`
		Aid      int    `json:"aid"`
		FileName string `json:"file_name"`
		Cid      string `json:"cid"`
		IsVideo  int    `json:"is_video"`
	} `json:"data"`
}
