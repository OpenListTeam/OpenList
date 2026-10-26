package _189pc

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	LoginType    string `json:"login_type" type:"select" options:"password,qrcode" default:"password" required:"true"`
	Username     string `json:"username" help:"Not needed when an access token or refresh token is provided"`
	Password     string `json:"password" help:"Not needed when an access token or refresh token is provided"`
	VCode        string `json:"validate_code"`
	SmsCode      string `json:"sms_code" help:"SMS code for the second device verification, fill it in and save again when login asks for it"`
	AccessToken  string `json:"access_token" required:"false"`
	RefreshToken string `json:"refresh_token" help:"To switch accounts, please clear this field"`
	DeviceID     string `json:"device_id" help:"DEVICEID cookie issued after the second device verification, keep it to avoid verifying again"`
	ClientSn     string `json:"client_sn" help:"Device serial number captured from the official client, leave it empty if you do not have one"`
	JgOpenId     string `json:"jg_open_id" help:"Optional push id reported by the official client"`
	UserFinger   string `json:"user_finger" help:"Device fingerprint sent with login requests, generated and kept automatically when empty"`
	driver.RootID
	OrderBy         string `json:"order_by" type:"select" options:"filename,filesize,lastOpTime" default:"filename"`
	OrderDirection  string `json:"order_direction" type:"select" options:"asc,desc" default:"asc"`
	Type            string `json:"type" type:"select" options:"personal,family" default:"personal"`
	FamilyID        string `json:"family_id"`
	UploadMethod    string `json:"upload_method" type:"select" options:"stream,rapid,old" default:"stream"`
	UploadThread    string `json:"upload_thread" default:"3" help:"1<=thread<=32"`
	FamilyTransfer  bool   `json:"family_transfer"`
	RapidUpload     bool   `json:"rapid_upload"`
	NoUseOcr        bool   `json:"no_use_ocr"`
	GenerateTorrent bool   `json:"generate_torrent" help:"Generate torrent file with CAS extension after upload"`
}

var config = driver.Config{
	Name:        "189CloudPC",
	DefaultRoot: "-11",
	CheckStatus: true,
}

func init() {
	op.RegisterDriver(func() driver.Driver {
		return &Cloud189PC{}
	})
}
