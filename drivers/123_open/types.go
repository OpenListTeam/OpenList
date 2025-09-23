package _123_open

import (
	"io"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type ApiInfo struct {
	url   string
	qps   int
	token chan struct{}
}

func (a *ApiInfo) Require() {
	if a.qps > 0 {
		a.token <- struct{}{}
	}
}
func (a *ApiInfo) Release() {
	if a.qps > 0 {
		time.AfterFunc(time.Second, func() {
			<-a.token
		})
	}
}
func (a *ApiInfo) SetQPS(qps int) {
	a.qps = qps
	a.token = make(chan struct{}, qps)
}
func (a *ApiInfo) NowLen() int {
	return len(a.token)
}
func InitApiInfo(url string, qps int) *ApiInfo {
	return &ApiInfo{
		url:   url,
		qps:   qps,
		token: make(chan struct{}, qps),
	}
}

type File struct {
	FileName     string `json:"filename"`
	Size         int64  `json:"size"`
	CreateAt     string `json:"createAt"`
	UpdateAt     string `json:"updateAt"`
	FileId       int64  `json:"fileId"`
	Type         int    `json:"type"`
	Etag         string `json:"etag"`
	S3KeyFlag    string `json:"s3KeyFlag"`
	ParentFileId int    `json:"parentFileId"`
	Category     int    `json:"category"`
	Status       int    `json:"status"`
	Trashed      int    `json:"trashed"`
}

func (f File) GetHash() utils.HashInfo {
	return utils.NewHashInfo(utils.MD5, f.Etag)
}

func (f File) GetPath() string {
	return ""
}

func (f File) GetSize() int64 {
	return f.Size
}

func (f File) GetName() string {
	return f.FileName
}

func (f File) CreateTime() time.Time {
	// 返回的时间没有时区信息，默认 UTC+8
	loc := time.FixedZone("UTC+8", 8*60*60)
	parsedTime, err := time.ParseInLocation("2006-01-02 15:04:05", f.CreateAt, loc)
	if err != nil {
		return time.Now()
	}
	return parsedTime
}

func (f File) ModTime() time.Time {
	// 返回的时间没有时区信息，默认 UTC+8
	loc := time.FixedZone("UTC+8", 8*60*60)
	parsedTime, err := time.ParseInLocation("2006-01-02 15:04:05", f.UpdateAt, loc)
	if err != nil {
		return time.Now()
	}
	return parsedTime
}

func (f File) IsDir() bool {
	return f.Type == 1
}

func (f File) GetID() string {
	return strconv.FormatInt(f.FileId, 10)
}

var _ model.Obj = (*File)(nil)

type BaseResp struct {
	Code     int    `json:"code"`
	Message  string `json:"message"`
	XTraceID string `json:"x-traceID"`
}

type AccessTokenResp struct {
	BaseResp
	Data struct {
		AccessToken string `json:"accessToken"`
		ExpiredAt   string `json:"expiredAt"`
	} `json:"data"`
}

type RefreshTokenResp struct {
	AccessToken  string `json:"access_token"`
	ExpiresIn    int    `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	Scope        string `json:"scope"`
	TokenType    string `json:"token_type"`
}

type UserInfoResp struct {
	BaseResp
	Data struct {
		UID uint64 `json:"uid"`
		// Username       string `json:"username"`
		// DisplayName    string `json:"displayName"`
		// HeadImage      string `json:"headImage"`
		// Passport       string `json:"passport"`
		// Mail           string `json:"mail"`
		SpaceUsed      uint64 `json:"spaceUsed"`
		SpacePermanent uint64 `json:"spacePermanent"`
		SpaceTemp      uint64 `json:"spaceTemp"`
		// SpaceTempExpr  int64  `json:"spaceTempExpr"`
		// Vip            bool   `json:"vip"`
		// DirectTraffic  int64  `json:"directTraffic"`
		// IsHideUID      bool   `json:"isHideUID"`
	} `json:"data"`
}

type FileListResp struct {
	BaseResp
	Data struct {
		LastFileId int64  `json:"lastFileId"`
		FileList   []File `json:"fileList"`
	} `json:"data"`
}

type DownloadInfoResp struct {
	BaseResp
	Data struct {
		DownloadUrl string `json:"downloadUrl"`
	} `json:"data"`
}

type DirectLinkResp struct {
	BaseResp
	Data struct {
		URL string `json:"url"`
	} `json:"data"`
}

// 上传完毕V2返回
type UploadCompleteResp struct {
	BaseResp
	Data struct {
		Completed bool  `json:"completed"`
		FileID    int64 `json:"fileID"`
	} `json:"data"`
}

// UploadCreateReq 预上传请求
// parentFileID	number	必填	父目录id，上传到根目录时填写 0
// filename	string	必填	文件名要小于255个字符且不能包含以下任何字符："\/:*?|><。（注：不能重名）
// containDir 为 true 时，传入路径+文件名，例如：/你好/123/测试文件.mp4
// etag	string	必填	文件md5
// size	number	必填	文件大小，单位为 byte 字节
// duplicate	number	非必填	当有相同文件名时，文件处理策略（1保留两者，新文件名将自动添加后缀，2覆盖原文件）
// containDir	bool	非必填	上传文件是否包含路径，默认false
type UploadCreateReq struct {
	ParentFileID uint64 `json:"parentFileID"`
	FileName     string `json:"filename"`
	Etag         string `json:"etag"`
	Size         int64  `json:"size"`
	Duplicate    int    `json:"duplicate"`
	ContainDir   bool   `json:"containDir"`
}

type UploadCreateResp struct {
	BaseResp
	Data UploadCreateData `json:"data"`
}

// UploadCreateData 预上传响应
// fileID	number	非必填	文件ID。当123云盘已有该文件,则会发生秒传。此时会将文件ID字段返回。唯一
// preuploadID	string	必填	预上传ID(如果 reuse 为 true 时,该字段不存在)
// reuse	boolean	必填	是否秒传，返回true时表示文件已上传成功
// sliceSize	number	必填	分片大小，必须按此大小生成文件分片再上传
// servers	array	必填	上传地址
type UploadCreateData struct {
	FileID      int64    `json:"fileID"`
	PreuploadID string   `json:"preuploadID"`
	Reuse       bool     `json:"reuse"`
	SliceSize   int64    `json:"sliceSize"`
	Servers     []string `json:"servers"`
}

// UploadSliceReq 分片上传请求
// preuploadID	string	必填	预上传ID
// sliceNo	number	必填	分片序号，从1开始自增
// sliceMD5	string	必填	当前分片md5
// slice	file	必填	分片二进制流
type UploadSliceReq struct {
	Name        string    `json:"name"`
	PreuploadID string    `json:"preuploadID"`
	SliceNo     int       `json:"sliceNo"`
	SliceMD5    string    `json:"sliceMD5"`
	Slice       io.Reader `json:"slice"`
	Server      string    `json:"server"`
}

type SliceUpCompleteResp struct {
	SingleUploadResp
}

type GetUploadServerResp struct {
	BaseResp
	Data []string `json:"data"`
}

// SingleUploadReq 单文件上传请求
// parentFileID	number	必填	父目录id，上传到根目录时填写 0
// filename	string	必填	文件名要小于255个字符且不能包含以下任何字符："\/:*?|><。（注：不能重名）
//
//	containDir 为 true 时，传入路径+文件名，例如：/你好/123/测试文件.mp4
//
// etag	string	必填	文件md5
// size	number	必填	文件大小，单位为 byte 字节
// file	file	必填	文件二进制流
// duplicate	number	非必填	当有相同文件名时，文件处理策略（1保留两者，新文件名将自动添加后缀，2覆盖原文件）
// containDir	bool	非必填	上传文件是否包含路径，默认false
type SingleUploadReq struct {
	ParentFileID int64     `json:"parentFileID"`
	FileName     string    `json:"filename"`
	Etag         string    `json:"etag"`
	Size         int64     `json:"size"`
	File         io.Reader `json:"file"`
	Duplicate    int       `json:"duplicate"`
	ContainDir   bool      `json:"containDir"`
}

// SingleUploadResp 单文件上传响应
type SingleUploadResp struct {
	BaseResp
	Data SingleUploadData `json:"data"`
}

type SingleUploadData struct {
	FileID    int64 `json:"fileID"`
	Completed bool  `json:"completed"`
}
