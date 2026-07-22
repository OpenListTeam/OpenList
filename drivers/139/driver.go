package _139

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"path"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/avast/retry-go"
	log "github.com/sirupsen/logrus"
)

type Yun139 struct {
	model.Storage
	Addition
	cron              *cron.Cron
	Account           string
	ref               *Yun139
	PersonalCloudHost string
	FamilyCloudHost   string
	GroupCloudHost    string
	ProviderRoot      string
}

func (d *Yun139) Config() driver.Config {
	return config
}

func (d *Yun139) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Yun139) Init(ctx context.Context) error {
	if d.ref == nil {
		if len(d.Authorization) == 0 && !d.isShare() {
			if d.Username != "" && d.Password != "" {
				log.Infof("139yun: authorization is empty, trying to login with password.")
				newAuth, err := d.loginWithPassword()
				log.Debugf("newAuth: Ok: %s", newAuth)
				if err != nil {
					return fmt.Errorf("login with password failed: %w", err)
				}
			} else {
				return fmt.Errorf("authorization is empty and username/password is not provided")
			}
		}
		if d.Authorization != "" {
			err := d.refreshToken()
			if err != nil {
				return err
			}

			// Query Route Policy
			var resp QueryRoutePolicyResp
			_, err = d.requestRoute(base.Json{
				"userInfo": base.Json{
					"userType":    1,
					"accountType": 1,
					"accountName": d.Account,
				},
				"modAddrType": 1,
			}, &resp)
			if err != nil {
				return err
			}
			for _, policyItem := range resp.Data.RoutePolicyList {
				switch policyItem.ModName {
				case "personal":
					d.PersonalCloudHost = policyItem.HttpsUrl
				case "group":
					d.GroupCloudHost = policyItem.HttpsUrl
				case "family":
					d.FamilyCloudHost = policyItem.HttpsUrl
				}
			}
			if len(d.PersonalCloudHost) == 0 {
				return fmt.Errorf("PersonalCloudHost is empty")
			}
			if d.isGroup() || d.isFamily() {
				if len(d.GroupCloudHost) == 0 {
					return fmt.Errorf("GroupCloudHost is empty")
				}
				if len(d.FamilyCloudHost) == 0 {
					return fmt.Errorf("FamilyCloudHost is empty")
				}
			}

			d.cron = cron.NewCron(time.Hour * 12)
			d.cron.Do(func() {
				err := d.refreshToken()
				if err != nil {
					log.Errorf("%+v", err)
				}
			})
		}
	}
	switch d.Addition.Type {
	case MetaPersonalNew:
		if len(d.Addition.RootFolderID) == 0 {
			d.RootFolderID = "/"
		}
	case MetaPersonal:
		if len(d.Addition.RootFolderID) == 0 {
			d.RootFolderID = "root"
		}
	case MetaGroup:
		if len(d.Addition.RootFolderID) == 0 {
			d.RootFolderID = d.CloudID
		}
		_, err := d.groupGetFiles(d.RootFolderID)
		if err != nil {
			return err
		}
	case MetaShare:
		if len(d.Addition.RootFolderID) == 0 {
			d.RootFolderID = "root"
		}
		if len(d.shareEntries()) == 0 {
			return fmt.Errorf("link_id is empty")
		}
	case MetaFamily:
		// Attempt to obtain data.path as the root via a query and persist it.
		root, err := d.getFamilyRootPath(d.CloudID)
		if err != nil || root == "" {
			return fmt.Errorf("failed to get family root path: %w", err)
		}
		d.ProviderRoot = root
		if len(d.Addition.RootFolderID) == 0 {
			d.RootFolderID = root
			op.MustSaveDriverStorage(d)
		}
		_, err = d.familyGetFiles(d.RootFolderID)
		if err != nil {
			return err
		}
	default:
		return errs.NotImplement
	}
	return nil
}

func (d *Yun139) InitReference(storage driver.Driver) error {
	refStorage, ok := storage.(*Yun139)
	if ok {
		d.ref = refStorage
		return nil
	}
	return errs.NotSupport
}

