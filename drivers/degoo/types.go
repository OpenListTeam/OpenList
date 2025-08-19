package template

// DegooLoginRequest corresponds to the JSON body for the login request in the Python script.
type DegooLoginRequest struct {
	GenerateToken bool `json:"GenerateToken"`
	Username string `json:"Username"`
	Password string `json:"Password"`
}

// DegooLoginResponse corresponds to the successful response from a login() call.
type DegooLoginResponse struct {
	Token string `json:"Token"`
	RefreshToken string `json:"RefreshToken"`
}

// DegooAccessTokenRequest corresponds to the JSON body for a token refresh request.
type DegooAccessTokenRequest struct {
	RefreshToken string `json:"RefreshToken"`
}

// DegooAccessTokenResponse corresponds to the response for a token refresh.
type DegooAccessTokenResponse struct {
	AccessToken string `json:"AccessToken"`
}

// DegooFileItem corresponds to the properties of a Degoo file in the Python script.
type DegooFileItem struct {
	ID string `json:"ID"`
	ParentID string `json:"ParentID"`
	Name string `json:"Name"`
	Category int `json:"Category"`
	Size int64 `json:"Size"`
	URL string `json:"URL"`
	OptimizedURL string `json:"OptimizedURL"`
	CreationTime string `json:"CreationTime"`
	LastModificationTime string `json:"LastModificationTime"`
	LastUploadTime string `json:"LastUploadTime"`
	MetadataID string `json:"MetadataID"`
	DeviceID string `json:"DeviceID"`
	FilePath string `json:"FilePath"`
	IsInRecycleBin bool `json:"IsInRecycleBin"`
}

// DegooGraphqlResponse corresponds to the common response structure for all GraphQL APIs.
type DegooGraphqlResponse struct {
	Data json.RawMessage `json:"data"`
	Errors []map[string]interface{} `json:"errors"`
}

// DegooGetChildren5Data corresponds to the 'Data' field for getFileChildren5.
type DegooGetChildren5Data struct {
	GetFileChildren5 struct {
		Items []DegooFileItem `json:"Items"`
		NextToken string `json:"NextToken"`
	} `json:"getFileChildren5"`
}

// DegooGetOverlay4Data corresponds to the 'Data' field for getOverlay4.
type DegooGetOverlay4Data struct {
	GetOverlay4 DegooFileItem `json:"getOverlay4"`
}

// DegooFileRenameInfo corresponds to the 'FileRenames' field for setRenameFile.
type DegooFileRenameInfo struct {
	ID string `json:"ID"`
	NewName string `json:"NewName"`
}

// DegooFileIDs corresponds to the 'FileIDs' field for setMoveFile.
type DegooFileIDs struct {
	FileIDs []string `json:"FileIDs"`
}
