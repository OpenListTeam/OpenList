package cnb_releases

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Object struct {
	model.Object
	ParentID string
}

type TagList []Tag

type Tag struct {
	Commit struct {
		Author    UserInfo       `json:"author"`
		Commit    CommitObject   `json:"commit"`
		Committer UserInfo       `json:"committer"`
		Parents   []CommitParent `json:"parents"`
		Sha       string         `json:"sha"`
	} `json:"commit"`
	Name         string                `json:"name"`
	Target       string                `json:"target"`
	TargetType   string                `json:"target_type"`
	Verification TagObjectVerification `json:"verification"`
}

type UserInfo struct {
	Freeze   bool   `json:"freeze"`
	Nickname string `json:"nickname"`
	Username string `json:"username"`
}

type CommitObject struct {
	Author       Signature                `json:"author"`
	CommentCount int                      `json:"comment_count"`
	Committer    Signature                `json:"committer"`
	Message      string                   `json:"message"`
	Tree         CommitObjectTree         `json:"tree"`
	Verification CommitObjectVerification `json:"verification"`
}

type Signature struct {
	Date  time.Time `json:"date"`
	Email string    `json:"email"`
	Name  string    `json:"name"`
}

type CommitObjectTree struct {
	Sha string `json:"sha"`
}

type CommitObjectVerification struct {
	Payload    string `json:"payload"`
	Reason     string `json:"reason"`
	Signature  string `json:"signature"`
	Verified   bool   `json:"verified"`
	VerifiedAt string `json:"verified_at"`
}

type CommitParent = CommitObjectTree

type TagObjectVerification = CommitObjectVerification

type ReleaseList []Release

type Release struct {
	Assets       []ReleaseAsset `json:"assets"`
	Author       UserInfo       `json:"author"`
	Body         string         `json:"body"`
	CreatedAt    time.Time      `json:"created_at"`
	Draft        bool           `json:"draft"`
	ID           string         `json:"id"`
	IsLatest     bool           `json:"is_latest"`
	Name         string         `json:"name"`
	Prerelease   bool           `json:"prerelease"`
	PublishedAt  time.Time      `json:"published_at"`
	TagCommitish string         `json:"tag_commitish"`
	TagName      string         `json:"tag_name"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type ReleaseAsset struct {
	ContentType string    `json:"content_type"`
	CreatedAt   time.Time `json:"created_at"`
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	Path        string    `json:"path"`
	Size        int64     `json:"size"`
	UpdatedAt   time.Time `json:"updated_at"`
	Uploader    UserInfo  `json:"uploader"`
}

type ReleaseAssetUploadURL struct {
	UploadURL    string `json:"upload_url"`
	ExpiresInSec int    `json:"expires_in_sec"`
	VerifyURL    string `json:"verify_url"`
}