func (d *Yun139) Get(ctx context.Context, path string) (model.Obj, error) {
	if !d.isShare() {
		return nil, errs.NotImplement
	}
	if path == "/" {
		return &model.Object{ID: "root", Name: "root", IsFolder: true, Path: "/"}, nil
	}
	if obj, err := d.shareGetObj(path); err == nil {
		return obj, nil
	}
	return nil, errs.ObjectNotFound
}

func (d *Yun139) Drop(ctx context.Context) error {
	if d.cron != nil {
		d.cron.Stop()
	}
	d.ref = nil
	return nil
}

func (d *Yun139) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	switch d.Addition.Type {
	case MetaPersonalNew:
		return d.personalGetFiles(dir.GetID())
	case MetaPersonal:
		return d.getFiles(dir.GetID())
	case MetaFamily:
		return d.familyGetFiles(dir.GetID())
	case MetaGroup:
		return d.groupGetFiles(dir.GetID())
	case MetaShare:
		if dir.GetID() == "root" {
			return d.shareGetMergedFiles(d.shareRootEntries())
		}
		if refs, ok := decodeShareRefs(dir.GetID()); ok {
			return d.shareGetMergedFiles(refs)
		}
		return d.shareGetFilesWithRef(shareRef{LinkID: d.LinkID, NodeID: dir.GetID()}, dir.GetID())
	default:
		return nil, errs.NotImplement
	}
}

func (d *Yun139) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	if file.IsDir() {
		return nil, errs.NotFile
	}
	var url string
	var err error
	switch d.Addition.Type {
	case MetaPersonalNew:
		url, err = d.personalGetLink(file.GetID())
	case MetaPersonal:
		url, err = d.getLink(file.GetID())
	case MetaFamily:
		url, err = d.familyGetLink(file.GetID(), file.GetPath())
	case MetaGroup:
		url, err = d.groupGetLink(file.GetID(), file.GetPath())
	case MetaShare:
		if refs, ok := decodeShareRefs(file.GetID()); ok && len(refs) > 0 {
			return d.shareGetLinkWithRef(refs[0], refs[0].NodeID, args.Type)
		}
		fallbackRef := shareRef{LinkID: d.LinkID, NodeID: "root"}
		if entries := d.shareEntries(); len(entries) > 0 {
			fallbackRef.LinkID = entries[0].LinkID
			fallbackRef.Password = entries[0].Password
		}
		return d.shareGetLinkWithRef(fallbackRef, file.GetID(), args.Type)
	default:
		return nil, errs.NotImplement
	}
	if err != nil {
		return nil, err
	}
	return &model.Link{URL: url}, nil
}

func (d *Yun139) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	if d.isShare() {
		return errs.NotImplement
	}
	var err error
	switch d.Addition.Type {
	case MetaPersonalNew:
		data := base.Json{
			"parentFileId":   parentDir.GetID(),
			"name":           dirName,
			"description":    "",
			"type":           "folder",
			"fileRenameMode": "force_rename",
		}
		pathname := "/file/create"
		_, err = d.personalPost(pathname, data, nil)
	case MetaPersonal:
		data := base.Json{
			"createCatalogExtReq": base.Json{
				"parentCatalogID": parentDir.GetID(),
				"newCatalogName":  dirName,
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			},
		}
		pathname := "/orchestration/personalCloud/catalog/v1.0/createCatalogExt"
		_, err = d.post(pathname, data, nil)
	case MetaFamily:
		data := base.Json{
			"cloudID": d.CloudID,
			"commonAccountInfo": base.Json{
				"account":     d.getAccount(),
				"accountType": 1,
			},
			"docLibName": dirName,
			"path":       d.dirPath(parentDir),
		}
		pathname := "/orchestration/familyCloud-rebuild/cloudCatalog/v1.0/createCloudDoc"
		_, err = d.post(pathname, data, nil)
	case MetaGroup:
		data := base.Json{
			"catalogName": dirName,
			"commonAccountInfo": base.Json{
				"account":     d.getAccount(),
				"accountType": 1,
			},
			"groupID":      d.CloudID,
			"parentFileId": parentDir.GetID(),
			"path":         d.dirPath(parentDir),
		}
		pathname := "/orchestration/group-rebuild/catalog/v1.0/createGroupCatalog"
		_, err = d.post(pathname, data, nil)
	default:
		err = errs.NotImplement
	}
	return err
}

