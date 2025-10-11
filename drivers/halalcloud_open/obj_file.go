package halalcloudopen

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
)

type ObjFile struct {
	sdkFile    *sdkUserFile.File
	fileSize   int64
	modTime    time.Time
	createTime time.Time
}

func NewObjFile(f *sdkUserFile.File) model.Obj {
	ofile := &ObjFile{sdkFile: f}
	ofile.fileSize = f.Size
	modTimeTs := f.UpdateTs
	ofile.modTime = time.UnixMilli(modTimeTs)
	createTimeTs := f.CreateTs
	ofile.createTime = time.UnixMilli(createTimeTs)
	return ofile
}

func (f *ObjFile) GetSize() int64 {
	return f.fileSize
}

func (f *ObjFile) GetName() string {
	return f.sdkFile.Name
}

func (f *ObjFile) ModTime() time.Time {
	return f.modTime
}

func (f *ObjFile) IsDir() bool {
	return f.sdkFile.Dir
}

func (f *ObjFile) GetHash() utils.HashInfo {
	return utils.HashInfo{
		// TODO: support more hash types
	}
}

func (f *ObjFile) GetID() string {
	return f.sdkFile.Identity
}

func (f *ObjFile) GetPath() string {
	return f.sdkFile.Path
}

func (f *ObjFile) CreateTime() time.Time {
	return f.createTime
}
