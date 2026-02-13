package handles

import (
	"fmt"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/task"
	"github.com/OpenListTeam/OpenList/v4/pkg/generic"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

type MkdirOrLinkReq struct {
	Path string `json:"path" form:"path"`
}

func FsMkdir(c *gin.Context) {
	var req MkdirOrLinkReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	reqPath, err := user.JoinPath(req.Path)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if !user.CanWrite() {
		meta, err := op.GetNearestMeta(stdpath.Dir(reqPath))
		if err != nil {
			if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
				common.ErrorResp(c, err, 500, true)
				return
			}
		}
		if !common.CanWrite(meta, reqPath) {
			common.ErrorResp(c, errs.PermissionDenied, 403)
			return
		}
	}
	if err := fs.MakeDir(c.Request.Context(), reqPath); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

type MoveCopyReq struct {
	SrcDir       string   `json:"src_dir"`
	DstDir       string   `json:"dst_dir"`
	Names        []string `json:"names"`
	Overwrite    bool     `json:"overwrite"`
	SkipExisting bool     `json:"skip_existing"`
	Merge        bool     `json:"merge"`
}

func FsMove(c *gin.Context) {
	var req MoveCopyReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Names) == 0 {
		common.ErrorStrResp(c, "Empty file names", 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanMove() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	dstDir, err := user.JoinPath(req.DstDir)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	validPaths := make([]string, 0, len(req.Names))
	for _, name := range req.Names {
		// ensure req.Names is not a relative path
		srcPath := stdpath.Join(req.SrcDir, name)
		srcPath, err = user.JoinPath(srcPath)
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
		if !req.Overwrite {
			base := stdpath.Base(srcPath)
			if base == "." || base == "/" {
				common.ErrorStrResp(c, fmt.Sprintf("invalid file name [%s]", name), 400)
				return
			}
			if res, _ := fs.Get(c.Request.Context(), stdpath.Join(dstDir, base), &fs.GetArgs{NoLog: true}); res != nil {
				if !req.SkipExisting {
					common.ErrorStrResp(c, fmt.Sprintf("file [%s] exists", name), 403)
					return
				} else {
					continue
				}
			}
		}
		validPaths = append(validPaths, srcPath)
	}

	// Create all tasks immediately without any synchronous validation
	// All validation will be done asynchronously in the background
	var addedTasks []task.TaskExtensionInfo
	for i, p := range validPaths {
		t, err := fs.Move(c.Request.Context(), p, dstDir, len(validPaths) > i+1)
		if t != nil {
			addedTasks = append(addedTasks, t)
		}
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}

	// Return immediately with task information
	if len(addedTasks) > 0 {
		common.SuccessResp(c, gin.H{
			"message": fmt.Sprintf("Successfully created %d move task(s)", len(addedTasks)),
			"tasks":   getTaskInfos(addedTasks),
		})
	} else {
		common.SuccessResp(c, gin.H{
			"message": "Move operations completed immediately",
		})
	}
}

func FsCopy(c *gin.Context) {
	var req MoveCopyReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Names) == 0 {
		common.ErrorStrResp(c, "Empty file names", 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanCopy() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	dstDir, err := user.JoinPath(req.DstDir)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	validPaths := make([]string, 0, len(req.Names))
	for _, name := range req.Names {
		// ensure req.Names is not a relative path
		srcPath := stdpath.Join(req.SrcDir, name)
		srcPath, err = user.JoinPath(srcPath)
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
		if !req.Overwrite {
			base := stdpath.Base(srcPath)
			if base == "." || base == "/" {
				common.ErrorStrResp(c, fmt.Sprintf("invalid file name [%s]", name), 400)
				return
			}
			if res, _ := fs.Get(c.Request.Context(), stdpath.Join(dstDir, base), &fs.GetArgs{NoLog: true}); res != nil {
				if !req.SkipExisting && !req.Merge {
					common.ErrorStrResp(c, fmt.Sprintf("file [%s] exists", name), 403)
					return
				} else if !req.Merge || !res.IsDir() {
					continue
				}
			}
		}
		validPaths = append(validPaths, srcPath)
	}

	// Create all tasks immediately without any synchronous validation
	// All validation will be done asynchronously in the background
	var addedTasks []task.TaskExtensionInfo
	for i, p := range validPaths {
		var t task.TaskExtensionInfo
		if req.Merge {
			t, err = fs.Merge(c.Request.Context(), p, dstDir, len(validPaths) > i+1)
		} else {
			t, err = fs.Copy(c.Request.Context(), p, dstDir, len(validPaths) > i+1)
		}
		if t != nil {
			addedTasks = append(addedTasks, t)
		}
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}

	// Return immediately with task information
	if len(addedTasks) > 0 {
		common.SuccessResp(c, gin.H{
			"message": fmt.Sprintf("Successfully created %d copy task(s)", len(addedTasks)),
			"tasks":   getTaskInfos(addedTasks),
		})
	} else {
		common.SuccessResp(c, gin.H{
			"message": "Copy operations completed immediately",
		})
	}
}

type RenameReq struct {
	Path      string `json:"path"`
	Name      string `json:"name"`
	Overwrite bool   `json:"overwrite"`
}

func FsRename(c *gin.Context) {
	var req RenameReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanRename() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	reqPath, err := user.JoinPath(req.Path)
	if err == nil {
		err = checkRelativePath(req.Name)
	}
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}
	if !req.Overwrite {
		dstPath := stdpath.Join(stdpath.Dir(reqPath), req.Name)
		if dstPath != reqPath {
			if res, _ := fs.Get(c.Request.Context(), dstPath, &fs.GetArgs{NoLog: true}); res != nil {
				common.ErrorStrResp(c, fmt.Sprintf("file [%s] exists", req.Name), 403)
				return
			}
		}
	}
	if err := fs.Rename(c.Request.Context(), reqPath, req.Name); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

func checkRelativePath(path string) error {
	if strings.ContainsAny(path, "/\\") || path == "" || path == "." || path == ".." {
		return errs.RelativePath
	}
	return nil
}

type RemoveReq struct {
	Dir   string   `json:"dir"`
	Names []string `json:"names"`
}

func FsRemove(c *gin.Context) {
	var req RemoveReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if len(req.Names) == 0 {
		common.ErrorStrResp(c, "Empty file names", 400)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanRemove() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	for i, name := range req.Names {
		if strings.TrimSpace(utils.FixAndCleanPath(name)) == "/" {
			log.Warnf("FsRemove: invalid item skipped: %s (parent directory: %s)\n", name, req.Dir)
			req.Names[i] = ""
			continue
		}
		// ensure req.Names is not a relative path
		var err error
		req.Names[i], err = user.JoinPath(stdpath.Join(req.Dir, name))
		if err != nil {
			common.ErrorResp(c, err, 403)
			return
		}
	}
	for _, path := range req.Names {
		if path == "" {
			continue
		}
		err := fs.Remove(c.Request.Context(), path)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
	}
	//fs.ClearCache(req.Dir)
	common.SuccessResp(c)
}

type RemoveEmptyDirectoryReq struct {
	SrcDir string `json:"src_dir"`
}

func FsRemoveEmptyDirectory(c *gin.Context) {
	var req RemoveEmptyDirectoryReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if !user.CanRemove() {
		common.ErrorResp(c, errs.PermissionDenied, 403)
		return
	}
	srcDir, err := user.JoinPath(req.SrcDir)
	if err != nil {
		common.ErrorResp(c, err, 403)
		return
	}

	meta, err := op.GetNearestMeta(srcDir)
	if err != nil {
		if !errors.Is(errors.Cause(err), errs.MetaNotFound) {
			common.ErrorResp(c, err, 500, true)
			return
		}
	}
	common.GinWithValue(c, conf.MetaKey, meta)

	rootFiles, err := fs.List(c.Request.Context(), srcDir, &fs.ListArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	// record the file path
	filePathMap := make(map[model.Obj]string)
	// record the parent file
	fileParentMap := make(map[model.Obj]model.Obj)
	// removing files
	removingFiles := generic.NewQueue[model.Obj]()
	// removed files
	removedFiles := make(map[string]bool)
	for _, file := range rootFiles {
		if !file.IsDir() {
			continue
		}
		removingFiles.Push(file)
		filePathMap[file] = srcDir
	}

	for !removingFiles.IsEmpty() {

		removingFile := removingFiles.Pop()
		removingFilePath := fmt.Sprintf("%s/%s", filePathMap[removingFile], removingFile.GetName())

		if removedFiles[removingFilePath] {
			continue
		}

		subFiles, err := fs.List(c.Request.Context(), removingFilePath, &fs.ListArgs{Refresh: true})
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}

		if len(subFiles) == 0 {
			// remove empty directory
			err = fs.Remove(c.Request.Context(), removingFilePath)
			removedFiles[removingFilePath] = true
			if err != nil {
				common.ErrorResp(c, err, 500)
				return
			}
			// recheck parent folder
			parentFile, exist := fileParentMap[removingFile]
			if exist {
				removingFiles.Push(parentFile)
			}

		} else {
			// recursive remove
			for _, subFile := range subFiles {
				if !subFile.IsDir() {
					continue
				}
				removingFiles.Push(subFile)
				filePathMap[subFile] = removingFilePath
				fileParentMap[subFile] = removingFile
			}
		}

	}

	common.SuccessResp(c)
}

// Link return real link, just for proxy program, it may contain cookie, so just allowed for admin
func Link(c *gin.Context) {
	var req MkdirOrLinkReq
	if err := c.ShouldBind(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	//user := c.Request.Context().Value(conf.UserKey).(*model.User)
	//rawPath := stdpath.Join(user.BasePath, req.Path)
	// why need not join base_path? because it's always the full path
	rawPath := req.Path
	storage, err := fs.GetStorage(rawPath, &fs.GetStoragesArgs{})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if storage.Config().NoLinkURL {
		common.SuccessResp(c, model.Link{
			URL: fmt.Sprintf("%s/p%s?d&sign=%s",
				common.GetApiUrl(c),
				utils.EncodePath(rawPath, true),
				sign.Sign(rawPath)),
		})
		return
	}
	link, _, err := fs.Link(c.Request.Context(), rawPath, model.LinkArgs{IP: c.ClientIP(), Header: c.Request.Header, Redirect: true})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	defer link.Close()
	common.SuccessResp(c, link)
}
