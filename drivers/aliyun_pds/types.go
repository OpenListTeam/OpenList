package aliyun_pds

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type RespErr struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type Files struct {
	Items      []File `json:"items"`
	NextMarker string `json:"next_marker"`
}

type File struct {
	DriveID         string     `json:"drive_id"`
	DomainID        string     `json:"domain_id"`
	FileID          string     `json:"file_id"`
	Name            string     `json:"name"`
	Size            int64      `json:"size"`
	Type            string     `json:"type"`
	CreatedAt       *time.Time `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
	ParentFileID    string     `json:"parent_file_id"`
	Thumbnail       string     `json:"thumbnail"`
	Url             string     `json:"url"`
	ContentHash     string     `json:"content_hash,omitempty"`
	ContentHashName string     `json:"content_hash_name,omitempty"`
}

func fileToObj(f File) *model.ObjThumb {
	var hashInfo utils.HashInfo
	if ht, ok := utils.GetHashByName(f.ContentHashName); ok {
		hashInfo = utils.NewHashInfo(ht, f.ContentHash)
	}
	return &model.ObjThumb{
		Object: model.Object{
			ID:       f.FileID,
			Name:     f.Name,
			Size:     f.Size,
			Modified: f.UpdatedAt,
			IsFolder: f.Type == "folder",
			HashInfo: hashInfo,
		},
		Thumbnail: model.Thumbnail{Thumbnail: f.Thumbnail},
	}
}

type UploadResp struct {
	FileID       string `json:"file_id"`
	UploadID     string `json:"upload_id"`
	PartInfoList []struct {
		UploadUrl         string `json:"upload_url"`
		InternalUploadUrl string `json:"internal_upload_url"`
	} `json:"part_info_list"`

	RapidUpload bool `json:"rapid_upload"`
}

type EndpointResp struct {
	AuthEndpoint   string `json:"auth_endpoint"`
	APIEndpoint    string `json:"api_endpoint"`
	UIEndpoint     string `json:"ui_endpoint"`
	ParentDomainID string `json:"parent_domain_id"`
	ClientID       string `json:"client_id"`
	RedirectURI    string `json:"redirect_uri"`
	ProductType    string `json:"product_type"`
	DomainID       string `json:"domain_id"`
	IsVpc          bool   `json:"is_vpc"`
	IsIntl         bool   `json:"is_intl"`
}

type DriveResp struct {
	DomainID          string    `json:"domain_id"`
	DriveID           string    `json:"drive_id"`
	DriveName         string    `json:"drive_name"`
	Description       string    `json:"description"`
	Creator           string    `json:"creator"`
	Owner             string    `json:"owner"`
	OwnerType         string    `json:"owner_type"`
	DriveType         string    `json:"drive_type"`
	Status            string    `json:"status"`
	UsedSize          int64     `json:"used_size"`
	TotalSize         int64     `json:"total_size"`
	StoreID           string    `json:"store_id"`
	RelativePath      string    `json:"relative_path"`
	EncryptMode       string    `json:"encrypt_mode"`
	EncryptDataAccess bool      `json:"encrypt_data_access"`
	CreatedAt         time.Time `json:"created_at"`
	UpdatedAt         time.Time `json:"updated_at"`
	Category          string    `json:"category"`
}
