package s3

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	driver.RootPath
	Bucket                   string `json:"bucket" required:"true"`
	Endpoint                 string `json:"endpoint" required:"true"`
	Region                   string `json:"region"`
	AccessKeyID              string `json:"access_key_id" required:"true"`
	SecretAccessKey          string `json:"secret_access_key" required:"true"`
	SessionToken             string `json:"session_token"`
	CustomHost               string `json:"custom_host"`
	EnableCustomHostPresign  bool   `json:"enable_custom_host_presign"`
	SignURLExpire            int    `json:"sign_url_expire" type:"number" default:"4"`
	Placeholder              string `json:"placeholder"`
	ForcePathStyle           bool   `json:"force_path_style"`
	ListObjectVersion        string `json:"list_object_version" type:"select" options:"v1,v2" default:"v1"`
	RemoveBucket             bool   `json:"remove_bucket" help:"Remove bucket name from path when using custom host."`
	AddFilenameToDisposition bool   `json:"add_filename_to_disposition" help:"Add filename to Content-Disposition header."`
	EnableDirectUpload       bool   `json:"enable_direct_upload" default:"false"`
	DirectUploadHost         string `json:"direct_upload_host" required:"false"`
	DirectUploadMaxParts     int64  `json:"direct_upload_max_parts" type:"number" default:"10000" help:"Maximum number of parts for direct multipart upload. Set to 1 to disable multipart and use single-part upload. Valid range: 1-10000."`
	DirectUploadMinPartSize  int64  `json:"direct_upload_min_part_size" type:"number" default:"104857600" help:"Minimum part size for frontend multipart upload, in bytes."`
	UserAgent                string `json:"user_agent" required:"false" default:"" help:"Custom User-Agent for S3 requests."`
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &S3{
			config: driver.Config{
				Name:        "S3",
				DefaultRoot: "/",
				LocalSort:   true,
				CheckStatus: true,
			},
		}
	})
	op.RegisterDriver(func() driver.Driver {
		return &S3{
			config: driver.Config{
				Name:        "Doge",
				DefaultRoot: "/",
				LocalSort:   true,
				CheckStatus: true,
			},
		}
	})
}
