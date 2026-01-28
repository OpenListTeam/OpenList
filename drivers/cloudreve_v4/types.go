package cloudreve_v4

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

const (
	MetadataUploadSessionID = "sys:upload_session_id"
	MetadataThumbDisabled   = "thumb:disabled"
)

type Object struct {
	model.Object
	StoragePolicy StoragePolicy
}

type Resp struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data any    `json:"data"`
}

type BasicConfigResp struct {
	InstanceID string `json:"instance_id"`
	// Title        string `json:"title"`
	// Themes       string `json:"themes"`
	// DefaultTheme string `json:"default_theme"`
	User struct {
		ID string `json:"id"`
		// Nickname  string    `json:"nickname"`
		// CreatedAt time.Time `json:"created_at"`
		// Anonymous bool      `json:"anonymous"`
		Group struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Permission string `json:"permission"`
		} `json:"group"`
	} `json:"user"`
	// Logo                string `json:"logo"`
	// LogoLight           string `json:"logo_light"`
	// CaptchaReCaptchaKey string `json:"captcha_ReCaptchaKey"`
	CaptchaType string `json:"captcha_type"` // support 'normal' only
	// AppPromotion        bool   `json:"app_promotion"`
}

type SiteLoginConfigResp struct {
	LoginCaptcha bool `json:"login_captcha"`
	// RegCaptcha bool `json:"reg_captcha"`
	// ForgetCaptcha bool `json:"forget_captcha"`
	// RegisterEnabled bool `json:"register_enabled"`
	// TosURL string `json:"tos_url"`
	// PrivacyPolicyURL string `json:"privacy_policy_url"`
	// SsoDisplayName string `json:"sso_display_name"`
	// OidcDisplayName string `json:"oidc_display_name"`
}

type PrepareLoginResp struct {
	WebauthnEnabled bool `json:"webauthn_enabled"`
	PasswordEnabled bool `json:"password_enabled"`
}

type CaptchaResp struct {
	Image  string `json:"image"`
	Ticket string `json:"ticket"`
}

type AccessJWT struct {
	TokenType string `json:"token_type"`
	Sub       string `json:"sub"`
	Exp       int64  `json:"exp"`
	Nbf       int64  `json:"nbf"`
}

type RefreshJWT struct {
	TokenType   string `json:"token_type"`
	Sub         string `json:"sub"`
	Exp         int    `json:"exp"`
	Nbf         int    `json:"nbf"`
	StateHash   string `json:"state_hash"`
	RootTokenID string `json:"root_token_id"`
}

type Token struct {
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	AccessExpires  string `json:"access_expires"`
	RefreshExpires string `json:"refresh_expires"`
}

type TokenResponse struct {
	User struct {
		ID string `json:"id"`
		// Email     string    `json:"email"`
		// Nickname  string    `json:"nickname"`
		Status string `json:"status"`
		// CreatedAt time.Time `json:"created_at"`
		Group struct {
			ID         string `json:"id"`
			Name       string `json:"name"`
			Permission string `json:"permission"`
			// DirectLinkBatchSize int    `json:"direct_link_batch_size"`
			// TrashRetention      int    `json:"trash_retention"`
		} `json:"group"`
		// Language string `json:"language"`
	} `json:"user"`
	Token Token `json:"token"`
}

type File struct {
	Type          int            `json:"type"` // 0: file, 1: folder
	ID            string         `json:"id"`
	Name          string         `json:"name"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	Size          int64          `json:"size"`
	Metadata      map[string]any `json:"metadata,omitempty"`
	Path          string         `json:"path"`
	Capability    string         `json:"capability"`
	Owned         bool           `json:"owned"`
	PrimaryEntity string         `json:"primary_entity"`
}

func fileToObject(f *File) *model.Object {
	return &model.Object{
		ID:       f.ID,
		Path:     f.Path,
		Name:     f.Name,
		Size:     f.Size,
		Modified: f.UpdatedAt,
		Ctime:    f.CreatedAt,
		IsFolder: f.Type == 1,
	}
}

type StoragePolicy struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	MaxSize int64  `json:"max_size"`
	Relay   bool   `json:"relay,omitempty"`
}

type Pagination struct {
	Page      int    `json:"page"`
	PageSize  int    `json:"page_size"`
	IsCursor  bool   `json:"is_cursor"`
	NextToken string `json:"next_token,omitempty"`
}

type Props struct {
	Capability            string   `json:"capability"`
	MaxPageSize           int      `json:"max_page_size"`
	OrderByOptions        []string `json:"order_by_options"`
	OrderDirectionOptions []string `json:"order_direction_options"`
}

type FileResp struct {
	Files         []File        `json:"files"`
	Parent        File          `json:"parent"`
	Pagination    Pagination    `json:"pagination"`
	Props         Props         `json:"props"`
	ContextHint   string        `json:"context_hint"`
	MixedType     bool          `json:"mixed_type"`
	StoragePolicy StoragePolicy `json:"storage_policy"`
}

type FileUrlResp struct {
	Urls []struct {
		URL string `json:"url"`
	} `json:"urls"`
	Expires time.Time `json:"expires"`
}

type FileUploadResp struct {
	// UploadID       string        `json:"upload_id"`
	SessionID      string        `json:"session_id"`
	ChunkSize      int64         `json:"chunk_size"`
	Expires        int64         `json:"expires"`
	StoragePolicy  StoragePolicy `json:"storage_policy"`
	URI            string        `json:"uri"`
	CompleteURL    string        `json:"completeURL,omitempty"`     // for S3-like
	CallbackSecret string        `json:"callback_secret,omitempty"` // for S3-like, OneDrive
	UploadUrls     []string      `json:"upload_urls,omitempty"`     // for not-local
	Credential     string        `json:"credential,omitempty"`      // for local
}

type FileDeleteResp struct {
	Resp
	Data []struct {
		Path  string `json:"path"`
		Token string `json:"token"`
		// Owner struct {
		// 	Owner       string `json:"owner"`
		// 	Application struct {
		// 		Type string `json:"type"`
		// 	} `json:"application"`
		// } `json:"owner"`
		Type int `json:"type"`
	} `json:"data,omitempty"`
}

type FileThumbResp struct {
	URL     string    `json:"url"`
	Expires time.Time `json:"expires"`
}

type FolderSummaryResp struct {
	File
	FolderSummary struct {
		Size         int64     `json:"size"`
		Files        int64     `json:"files"`
		Folders      int64     `json:"folders"`
		Completed    bool      `json:"completed"`
		CalculatedAt time.Time `json:"calculated_at"`
	} `json:"folder_summary"`
}

type CapacityResp struct {
	Total int64 `json:"total"`
	Used  int64 `json:"used"`
	// StoragePackTotal uint64 `json:"storage_pack_total"`
}