func (d *Yun139) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	if d.isShare() {
		return nil, errs.NotImplement
	}
	switch d.Addition.Type {
	case MetaPersonalNew:
		data := base.Json{
			"fileIds":        []string{srcObj.GetID()},
			"toParentFileId": dstDir.GetID(),
		}
		pathname := "/file/batchMove"
		_, err := d.personalPost(pathname, data, nil)
		if err != nil {
			return nil, err
		}
		return srcObj, nil
	case MetaGroup:
		var contentList []string
		var catalogList []string
		if srcObj.IsDir() {
			catalogList = append(catalogList, srcObj.GetID())
		} else {
			contentList = append(contentList, srcObj.GetID())
		}
		data := base.Json{
			"taskType":    3,
			"srcType":     2,
			"srcGroupID":  d.CloudID,
			"destType":    2,
			"destGroupID": d.CloudID,
			"destPath":    dstDir.GetPath(),
			"contentList": contentList,
			"catalogList": catalogList,
			"commonAccountInfo": base.Json{
				"account":     d.getAccount(),
				"accountType": 1,
			},
		}
		pathname := "/orchestration/group-rebuild/task/v1.0/createBatchOprTask"
		_, err := d.post(pathname, data, nil)
		if err != nil {
			return nil, err
		}
		return srcObj, nil
	case MetaPersonal:
		var contentInfoList []string
		var catalogInfoList []string
		if srcObj.IsDir() {
			catalogInfoList = append(catalogInfoList, srcObj.GetID())
		} else {
			contentInfoList = append(contentInfoList, srcObj.GetID())
		}
		data := base.Json{
			"createBatchOprTaskReq": base.Json{
				"taskType":   3,
				"actionType": "304",
				"taskInfo": base.Json{
					"contentInfoList": contentInfoList,
					"catalogInfoList": catalogInfoList,
					"newCatalogID":    dstDir.GetID(),
				},
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			},
		}
		pathname := "/orchestration/personalCloud/batchOprTask/v1.0/createBatchOprTask"
		_, err := d.post(pathname, data, nil)
		if err != nil {
			return nil, err
		}
		return srcObj, nil
	case MetaFamily:
		pathname := "/isbo/openApi/createBatchOprTask"
		var contentList []string
		var catalogList []string
		if srcObj.IsDir() {
			catalogList = append(catalogList, d.dirPath(srcObj))
		} else {
			contentList = append(contentList, d.dirPath(srcObj))
		}

		body := base.Json{
			"catalogList": catalogList,
			"accountInfo": base.Json{
				"accountName": d.getAccount(),
				"accountType": "1",
			},
			"contentList":   contentList,
			"destCatalogID": dstDir.GetID(),
			"destGroupID":   d.CloudID,
			"destPath":      d.dirPath(dstDir),
			"destType":      0,
			"srcGroupID":    d.CloudID,
			"srcType":       0,
			"taskType":      3,
		}

		var resp CreateBatchOprTaskResp
		_, err := d.isboPost(pathname, body, &resp)
		if err != nil {
			return nil, err
		}
		log.Debugf("[139] Move MetaFamily CreateBatchOprTaskResp.Result.ResultCode: %s", resp.Result.ResultCode)
		if resp.Result.ResultCode != "0" {
			return nil, fmt.Errorf("failed to move in family cloud: %s", resp.Result.ResultDesc)
		}
		return srcObj, nil
	default:
		return nil, errs.NotImplement
	}
}

