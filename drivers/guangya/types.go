package guangya

import "time"

// ---- Auth API ----

type tokenResp struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	Sub          string `json:"sub"`
	Error        string `json:"error"`
	ErrorCode    int    `json:"error_code"`
	ErrorDesc    string `json:"error_description"`
}

type verificationResp struct {
	VerificationID string `json:"verification_id"`
	Error          string `json:"error"`
	ErrorCode      int    `json:"error_code"`
	ErrorDesc      string `json:"error_description"`
}

type captchaInitResp struct {
	CaptchaToken string `json:"captcha_token"`
	ExpiresIn    int64  `json:"expires_in"`
	Error        string `json:"error"`
	ErrorCode    int    `json:"error_code"`
	ErrorDesc    string `json:"error_description"`
}

type verifyResp struct {
	VerificationToken string `json:"verification_token"`
	Error             string `json:"error"`
	ErrorCode         int    `json:"error_code"`
	ErrorDesc         string `json:"error_description"`
}

// ---- File API ----

type listResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Total int        `json:"total"`
		List  []fileItem `json:"list"`
	} `json:"data"`
}

type fileItem struct {
	FileID   string `json:"fileId"`
	ParentID string `json:"parentId"`
	FileName string `json:"fileName"`
	FileSize int64  `json:"fileSize"`
	ResType  int    `json:"resType"` // 2 = folder
	CTime    int64  `json:"ctime"`
	UTime    int64  `json:"utime"`
}

type downloadResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		SignedURL   string `json:"signedURL"`
		DownloadURL string `json:"downloadUrl"`
	} `json:"data"`
}

// ---- Common ----

type commonResp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
}

// ---- Helper ----

func unixOrZero(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}
