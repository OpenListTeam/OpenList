package _189pc

import (
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

type Addition struct {
	LoginType    string `json:"login_type" type:"select" options:"password,qrcode" default:"password" required:"true"`
	Username     string `json:"username" required:"true"`
	Password     string `json:"password" required:"true"`
	VCode        string `json:"validate_code"`
	AccessToken  string `json:"access_token" required:"false"`
	RefreshToken string `json:"refresh_token" help:"To switch accounts, please clear this field"`
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

	// 跨账号家庭转移：使用第二账号（上传号）上传到家庭云，再由当前账号转存到个人云
	SecondAccountTransfer bool   `json:"second_account_transfer" help:"Upload to the family cloud with the second account, then transfer the file to the personal cloud of the current account. Auto enables family_transfer"`
	SecondUsername        string `json:"second_username" help:"Username of the second account used for family cloud upload"`
	SecondPassword        string `json:"second_password" help:"Password of the second account used for family cloud upload"`
	SecondAccessToken     string `json:"second_access_token" required:"false"`
	SecondRefreshToken    string `json:"second_refresh_token" help:"To switch the second account, please clear this field"`
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