func (d *Yun139) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	if d.isShare() {
		return errs.NotImplement
	}
	var err error
	switch d.Addition.Type {
	case MetaPersonalNew:
		data := base.Json{
			"fileId":      srcObj.GetID(),
			"name":        newName,
			"description": "",
		}
		pathname := "/file/update"
		_, err = d.personalPost(pathname, data, nil)
	case MetaPersonal:
		var data base.Json
		var pathname string
		if srcObj.IsDir() {
			data = base.Json{
				"catalogID":   srcObj.GetID(),
				"catalogName": newName,
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			}
			pathname = "/orchestration/personalCloud/catalog/v1.0/updateCatalogInfo"
		} else {
			data = base.Json{
				"contentID":   srcObj.GetID(),
				"contentName": newName,
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			}
			pathname = "/orchestration/personalCloud/content/v1.0/updateContentInfo"
		}
		_, err = d.post(pathname, data, nil)
	case MetaGroup:
		var data base.Json
		var pathname string
		if srcObj.IsDir() {
			data = base.Json{
				"groupID":           d.CloudID,
				"modifyCatalogID":   srcObj.GetID(),
				"modifyCatalogName": newName,
				"path":              srcObj.GetPath(),
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			}
			pathname = "/orchestration/group-rebuild/catalog/v1.0/modifyGroupCatalog"
		} else {
			data = base.Json{
				"groupID":     d.CloudID,
				"contentID":   srcObj.GetID(),
				"contentName": newName,
				"path":        srcObj.GetPath(),
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			}
			pathname = "/orchestration/group-rebuild/content/v1.0/modifyGroupContent"
		}
		_, err = d.post(pathname, data, nil)
	case MetaFamily:
		var data base.Json
		var pathname string
		if srcObj.IsDir() {
			pathname = "/modifyCloudDocV2"
			data = base.Json{
				"catalogType": 3,
				"cloudID":     d.CloudID,
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": "1",
				},
				"docLibName":   newName,
				"docLibraryID": srcObj.GetID(),
				"path":         d.dirPath(srcObj),
			}
			var resp ModifyCloudDocV2Resp
			_, err = d.andAlbumRequest(pathname, data, &resp)
			if err != nil {
				return err
			}
			if resp.Result.ResultCode != "0" {
				return fmt.Errorf("failed to rename family folder: %s", resp.Result.ResultDesc)
			}
			return nil
		} else {
			data = base.Json{
				"contentID":   srcObj.GetID(),
				"contentName": newName,
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
				"path": srcObj.GetPath(),
			}
			pathname = "/orchestration/familyCloud-rebuild/photoContent/v1.0/modifyContentInfo"
		}
		_, err = d.post(pathname, data, nil)
	default:
		err = errs.NotImplement
	}
	return err
}

func (d *Yun139) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	if d.isShare() {
		return errs.NotImplement
	}
	var err error
	switch d.Addition.Type {
	case MetaPersonalNew:
		data := base.Json{
			"fileIds":        []string{srcObj.GetID()},
			"toParentFileId": dstDir.GetID(),
		}
		pathname := "/file/batchCopy"
		_, err := d.personalPost(pathname, data, nil)
		return err
	case MetaPersonal:
		var contentInfoList []string
		var catalogInfoList []string
		if srcObj.IsDir() {
			catalogInfoList = append(catalogInfoList, srcObj.GetID())
		} else {
			contentInfoList = append(contentInfoList, srcObj.GetID())
		}
		data := base.Json{
			"createBatchOprTaskReq": base.Json{
				"taskType":   3,
				"actionType": 309,
				"taskInfo": base.Json{
					"contentInfoList": contentInfoList,
					"catalogInfoList": catalogInfoList,
					"newCatalogID":    dstDir.GetID(),
				},
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			},
		}
		pathname := "/orchestration/personalCloud/batchOprTask/v1.0/createBatchOprTask"
		_, err = d.post(pathname, data, nil)
	case MetaGroup:
		err = d.handleMetaGroupCopy(ctx, srcObj, dstDir)
	case MetaFamily:
		pathname := "/copyContentCatalog"
		var sourceContentIDs []string
		var sourceCatalogIDs []string
		if srcObj.IsDir() {
			sourceCatalogIDs = append(sourceCatalogIDs, srcObj.GetID())
		} else {
			sourceContentIDs = append(sourceContentIDs, srcObj.GetID())
		}

		body := base.Json{
			"commonAccountInfo": base.Json{
				"accountType":   "1",
				"accountUserId": d.ref.UserDomainID,
			},
			"destCatalogID":    dstDir.GetID(),
			"destCloudID":      d.CloudID,
			"sourceCatalogIDs": sourceCatalogIDs,
			"sourceCloudID":    d.CloudID,
			"sourceContentIDs": sourceContentIDs,
		}

		var resp base.Json // Assuming a generic JSON response for success/failure
		_, err = d.andAlbumRequest(pathname, body, &resp)
		// For now, we assume no error means success.
	default:
		err = errs.NotImplement
	}
	return err
}

