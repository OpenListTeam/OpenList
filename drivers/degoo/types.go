package template

// DegooLoginRequest 对应 Python 脚本中登录请求的 JSON Body
type DegooLoginRequest struct {
	GenerateToken bool   `json:"GenerateToken"`
	Username      string `json:"Username"`
	Password      string `json:"Password"`
}

// DegooLoginResponse 对应 login() 成功后的响应
type DegooLoginResponse struct {
	Token        string `json:"Token"`
	RefreshToken string `json:"RefreshToken"`
}

// DegooAccessTokenRequest 对应刷新令牌请求的 JSON Body
type DegooAccessTokenRequest struct {
	RefreshToken string `json:"RefreshToken"`
}

// DegooAccessTokenResponse 对应刷新令牌响应
type DegooAccessTokenResponse struct {
	AccessToken string `json:"AccessToken"`
}

// DegooFileItem 对应 Python 脚本中 Degoo 文件的属性
type DegooFileItem struct {
	ID                 string `json:"ID"`
	ParentID           string `json:"ParentID"`
	Name               string `json:"Name"`
	Category           int    `json:"Category"`
	Size               int64  `json:"Size"`
	URL                string `json:"URL"`
	OptimizedURL       string `json:"OptimizedURL"`
	CreationTime       string `json:"CreationTime"`
	LastModificationTime string `json:"LastModificationTime"`
	LastUploadTime       string `json:"LastUploadTime"`
	MetadataID         string `json:"MetadataID"`
	DeviceID           string `json:"DeviceID"`
	FilePath           string `json:"FilePath"`
	IsInRecycleBin     bool   `json:"IsInRecycleBin"`
}

// DegooGraphqlResponse 对应所有 GraphQL API 的通用响应结构
type DegooGraphqlResponse struct {
	Data   json.RawMessage          `json:"data"`
	Errors []map[string]interface{} `json:"errors"`
}

// DegooGetChildren5Data 对应 getFileChildren5 的 Data 字段
type DegooGetChildren5Data struct {
	GetFileChildren5 struct {
		Items     []DegooFileItem `json:"Items"`
		NextToken string          `json:"NextToken"`
	} `json:"getFileChildren5"`
}

// DegooGetOverlay4Data 对应 getOverlay4 的 Data 字段
type DegooGetOverlay4Data struct {
	GetOverlay4 DegooFileItem `json:"getOverlay4"`
}

// DegooFileRenameInfo 对应 setRenameFile 的 FileRenames 字段
type DegooFileRenameInfo struct {
	ID      string `json:"ID"`
	NewName string `json:"NewName"`
}

// DegooFileIDs 对应 setMoveFile 的 FileIDs 字段
type DegooFileIDs struct {
	FileIDs []string `json:"FileIDs"`
}
