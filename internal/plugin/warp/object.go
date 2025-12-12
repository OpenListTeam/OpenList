package plugin_warp

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	clocks "github.com/OpenListTeam/wazero-wasip2/wasip2/clocks/v0_2"
	witgo "github.com/OpenListTeam/wazero-wasip2/wit-go"
)

type Object struct {
	// 对象的绝对路径。
	Path string
	// 对象的id信息
	ID string
	// 对象的名称。
	Name string
	// 对象的大小（字节）。
	Size int64
	// 是否为目录。
	IsFolder bool
	// 创建时间戳
	Created clocks.Duration
	// 修改时间戳
	Modified clocks.Duration
	// 缩略图链接。
	Thumbnail witgo.Option[string]
	// 文件的哈希信息列表。
	Hashes []HashInfo
	// 用于存储驱动特定的、非标准的元数据。
	Extra [][2]string
}

func (o *Object) GetName() string {
	return o.Name
}

func (o *Object) GetSize() int64 {
	return o.Size
}

func (o *Object) ModTime() time.Time {
	return o.Modified.ToTime()
}
func (o *Object) CreateTime() time.Time {
	if o.Created == 0 {
		return o.ModTime()
	}
	return o.Created.ToTime()
}

func (o *Object) IsDir() bool {
	return o.IsFolder
}

func (o *Object) GetID() string {
	return o.ID
}

func (o *Object) GetPath() string {
	return o.Path
}

func (o *Object) SetPath(path string) {
	o.Path = path
}

func (o *Object) GetHash() utils.HashInfo {
	return HashInfoConvert3(o.Hashes)
}

func (o *Object) Thumb() string {
	return o.Thumbnail.UnwrapOr("")
}

var _ model.Obj = (*Object)(nil)
var _ model.Thumb = (*Object)(nil)
var _ model.SetPath = (*Object)(nil)

func ConvertObjToObject(obj model.Obj) Object {

	thumbnail := witgo.None[string]()
	if t, ok := obj.(model.Thumb); ok {
		thumbnail = witgo.Some(t.Thumb())
	}
	return Object{
		Path:      obj.GetPath(),
		ID:        obj.GetID(),
		Name:      obj.GetName(),
		Size:      obj.GetSize(),
		IsFolder:  obj.IsDir(),
		Created:   clocks.Duration(obj.CreateTime().UnixNano()),
		Modified:  clocks.Duration(obj.ModTime().UnixNano()),
		Thumbnail: thumbnail,
		Hashes:    HashInfoConvert(obj.GetHash()),
	}
}
