package plugin_warp

import (
	"errors"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
)

type ErrCode struct {
	InvalidHandle *struct{} `wit:"case(0)"`
	// 表示功能未实现。
	NotImplemented *struct{} `wit:"case(1)"`
	// 表示功能不支持。
	NotSupport *struct{} `wit:"case(2)"`
	// 表示资源未找到。
	NotFound *struct{} `wit:"case(3)"`
	// 表示路径是文件而非目录。
	NotFolder *struct{} `wit:"case(4)"`
	// 表示路径是目录而非文件。
	NotFile *struct{} `wit:"case(5)"`
	// 包含描述信息的通用错误。
	Generic *string `wit:"case(6)"`
	// 授权失效，此时驱动处于无法自动恢复的状态
	Unauthorized *string `wit:"case(7)"`
}

func (e ErrCode) ToError() error {
	if e.InvalidHandle != nil {
		return errs.StorageNotFound
	} else if e.NotImplemented != nil {
		return errs.NotImplement
	} else if e.NotSupport != nil {
		return errs.NotSupport
	} else if e.NotFound != nil {
		return errs.ObjectNotFound
	} else if e.NotFile != nil {
		return errs.NotFile
	} else if e.NotFolder != nil {
		return errs.NotFolder
	} else if e.Unauthorized != nil {
		return errors.New(*e.Unauthorized)
	}

	return errors.New(*e.Generic)
}
