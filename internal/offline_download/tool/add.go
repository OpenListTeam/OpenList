package tool

import (
	"context"
	"net/url"
	stdpath "path"
	"path/filepath"

	_115 "github.com/OpenListTeam/OpenList/v4/drivers/115"
	_115_open "github.com/OpenListTeam/OpenList/v4/drivers/115_open"
	_123 "github.com/OpenListTeam/OpenList/v4/drivers/123"
	_123_open "github.com/OpenListTeam/OpenList/v4/drivers/123_open"
	"github.com/OpenListTeam/OpenList/v4/drivers/pikpak"
	"github.com/OpenListTeam/OpenList/v4/drivers/thunder"
	"github.com/OpenListTeam/OpenList/v4/drivers/thunder_browser"
	"github.com/OpenListTeam/OpenList/v4/drivers/thunderx"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/google/uuid"
	"github.com/pkg/errors"
)

type DeletePolicy string

const (
	DeleteOnUploadSucceed DeletePolicy = "delete_on_upload_succeed"
	DeleteOnUploadFailed  DeletePolicy = "delete_on_upload_failed"
	DeleteNever           DeletePolicy = "delete_never"
	DeleteAlways          DeletePolicy = "delete_always"
	UploadDownloadStream  DeletePolicy = "upload_download_stream"
)

type AddURLArgs struct {
	URL          string
	DstDirPath   string
	Tool         string
	DeletePolicy DeletePolicy
}

func AddURL(ctx context.Context, args *AddURLArgs) (task.TaskExtensionInfo, error) {
	// check storage
	storage, dstDirActualPath, err := op.GetStorageAndActualPath(args.DstDirPath)
	if err != nil {
		return nil, errors.WithMessage(err, "failed get storage")
	}
	// check is it could upload
	if storage.Config().NoUpload {
		return nil, errors.WithStack(errs.UploadNotSupported)
	}
	// check path is valid
	obj, err := op.Get(ctx, storage, dstDirActualPath)
	if err != nil {
		if !errs.IsObjectNotFound(err) {
			return nil, errors.WithMessage(err, "failed get object")
		}
	} else {
		if !obj.IsDir() {
			// can't add to a file
			return nil, errors.WithStack(errs.NotFolder)
		}
	}
	// try putting url
	if args.Tool == "SimpleHttp" {
		err = tryPutUrl(ctx, args.DstDirPath, args.URL)
		if err == nil || !errors.Is(err, errs.NotImplement) {
			return nil, err
		}
	}

	// get tool
	tool, err := Tools.Get(args.Tool)
	if err != nil {
		return nil, errors.Wrapf(err, "failed get offline download tool")
	}
	// check tool is ready
	if !tool.IsReady() {
		// try to init tool
		if _, err := tool.Init(); err != nil {
			return nil, errors.Wrapf(err, "failed init offline download tool %s", args.Tool)
		}
	}

	uid := uuid.NewString()
	tempDir := filepath.Join(conf.Conf.TempDir, args.Tool, uid)
	deletePolicy := args.DeletePolicy

	// 如果当前 storage 是对应网盘，则直接下载到目标路径，无需转存
	switch args.Tool {
	case "115 Cloud":
		if _, ok := storage.(*_115.Pan115); ok {
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.Pan115TempDir), uid)
		}
	case "115 Open":
		if _, ok := storage.(*_115_open.Open115); ok {
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.Pan115OpenTempDir), uid)
		}
	case "123 Open":
		if _, ok := storage.(*_123_open.Open123); ok && dstDirActualPath != "/" {
			// directly offline downloading to the root path is not allowed via 123 open platform
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.Pan123OpenTempDir), uid)
		}
	case "123Pan":
		if _, ok := storage.(*_123.Pan123); ok {
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.Pan123TempDir), uid)
		}
	case "PikPak":
		if _, ok := storage.(*pikpak.PikPak); ok {
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.PikPakTempDir), uid)
		}
	case "Thunder":
		if _, ok := storage.(*thunder.Thunder); ok {
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.ThunderTempDir), uid)
		}
	case "ThunderBrowser":
		switch storage.(type) {
		case *thunder_browser.ThunderBrowser, *thunder_browser.ThunderBrowserExpert:
			tempDir = args.DstDirPath
		default:
			tempDir = filepath.Join(setting.GetStr(conf.ThunderBrowserTempDir), uid)
		}
	case "ThunderX":
		if _, ok := storage.(*thunderx.ThunderX); ok {
			tempDir = args.DstDirPath
		} else {
			tempDir = filepath.Join(setting.GetStr(conf.ThunderXTempDir), uid)
		}
	}

	taskCreator, _ := ctx.Value(conf.UserKey).(*model.User) // taskCreator is nil when convert failed
	t := &DownloadTask{
		TaskExtension: task.TaskExtension{
			Creator: taskCreator,
			ApiUrl:  common.GetApiUrl(ctx),
		},
		Url:          args.URL,
		DstDirPath:   args.DstDirPath,
		TempDir:      tempDir,
		DeletePolicy: deletePolicy,
		Toolname:     args.Tool,
		tool:         tool,
	}
	DownloadTaskManager.Add(t)
	return t, nil
}

func tryPutUrl(ctx context.Context, path, urlStr string) error {
	var dstName string
	u, err := url.Parse(urlStr)
	if err == nil {
		dstName = stdpath.Base(u.Path)
	} else {
		dstName = "UnnamedURL"
	}
	return fs.PutURL(ctx, path, dstName, urlStr)
}
