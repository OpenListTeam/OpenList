package rakuten_drive

import "github.com/OpenListTeam/OpenList/v4/internal/model"

type refreshResp struct {
	IDToken      string `json:"idToken"`
	RefreshToken string `json:"refreshToken"`
}

type downloadResp struct {
	URL string `json:"url"`
}

type uploadInitResp struct {
	UploadID string `json:"upload_id"`
	Prefix   string `json:"prefix"`
	Bucket   string `json:"bucket"`
	Region   string `json:"region"`
	File     []struct {
		Path         string `json:"path"`
		Size         int64  `json:"size"`
		VersionID    string `json:"version_id"`
		LastModified string `json:"last_modified"`
	} `json:"file"`
}

type uploadCheckResp struct {
	Action string `json:"action"`
	State  string `json:"state"`
}

type filelinkTokenResp struct {
	AccessKeyID     string `json:"AccessKeyId"`
	SecretAccessKey string `json:"SecretAccessKey"`
	SessionToken    string `json:"SessionToken"`
	Expiration      string `json:"Expiration"`
}

type File struct {
	model.Object
	VersionID    string
	LastModified string
}
