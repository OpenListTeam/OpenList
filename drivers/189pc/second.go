package _189pc

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/google/uuid"
)

// 家庭云中转文件夹名称，两个账号共用同一个目录
const secondTransferFolderName = "FamilyTransferFolder"

// root 返回真正持有登录会话的驱动实例。
// 当通过 ref 复用其他存储的会话时，第二账号的状态也统一由被引用实例持有，
// 避免同一个账号的多个挂载点重复登录。
func (y *Cloud189PC) root() *Cloud189PC {
	if y.owner != nil {
		return y.owner.root()
	}
	if y.ref != nil {
		return y.ref.root()
	}
	return y
}

// SecondAccountEnabled 是否启用跨账号家庭转移
func (y *Cloud189PC) SecondAccountEnabled() bool {
	return y.Addition.SecondAccountTransfer &&
		(y.Addition.SecondUsername != "" || y.Addition.SecondRefreshToken != "") &&
		(y.Addition.SecondPassword != "" || y.Addition.SecondRefreshToken != "")
}

// saveAddition 持久化当前驱动的配置。
// 若当前实例是第二账号会话，则把它的凭证回写到主账号配置中。
func (y *Cloud189PC) saveAddition() {
	root := y.root()
	root.saveMu.Lock()
	defer root.saveMu.Unlock()
	if root != y {
		// 第二账号的会话凭据需要保存在当前账号的配置里
		root.Addition.SecondAccessToken = y.Addition.AccessToken
		root.Addition.SecondRefreshToken = y.Addition.RefreshToken
		// 第二账号登录出错时同步状态，便于在前端看到原因
		if y.Status != "" {
			root.Status = y.Status
		}
	}
	op.MustSaveDriverStorage(root)
}

// initSecondAccount 初始化第二账号（上传号）会话
func (y *Cloud189PC) initSecondAccount(ctx context.Context) error {
	root := y.root()
	if !root.SecondAccountEnabled() {
		return nil
	}
	if err := root.ensureSecondClient(ctx); err != nil {
		return fmt.Errorf("failed to init the second account: %w", err)
	}
	utils.Log.Infof("189pc: cross-account family transfer enabled, uploading with the second account [%s]", root.Addition.SecondUsername)
	return nil
}

// ensureSecondClient 确保第二账号会话可用
func (y *Cloud189PC) ensureSecondClient(ctx context.Context) error {
	y.secondMu.RLock()
	ready := y.second != nil
	y.secondMu.RUnlock()
	if ready {
		return nil
	}

	y.secondLoginMu.Lock()
	defer y.secondLoginMu.Unlock()
	y.secondMu.RLock()
	ready = y.second != nil
	y.secondMu.RUnlock()
	if ready {
		return nil
	}

	// newSecondClient 内部会在成功后写入 y.second
	_, err := y.newSecondClient(ctx)
	return err
}

// getSecondClient 获取已初始化的第二账号会话
func (y *Cloud189PC) getSecondClient() (*Cloud189PC, error) {
	if err := y.ensureSecondClient(context.Background()); err != nil {
		return nil, err
	}
	y.secondMu.RLock()
	defer y.secondMu.RUnlock()
	if y.second == nil {
		return nil, errors.New("the second account is not initialized")
	}
	return y.second, nil
}

// newSecondClient 使用第二账号的凭据创建一个独立的驱动会话
func (y *Cloud189PC) newSecondClient(ctx context.Context) (*Cloud189PC, error) {
	c := &Cloud189PC{
		// owner 用于把第二账号的凭证与中转目录状态回写到当前账号
		owner:        y,
		uploadThread: y.uploadThread,
	}
	// 保持同一个存储记录，便于持久化配置
	c.Storage = y.Storage
	c.Addition = y.Addition
	// 使用第二账号登录
	c.Addition.Username = y.Addition.SecondUsername
	c.Addition.Password = y.Addition.SecondPassword
	c.Addition.AccessToken = y.Addition.SecondAccessToken
	c.Addition.RefreshToken = y.Addition.SecondRefreshToken
	// 第二账号只需要家庭云，因此固定为 family 类型，以复用家庭云接口
	c.Addition.Type = "family"
	// 第二账号在后台登录，二维码登录没有意义，固定使用账号密码登录
	c.Addition.LoginType = "password"
	// 第二账号不再嵌套自己的第二账号，也不参与个人云 torrent 生成
	c.Addition.SecondAccountTransfer = false
	c.Addition.GenerateTorrent = false
	c.storageConfig = c.Addition.toStorageConfig()

	c.client = base.NewRestyClient().SetHeaders(map[string]string{
		"Accept":  "application/json;charset=UTF-8",
		"Referer": WEB_URL,
	})

	// 先尝试用Token刷新，之后尝试登陆
	switch {
	case c.Addition.AccessToken != "":
		c.tokenInfo = &AppSessionResp{AccessToken: c.Addition.AccessToken, RefreshToken: c.Addition.RefreshToken}
		if err := c.refreshSession(); err != nil {
			return nil, err
		}
	case c.Addition.RefreshToken != "":
		c.tokenInfo = &AppSessionResp{RefreshToken: c.Addition.RefreshToken}
		if err := c.refreshToken(); err != nil {
			return nil, err
		}
	default:
		if err := c.login(); err != nil {
			return nil, err
		}
	}

	// 家庭云ID：优先使用配置值，默认两个账号访问同一个家庭
	if c.FamilyID == "" {
		familyID, err := c.getFamilyID()
		if err != nil {
			return nil, fmt.Errorf("failed to get the family id of the second account: %w", err)
		}
		c.FamilyID = familyID
	}

	// 使用与当前账号相同的家庭云中转目录
	if err := c.initSecondTransferFolder(); err != nil {
		return nil, err
	}
	y.setSecondClient(c)

	c.cron = cron.NewCron(time.Minute * 5)
	c.cron.Do(c.keepAlive)
	return c, nil
}

