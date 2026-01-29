package degoo

import (
	"encoding/json"
)

// DegooLoginRequest represents the login request body.
type DegooLoginRequest struct {
	GenerateToken bool   `json:"GenerateToken"`
	Username      string `json:"Username"`
	Password      string `json:"Password"`
}

// DegooLoginResponse represents a successful login response.
type DegooLoginResponse struct {
	Token        string `json:"Token"`
	RefreshToken string `json:"RefreshToken"`
}

// DegooAccessTokenRequest represents the token refresh request body.
type DegooAccessTokenRequest struct {
	RefreshToken string `json:"RefreshToken"`
}

// DegooAccessTokenResponse represents the token refresh response.
type DegooAccessTokenResponse struct {
	AccessToken string `json:"AccessToken"`
}

// DegooFileItem represents a Degoo file or folder.
type DegooFileItem struct {
	ID                   string `json:"ID"`
	ParentID             string `json:"ParentID"`
	Name                 string `json:"Name"`
	Category             int    `json:"Category"`
	Size                 string `json:"Size"`
	URL                  string `json:"URL"`
	CreationTime         string `json:"CreationTime"`
	LastModificationTime string `json:"LastModificationTime"`
	LastUploadTime       string `json:"LastUploadTime"`
	MetadataID           string `json:"MetadataID"`
	DeviceID             int64  `json:"DeviceID"`
	FilePath             string `json:"FilePath"`
	IsInRecycleBin       bool   `json:"IsInRecycleBin"`
}

type DegooErrors struct {
	Path      []string    `json:"path"`
	Data      interface{} `json:"data"`
	ErrorType string      `json:"errorType"`
	ErrorInfo interface{} `json:"errorInfo"`
	Message   string      `json:"message"`
}

// DegooGraphqlResponse is the common structure for GraphQL API responses.
type DegooGraphqlResponse struct {
	Data   json.RawMessage `json:"data"`
	Errors []DegooErrors   `json:"errors,omitempty"`
}

// DegooGetChildren5Data is the data field for getFileChildren5.
type DegooGetChildren5Data struct {
	GetFileChildren5 struct {
		Items     []DegooFileItem `json:"Items"`
		NextToken string          `json:"NextToken"`
	} `json:"getFileChildren5"`
}

// DegooGetOverlay4Data is the data field for getOverlay4.
type DegooGetOverlay4Data struct {
	GetOverlay4 DegooFileItem `json:"getOverlay4"`
}

// DegooFileRenameInfo represents a file rename operation.
type DegooFileRenameInfo struct {
	ID      string `json:"ID"`
	NewName string `json:"NewName"`
}

// DegooFileIDs represents a list of file IDs for move operations.
type DegooFileIDs struct {
	FileIDs []string `json:"FileIDs"`
}

// DegooGetBucketWriteAuth4Data is the data field for GetBucketWriteAuth4.
type DegooGetBucketWriteAuth4Data struct {
	GetBucketWriteAuth4 []struct {
		AuthData struct {
			PolicyBase64 string `json:"PolicyBase64"`
			Signature    string `json:"Signature"`
			BaseURL      string `json:"BaseURL"`
			KeyPrefix    string `json:"KeyPrefix"`
			AccessKey    struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"AccessKey"`
			ACL            string `json:"ACL"`
			AdditionalBody []struct {
				Key   string `json:"Key"`
				Value string `json:"Value"`
			} `json:"AdditionalBody"`
		} `json:"AuthData"`
		Error interface{} `json:"Error"`
	} `json:"getBucketWriteAuth4"`
}

// DegooSetUploadFile3Data is the data field for SetUploadFile3.
type DegooSetUploadFile3Data struct {
	SetUploadFile3 bool `json:"setUploadFile3"`
}

type DegooGetUserInfo3Data struct {
	GetUserInfo3 struct {
		// ID string
		// FirstName string
		// LastName string
		// Email string
		// AvatarURL string
		// CountryCode string = CN
		// LanguageCode string = zh-cn
		// Phone string
		// AccountType int
		UsedQuota  string `json:"UsedQuota"`
		TotalQuota string `json:"TotalQuota"`
		// OAuth2Provider
		// GPMigrationStatus int
		// FeatureNoAds bool
		// FeatureTopSecret bool
		// FeatureDownsampling bool
		// FeatureAutomaticVideoUploads bool
		// FileSizeLimit string
	} `json:"getUserInfo3"`
}
