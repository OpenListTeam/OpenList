package weiyun_open

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const (
	defaultAPIURL     = "https://www.weiyun.com/api/v3/mcpserver"
	defaultRootName   = "/"
	listPageSize      = 50
	maxUploadRounds   = 200
	uploadBlockSize   = 512 * 1024
	checkBlockDivisor = 128
	cacheProgressEnd  = 10
	uploadStateDone   = 2
	emptyFileSHA1     = "da39a3ee5e6b4b0d3255bfef95601890afd80709"
	emptyFileMD5      = "d41d8cd98f00b204e9800998ecf8427e"
	emptySHA1StateHex = "0123456789abcdeffedcba9876543210f0e1d2c3"
)

const (
	orderByNone = iota
	orderByName
	orderByModified
)

type toolResponse struct {
	Error string `json:"error"`
}

type listArgs struct {
	GetType uint32 `json:"get_type,omitempty"`
	Offset  uint32 `json:"offset,omitempty"`
	Limit   uint32 `json:"limit"`
	OrderBy uint32 `json:"order_by,omitempty"`
	Asc     bool   `json:"asc,omitempty"`
	DirKey  string `json:"dir_key,omitempty"`
	PdirKey string `json:"pdir_key,omitempty"`
}

type dirItem struct {
	DirKey   string    `json:"dir_key"`
	DirName  string    `json:"dir_name"`
	DirCTime jsonInt64 `json:"dir_ctime"`
	DirMTime jsonInt64 `json:"dir_mtime"`
}

type fileItem struct {
	FileID    string    `json:"file_id"`
	FileName  string    `json:"filename"`
	FileSize  jsonInt64 `json:"file_size"`
	FileCTime jsonInt64 `json:"file_ctime"`
	FileMTime jsonInt64 `json:"file_mtime"`
	PdirKey   string    `json:"pdir_key"`
}

type listResponse struct {
	toolResponse
	PdirKey        string     `json:"pdir_key"`
	TotalDirCount  jsonUint32 `json:"total_dir_count"`
	TotalFileCount jsonUint32 `json:"total_file_count"`
	DirList        []dirItem  `json:"dir_list"`
	FileList       []fileItem `json:"file_list"`
	FinishFlag     bool       `json:"finish_flag"`
}

type downloadFileItem struct {
	FileID  string `json:"file_id"`
	PdirKey string `json:"pdir_key"`
}

type downloadArgs struct {
	Items []downloadFileItem `json:"items"`
}

type downloadResultItem struct {
	FileID           string    `json:"file_id"`
	HTTPSDownloadURL string    `json:"https_download_url"`
	FileSize         jsonInt64 `json:"file_size"`
	Cookie           string    `json:"cookie"`
}

type downloadResponse struct {
	toolResponse
	Items []downloadResultItem `json:"items"`
}

type deleteFileItem struct {
	FileID  string `json:"file_id"`
	PdirKey string `json:"pdir_key"`
}

type deleteDirItem struct {
	DirKey  string `json:"dir_key"`
	PdirKey string `json:"pdir_key"`
}

type deleteArgs struct {
	FileList         []deleteFileItem `json:"file_list,omitempty"`
	DirList          []deleteDirItem  `json:"dir_list,omitempty"`
	DeleteCompletely bool             `json:"delete_completely"`
}

type deleteResponse struct {
	toolResponse
	FreedSpace    jsonInt64  `json:"freed_space"`
	FreedIndexCnt jsonUint32 `json:"freed_index_cnt"`
}

type uploadChannel struct {
	ID     jsonUint32 `json:"id"`
	Offset jsonUint64 `json:"offset"`
	Len    jsonUint32 `json:"len"`
}