// toStorageConfig 计算驱动配置（仅保留上传行为相关的差异）
func (a Addition) toStorageConfig() driver.Config {
	cfg := config
	if a.Type == "family" {
		// 兼容旧上传接口
		if a.RapidUpload || a.UploadMethod == "old" {
			cfg.NoOverwriteUpload = true
		}
	} else if a.FamilyTransfer || a.SecondAccountTransfer {
		// 家庭云转存不支持覆盖上传
		cfg.NoOverwriteUpload = true
	}
	return cfg
}

// setSecondClient 保存第二账号会话
func (y *Cloud189PC) setSecondClient(c *Cloud189PC) {
	y.secondMu.Lock()
	defer y.secondMu.Unlock()
	y.second = c
}

// initSecondTransferFolder 绑定家庭云中转文件夹，与当前账号使用同一个目录
func (y *Cloud189PC) initSecondTransferFolder() error {
	root := y.root()
	// 复用当前账号已创建/已定位的中转文件夹
	if root.familyTransferFolder != nil && root.familyTransferFolder.GetID() != "" {
		y.familyTransferFolder = &Cloud189Folder{
			ID:   String(root.familyTransferFolder.GetID()),
			Name: root.familyTransferFolder.GetName(),
		}
		return nil
	}
	// 查找家庭云中已存在的中转文件夹
	folder, err := y.findFolderByName(secondTransferFolderName, "")
	if err == nil {
		y.familyTransferFolder = folder
		root.familyTransferFolder = folder
		return nil
	}
	if !errs.IsObjectNotFound(err) {
		return err
	}
	// 不存在则创建
	if err := y.createFamilyTransferFolder(); err != nil {
		return err
	}
	root.familyTransferFolder = y.familyTransferFolder
	return nil
}

// getSecondClientFamilyID 返回第二账号使用的家庭云ID
func (y *Cloud189PC) getSecondClientFamilyID() string {
	y.secondMu.RLock()
	defer y.secondMu.RUnlock()
	if y.second == nil {
		return ""
	}
	return y.second.FamilyID
}

// findFolderByName 在家庭云指定目录下按名称查找文件夹
func (y *Cloud189PC) findFolderByName(name string, folderId string) (*Cloud189Folder, error) {
	for pageNum := 1; ; pageNum++ {
		resp, err := y.getFilesWithPage(context.Background(), folderId, true, pageNum, 100, "filename", "asc")
		if err != nil {
			return nil, err
		}
		// 获取完毕跳出
		if resp.FileListAO.Count == 0 {
			return nil, errs.ObjectNotFound
		}
		for i := range resp.FileListAO.FolderList {
			if resp.FileListAO.FolderList[i].Name == name {
				return &resp.FileListAO.FolderList[i], nil
			}
		}
	}
}

// uploadToFamily 上传文件到家庭云。
// 家庭云不支持 stream 上传，因此优先使用 rapid（fastUpload），失败后回退旧版上传。
func (y *Cloud189PC) uploadToFamily(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	if obj, err := y.FastUpload(ctx, dstDir, file, up, true, false); err == nil {
		return obj, nil
	} else {
		utils.Log.Warnf("189pc: fast upload to the family cloud failed, fallback to the old upload interface: %v", err)
	}
	return y.OldUpload(ctx, dstDir, file, up, true, false)
}

// PutBySecondAccount 使用第二账号把文件上传到家庭云，再转存到当前账号的个人云
func (y *Cloud189PC) PutBySecondAccount(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	root := y.root()
	second, err := root.getSecondClient()
	if err != nil {
		return nil, err
	}
	if second.FamilyID == "" {
		return nil, errors.New("the family id of the second account is not available, please fill in family_id manually")
	}
	if second.familyTransferFolder == nil {
		return nil, errors.New("the family transfer folder of the second account is not initialized")
	}

	// 使用临时文件名上传，避免家庭云中转目录中的同名冲突
	srcName := stream.GetName()
	transferStream := &WrapFileStreamer{
		FileStreamer: stream,
		Name:         fmt.Sprintf("0%s.transfer", uuid.NewString()),
	}

	// 使用第二账号上传到家庭云中转目录
	familyObj, err := second.uploadToFamily(ctx, second.familyTransferFolder, transferStream, up)
	if err != nil {
		return nil, err
	}

	// 无论后续是否成功，家庭云中的中转文件都要清理
	defer func() {
		go second.Delete(context.Background(), second.FamilyID, familyObj)
		if root.cleanFamilyTransferFile != nil {
			// 批量任务有概率删不掉
			go root.cleanFamilyTransferFile()
		}
	}()

	// 使用当前账号把家庭云文件转存到个人云
	if err = root.SaveFamilyFileToPersonCloud(ctx, second.FamilyID, familyObj, dstDir, true); err != nil {
		return nil, err
	}

	// 查找转存后的文件
	file, err := root.findFileByName(ctx, familyObj.GetName(), dstDir.GetID(), false)
	if err != nil {
		if err == errs.ObjectNotFound {
			return nil, fmt.Errorf("unknown error: no transfer file obtained %s", familyObj.GetName())
		}
		return nil, err
	}

	// 重命名转存后的文件
	newObj, err := root.Rename(ctx, file, srcName)
	if err != nil {
		// 重命名失败删除文件
		_ = root.Delete(ctx, "", file)
	}
	return newObj, err
}