func (d *Yun139) Remove(ctx context.Context, obj model.Obj) error {
	if d.isShare() {
		return errs.NotImplement
	}
	switch d.Addition.Type {
	case MetaPersonalNew:
		data := base.Json{
			"fileIds": []string{obj.GetID()},
		}
		pathname := "/recyclebin/batchTrash"
		_, err := d.personalPost(pathname, data, nil)
		return err
	case MetaGroup:
		var contentList []string
		var catalogList []string
		// 必须使用完整路径删除
		if obj.IsDir() {
			catalogList = append(catalogList, obj.GetPath())
		} else {
			contentList = append(contentList, path.Join(obj.GetPath(), obj.GetID()))
		}
		data := base.Json{
			"taskType":    2,
			"srcGroupID":  d.CloudID,
			"contentList": contentList,
			"catalogList": catalogList,
			"commonAccountInfo": base.Json{
				"account":     d.getAccount(),
				"accountType": 1,
			},
		}
		pathname := "/orchestration/group-rebuild/task/v1.0/createBatchOprTask"
		_, err := d.post(pathname, data, nil)
		return err
	case MetaPersonal:
		fallthrough
	case MetaFamily:
		var contentInfoList []string
		var catalogInfoList []string
		if obj.IsDir() {
			catalogInfoList = append(catalogInfoList, obj.GetID())
		} else {
			contentInfoList = append(contentInfoList, obj.GetID())
		}
		data := base.Json{
			"createBatchOprTaskReq": base.Json{
				"taskType":   2,
				"actionType": 201,
				"taskInfo": base.Json{
					"newCatalogID":    "",
					"contentInfoList": contentInfoList,
					"catalogInfoList": catalogInfoList,
				},
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
			},
		}
		pathname := "/orchestration/personalCloud/batchOprTask/v1.0/createBatchOprTask"
		if d.isFamily() {
			data = base.Json{
				"catalogList": catalogInfoList,
				"contentList": contentInfoList,
				"commonAccountInfo": base.Json{
					"account":     d.getAccount(),
					"accountType": 1,
				},
				"sourceCloudID":     d.CloudID,
				"sourceCatalogType": 1002,
				"taskType":          2,
				"path":              obj.GetPath(),
			}
			pathname = "/orchestration/familyCloud-rebuild/batchOprTask/v1.0/createBatchOprTask"
		}
		_, err := d.post(pathname, data, nil)
		return err
	default:
		return errs.NotImplement
	}
}

func (d *Yun139) getPartSize(size int64) int64 {
	if d.CustomUploadPartSize != 0 {
		return d.CustomUploadPartSize
	}
	// 网盘对于分片数量存在上限
	if size/utils.GB > 30 {
		return 512 * utils.MB
	}
	return 100 * utils.MB
}