type preUploadArgs struct {
	FileName     string   `json:"filename"`
	FileSize     uint64   `json:"file_size"`
	FileSHA      string   `json:"file_sha"`
	FileMD5      string   `json:"file_md5,omitempty"`
	BlockSHAList []string `json:"block_sha_list"`
	CheckSHA     string   `json:"check_sha"`
	CheckData    string   `json:"check_data,omitempty"`
	PdirKey      string   `json:"pdir_key,omitempty"`
}

type uploadChunkArgs struct {
	FileName     string          `json:"filename"`
	FileSize     uint64          `json:"file_size"`
	FileSHA      string          `json:"file_sha"`
	BlockSHAList []string        `json:"block_sha_list"`
	CheckSHA     string          `json:"check_sha"`
	UploadKey    string          `json:"upload_key"`
	ChannelList  []uploadChannel `json:"channel_list"`
	ChannelID    uint32          `json:"channel_id"`
	Ex           string          `json:"ex"`
	FileData     []byte          `json:"file_data"`
}

type uploadResponse struct {
	toolResponse
	FileID      string          `json:"file_id"`
	FileName    string          `json:"filename"`
	FileExist   bool            `json:"file_exist"`
	UploadState jsonInt64       `json:"upload_state"`
	UploadKey   string          `json:"upload_key"`
	ChannelList []uploadChannel `json:"channel_list"`
	Ex          string          `json:"ex"`
}

type File struct {
	ParentKey string
	FileID    string
	FileName  string
	FileSize  int64
	FileCTime int64
	FileMTime int64
}

func newFile(parentKey string, item fileItem) *File {
	return &File{
		ParentKey: parentKey,
		FileID:    item.FileID,
		FileName:  item.FileName,
		FileSize:  int64(item.FileSize),
		FileCTime: int64(item.FileCTime),
		FileMTime: int64(item.FileMTime),
	}
}

func (f *File) CreateTime() time.Time   { return time.UnixMilli(f.FileCTime) }
func (f *File) GetHash() utils.HashInfo { return utils.HashInfo{} }
func (f *File) GetID() string           { return f.FileID }
func (f *File) GetName() string         { return f.FileName }
func (f *File) GetPath() string         { return "" }
func (f *File) GetSize() int64          { return f.FileSize }
func (f *File) IsDir() bool             { return false }
func (f *File) ModTime() time.Time      { return time.UnixMilli(f.FileMTime) }

type Folder struct {
	Root      bool
	ParentKey string
	DirKey    string
	DirName   string
	DirCTime  int64
	DirMTime  int64
}

func newFolder(parentKey string, item dirItem) *Folder {
	return &Folder{
		ParentKey: parentKey,
		DirKey:    item.DirKey,
		DirName:   item.DirName,
		DirCTime:  int64(item.DirCTime),
		DirMTime:  int64(item.DirMTime),
	}
}

func newRootFolder(currentKey, parentKey string) *Folder {
	return &Folder{
		Root:      true,
		ParentKey: parentKey,
		DirKey:    currentKey,
		DirName:   defaultRootName,
	}
}

func (f *Folder) CreateTime() time.Time   { return time.UnixMilli(f.DirCTime) }
func (f *Folder) GetHash() utils.HashInfo { return utils.HashInfo{} }
func (f *Folder) GetID() string           { return f.DirKey }
func (f *Folder) GetName() string         { return f.DirName }
func (f *Folder) GetPath() string         { return "" }
func (f *Folder) GetSize() int64          { return 0 }
func (f *Folder) IsDir() bool             { return true }
func (f *Folder) ModTime() time.Time      { return time.UnixMilli(f.DirMTime) }

func fileFromUpload(parentKey string, resp *uploadResponse, size int64) *File {
	now := time.Now().UnixMilli()
	return &File{
		ParentKey: parentKey,
		FileID:    resp.FileID,
		FileName:  resp.FileName,
		FileSize:  size,
		FileCTime: now,
		FileMTime: now,
	}
}

var _ model.Obj = (*File)(nil)
var _ model.Obj = (*Folder)(nil)