func (d *Yun139) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) error {
	if d.isShare() {
		return errs.NotImplement
	}
	// PersonalNew 以及 Group/Family 在非旧流模式时走新上传路径
	if d.Addition.Type == MetaPersonalNew ||
		((d.isGroup() || d.isFamily()) && !d.UseOldStreamUpload) {
		var createPath, getUploadUrlPath, completePath string
		if d.isGroup() || d.isFamily() {
			// 家庭云和共享群共用同一套新上传 API
			createPath = "/dynamic/file/create"
			getUploadUrlPath = "/dynamic/file/getUploadUrl"
			completePath = "/dynamic/file/complete"
		} else {
			// MetaPersonalNew
			createPath = "/file/create"
			getUploadUrlPath = "/file/getUploadUrl"
			completePath = "/file/complete"
		}

		var err error
		fullHash := stream.GetHash().GetHash(utils.SHA256)
		if len(fullHash) != utils.SHA256.Width {
			_, fullHash, err = streamPkg.CacheFullAndHash(stream, &up, utils.SHA256)
			if err != nil {
				return err
			}
		}

		size := stream.GetSize()
		partSize := d.getPartSize(size)
		part := int64(1)
		if size > partSize {
			part = (size + partSize - 1) / partSize
		}

		// 生成所有 partInfos
		partInfos := make([]PartInfo, 0, part)
		for i := int64(0); i < part; i++ {
			if utils.IsCanceled(ctx) {
				return ctx.Err()
			}
			start := i * partSize
			byteSize := min(size-start, partSize)
			partNumber := i + 1
			partInfo := PartInfo{
				PartNumber: partNumber,
				PartSize:   byteSize,
				ParallelHashCtx: ParallelHashCtx{
					PartOffset: start,
				},
			}
			partInfos = append(partInfos, partInfo)
		}

		// 筛选出前 100 个 partInfos
		firstPartInfos := partInfos
		if len(firstPartInfos) > 100 {
			firstPartInfos = firstPartInfos[:100]
		}

		// 创建任务，获取上传信息和前100个分片的上传地址
		data := base.Json{
			"contentHash":          fullHash,
			"contentHashAlgorithm": "SHA256",
			"contentType":          "application/octet-stream",
			"parallelUpload":       false,
			"partInfos":            firstPartInfos,
			"size":                 size,
			"parentFileId":         dstDir.GetID(),
			"name":                 stream.GetName(),
			"type":                 "file",
			"fileRenameMode":       "auto_rename",
		}
		// 家庭云和共享群需要额外的参数
		if d.isGroup() || d.isFamily() {
			if d.CloudID == "" {
				return fmt.Errorf("cloud_id is required for group/family upload")
			}
			data["groupId"] = d.CloudID
			if d.isGroup() {
				data["groupType"] = 2
			} else if d.isFamily() {
				data["groupType"] = 1
			}
			data["catalogType"] = 3
			data["seqNo"] = random.String(32)
		}
		var resp PersonalUploadResp
		_, err = d.newPost(createPath, data, &resp)
		if err != nil {
			return err
		}

		// 判断文件是否已存在
		// resp.Data.Exist: true 已存在同名文件且校验相同，云端不会重复增加文件，无需手动处理冲突
		if resp.Data.Exist {
			return nil
		}

		// 判断文件是否支持快传
		// resp.Data.RapidUpload: true 支持快传，但此处直接检测是否返回分片的上传地址
		// 快传的情况下同样需要手动处理冲突
		if resp.Data.PartInfos != nil {
			// Progress
			p := driver.NewProgress(size, up)

			rateLimited := driver.NewLimitedUploadStream(ctx, stream)
			ss, err := streamPkg.NewStreamSectionReader(&streamPkg.FileStream{
				Ctx:    ctx,
				Reader: rateLimited,
				Obj:    &model.Object{Size: size},
			}, int(partSize), &up)
			if err != nil {
				return err
			}

			// 先上传前100个分片
			err = d.uploadPersonalParts(ctx, partInfos, resp.Data.PartInfos, ss, p)
			if err != nil {
				return err
			}

			// 如果还有剩余分片，分批获取上传地址并上传
			for i := 100; i < len(partInfos); i += 100 {
				end := min(i+100, len(partInfos))
				batchPartInfos := partInfos[i:end]
				moredata := base.Json{
					"fileId":    resp.Data.FileId,
					"uploadId":  resp.Data.UploadId,
					"partInfos": batchPartInfos,
					"commonAccountInfo": base.Json{
						"account":     d.getAccount(),
						"accountType": 1,
					},
				}
				var moreresp PersonalUploadUrlResp
				_, err = d.newPost(getUploadUrlPath, moredata, &moreresp)
				if err != nil {
					return err
				}
				err = d.uploadPersonalParts(ctx, partInfos, moreresp.Data.PartInfos, ss, p)
				if err != nil {
					return err
				}
			}

			// 全部分片上传完毕后，complete
			data = base.Json{
				"contentHash":          fullHash,
				"contentHashAlgorithm": "SHA256",
				"fileId":               resp.Data.FileId,
				"uploadId":             resp.Data.UploadId,
			}
			// 家庭云和共享群需要额外的参数
			if d.isGroup() || d.isFamily() {
				data["groupId"] = d.CloudID
			}
			_, err = d.newPost(completePath, data, nil)
			if err != nil {
				return err
			}
		}

		// 处理冲突
		if resp.Data.FileName != stream.GetName() {
			log.Debugf("[139] conflict detected: %s != %s", resp.Data.FileName, stream.GetName())
			// 给服务器一定时间处理数据，避免无法刷新文件列表
			time.Sleep(time.Millisecond * 500)
			// 刷新并获取文件列表
			files, err := d.List(ctx, dstDir, model.ListArgs{Refresh: true})
			if err != nil {
				return err
			}
			// 删除旧文件
			for _, file := range files {
				if file.GetName() == stream.GetName() {
					log.Debugf("[139] conflict: removing old: %s", file.GetName())
					// 删除前重命名旧文件，避免仍旧冲突
					err = d.Rename(ctx, file, stream.GetName()+random.String(4))
					if err != nil {
						return err
					}
					err = d.Remove(ctx, file)
					if err != nil {
						return err
					}
					break
				}
			}
			// 重命名新文件
			for _, file := range files {
				if file.GetName() == resp.Data.FileName {
					log.Debugf("[139] conflict: renaming new: %s => %s", file.GetName(), stream.GetName())
					err = d.Rename(ctx, file, stream.GetName())
					if err != nil {
						return err
					}
					break
				}
			}
		}
		return nil
	}

	// 旧上传路径
	switch d.Addition.Type {
	case MetaPersonal, MetaGroup, MetaFamily:
		// 处理冲突
		// 获取文件列表
		files, err := d.List(ctx, dstDir, model.ListArgs{})
		if err != nil {
			return err
		}
		// 删除旧文件
		for _, file := range files {
			if file.GetName() == stream.GetName() {
				log.Debugf("[139] conflict: removing old: %s", file.GetName())
				// 删除前重命名旧文件，避免仍旧冲突
				err = d.Rename(ctx, file, stream.GetName()+random.String(4))
				if err != nil {
					return err
				}
				err = d.Remove(ctx, file)
				if err != nil {
					return err
				}
				break
			}
		}
		var reportSize int64
		if d.ReportRealSize {
			reportSize = stream.GetSize()
		} else {
			reportSize = 0
		}
		data := base.Json{
			"manualRename": 2,
			"operation":    0,
			"fileCount":    1,
			"totalSize":    reportSize,
			"uploadContentList": []base.Json{{
				"contentName": stream.GetName(),
				"contentSize": reportSize,
				// "digest": "5a3231986ce7a6b46e408612d385bafa"
			}},
			"parentCatalogID": dstDir.GetID(),
			"newCatalogName":  "",
			"commonAccountInfo": base.Json{
				"account":     d.getAccount(),
				"accountType": 1,
			},
		}
		pathname := "/orchestration/personalCloud/uploadAndDownload/v1.0/pcUploadFileRequest"
		if d.isFamily() || d.isGroup() {
			uploadPath := d.dirPath(dstDir)
			// 共享群的根目录上传路径为 0
			if d.isGroup() && dstDir.GetID() == d.RootFolderID {
				uploadPath = "0"
			}
			data = d.newJson(base.Json{
				"fileCount":    1,
				"manualRename": 2,
				"operation":    0,
				"path":         uploadPath,
				"seqNo":        random.String(32), // 序列号不能为空
				"totalSize":    reportSize,
				"uploadContentList": []base.Json{{
					"contentName": stream.GetName(),
					"contentSize": reportSize,
					// "digest": "5a3231986ce7a6b46e408612d385bafa"
				}},
			})
			pathname = "/orchestration/familyCloud-rebuild/content/v1.0/getFileUploadURL"
		}
		var resp UploadResp
		log.Debugf("[139] upload request body: %+v", data)
		_, err = d.post(pathname, data, &resp)
		if err != nil {
			return err
		}
		if resp.Data.Result.ResultCode != "0" {
			return fmt.Errorf("get file upload url failed with result code: %s, message: %s", resp.Data.Result.ResultCode, resp.Data.Result.ResultDesc)
		}

		size := stream.GetSize()
		partSize := d.getPartSize(size)

		// Progress
		p := driver.NewProgress(size, up)
		rateLimited := driver.NewLimitedUploadStream(ctx, stream)

		// StreamSectionReader for per-chunk buffering and retry
		ss, err := streamPkg.NewStreamSectionReader(&streamPkg.FileStream{
			Ctx:    ctx,
			Reader: rateLimited,
			Obj:    &model.Object{Size: size},
		}, int(partSize), &up)
		if err != nil {
			return err
		}

		part := int64(1)
		if size > partSize {
			part = (size + partSize - 1) / partSize
		}
		for i := int64(0); i < part; i++ {
			if utils.IsCanceled(ctx) {
				return ctx.Err()
			}
			start := i * partSize
			byteSize := min(size-start, partSize)

			rd, getErr := ss.GetSectionReader(start, byteSize)
			if getErr != nil {
				return getErr
			}

			err = retry.Do(
				func() error {
					if _, err := rd.Seek(0, io.SeekStart); err != nil {
						return err
					}
					req, reqErr := http.NewRequestWithContext(ctx, http.MethodPost, resp.Data.UploadResult.RedirectionURL,
						io.TeeReader(rd, p))
					if reqErr != nil {
						return reqErr
					}
					req.Header.Set("Content-Type", "text/plain;name="+unicode(stream.GetName()))
					req.Header.Set("contentSize", strconv.FormatInt(size, 10))
					req.Header.Set("range", fmt.Sprintf("bytes=%d-%d", start, start+byteSize-1))
					req.Header.Set("uploadtaskID", resp.Data.UploadResult.UploadTaskID)
					req.Header.Set("rangeType", "0")
					req.ContentLength = byteSize

					res, doErr := base.HttpClient.Do(req)
					if doErr != nil {
						return doErr
					}
					defer res.Body.Close()
					bodyBytes, readErr := io.ReadAll(res.Body)
					if readErr != nil {
						return fmt.Errorf("error reading response body: %v", readErr)
					}
					if res.StatusCode != http.StatusOK {
						return fmt.Errorf("unexpected status code: %d, body: %s", res.StatusCode, string(bodyBytes))
					}
					var result InterLayerUploadResult
					xmlErr := xml.Unmarshal(bodyBytes, &result)
					if xmlErr != nil {
						return fmt.Errorf("error parsing XML: %v", xmlErr)
					}
					if result.ResultCode != 0 {
						return fmt.Errorf("upload failed with result code: %d, message: %s", result.ResultCode, result.Msg)
					}
					return nil
				},
				retry.Context(ctx),
				retry.Attempts(3),
				retry.DelayType(retry.BackOffDelay),
				retry.Delay(time.Second),
			)
			ss.FreeSectionReader(rd)
			if err != nil {
				return err
			}
		}
		return nil
	default:
		return errs.NotImplement
	}
}

func (d *Yun139) Other(ctx context.Context, args model.OtherArgs) (interface{}, error) {
	switch d.Addition.Type {
	case MetaPersonalNew:
		var resp base.Json
		var uri string
		data := base.Json{
			"category": "video",
			"fileId":   args.Obj.GetID(),
		}
		switch args.Method {
		case "video_preview":
			uri = "/videoPreview/getPreviewInfo"
		default:
			return nil, errs.NotSupport
		}
		_, err := d.personalPost(uri, data, &resp)
		if err != nil {
			return nil, err
		}
		return resp["data"], nil
	default:
		return nil, errs.NotImplement
	}
}

func (d *Yun139) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	if d.UserDomainID == "" {
		return nil, errs.NotImplement
	}
	detail, err := d.getDiskQuotaDetail(ctx)
	if err != nil {
		return nil, err
	}

	total := detail.Data.DiskSize * utils.MB
	used := (detail.Data.DiskSize - detail.Data.FreeDiskSize) * utils.MB

	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}

var _ driver.Driver = (*Yun139)(nil)
