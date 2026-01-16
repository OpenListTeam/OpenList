package degoo

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type Degoo struct {
	model.Storage
	Addition
	client *http.Client
}

func (d *Degoo) Config() driver.Config {
	return config
}

func (d *Degoo) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *Degoo) Init(ctx context.Context) error {

	d.client = base.HttpClient

	// Ensure we have a valid token (will login if needed or refresh if expired)
	if err := d.ensureValidToken(ctx); err != nil {
		return fmt.Errorf("failed to initialize token: %w", err)
	}

	return d.getDevices(ctx)
}

func (d *Degoo) Drop(ctx context.Context) error {
	return nil
}

func (d *Degoo) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	items, err := d.getAllFileChildren5(ctx, dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.MustSliceConvert(items, func(s DegooFileItem) model.Obj {
		isFolder := s.Category == 2 || s.Category == 1 || s.Category == 10

		createTime, modTime, _ := humanReadableTimes(s.CreationTime, s.LastModificationTime, s.LastUploadTime)

		size, err := strconv.ParseInt(s.Size, 10, 64)
		if err != nil {
			size = 0 // Default to 0 if size parsing fails
		}

		return &model.Object{
			ID:       s.ID,
			Path:     s.FilePath,
			Name:     s.Name,
			Size:     size,
			Modified: modTime,
			Ctime:    createTime,
			IsFolder: isFolder,
		}
	}), nil
}

func (d *Degoo) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	item, err := d.getOverlay4(ctx, file.GetID())
	if err != nil {
		return nil, err
	}

	return &model.Link{URL: item.URL}, nil
}

func (d *Degoo) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) error {
	// This is done by calling the setUploadFile3 API with a special checksum and size.
	const query = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) { setUploadFile3(Token: $Token, FileInfos: $FileInfos) }`

	variables := map[string]interface{}{
		"Token": d.AccessToken,
		"FileInfos": []map[string]interface{}{
			{
				"Checksum":     folderChecksum,
				"Name":         dirName,
				"CreationTime": time.Now().UnixMilli(),
				"ParentID":     parentDir.GetID(),
				"Size":         0,
			},
		},
	}

	_, err := d.apiCall(ctx, "SetUploadFile3", query, variables)
	if err != nil {
		return err
	}

	return nil
}

func (d *Degoo) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	const query = `mutation SetMoveFile($Token: String!, $Copy: Boolean, $NewParentID: String!, $FileIDs: [String]!) { setMoveFile(Token: $Token, Copy: $Copy, NewParentID: $NewParentID, FileIDs: $FileIDs) }`

	variables := map[string]interface{}{
		"Token":       d.AccessToken,
		"Copy":        false,
		"NewParentID": dstDir.GetID(),
		"FileIDs":     []string{srcObj.GetID()},
	}

	_, err := d.apiCall(ctx, "SetMoveFile", query, variables)
	if err != nil {
		return nil, err
	}

	return srcObj, nil
}

func (d *Degoo) Rename(ctx context.Context, srcObj model.Obj, newName string) error {
	const query = `mutation SetRenameFile($Token: String!, $FileRenames: [FileRenameInfo]!) { setRenameFile(Token: $Token, FileRenames: $FileRenames) }`

	variables := map[string]interface{}{
		"Token": d.AccessToken,
		"FileRenames": []DegooFileRenameInfo{
			{
				ID:      srcObj.GetID(),
				NewName: newName,
			},
		},
	}

	_, err := d.apiCall(ctx, "SetRenameFile", query, variables)
	if err != nil {
		return err
	}
	return nil
}

func (d *Degoo) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// Copy is not implemented, Degoo API does not support direct copy.
	return nil, errs.NotImplement
}

func (d *Degoo) Remove(ctx context.Context, obj model.Obj) error {
	// Remove deletes a file or folder (moves to trash).
	const query = `mutation SetDeleteFile5($Token: String!, $IsInRecycleBin: Boolean!, $IDs: [IDType]!) { setDeleteFile5(Token: $Token, IsInRecycleBin: $IsInRecycleBin, IDs: $IDs) }`

	variables := map[string]interface{}{
		"Token":          d.AccessToken,
		"IsInRecycleBin": false,
		"IDs":            []map[string]string{{"FileID": obj.GetID()}},
	}

	_, err := d.apiCall(ctx, "SetDeleteFile5", query, variables)
	return err
}

func (d *Degoo) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	tmpF, err := file.CacheFullAndWriter(&up, nil)
	if err != nil {
		return err
	}

	parentID := dstDir.GetID()

	// Calculate the checksum for the file.
	checksum, err := d.checkSum(tmpF)
	if err != nil {
		return err
	}

	// 1. Get upload authorization via getBucketWriteAuth4.
	auths, err := d.getBucketWriteAuth4(ctx, file, parentID, checksum)
	if err != nil {
		return err
	}

	// 2. Upload file.
	// support rapid upload
	if auths.GetBucketWriteAuth4[0].Error != "Already exist!" {
		err = d.uploadS3(ctx, auths, tmpF, file, checksum)
		if err != nil {
			return err
		}
	}

	// 3. Register metadata with setUploadFile3.
	data, err := d.SetUploadFile3(ctx, file, parentID, checksum)
	if err != nil {
		return err
	}
	if !data.SetUploadFile3 {
		return fmt.Errorf("setUploadFile3 failed: %v", data)
	}
	return nil
}

func (d *Degoo) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	quota, err := d.getUserInfo(ctx)
	if err != nil {
		return nil, err
	}
	used, err := strconv.ParseInt(quota.GetUserInfo3.UsedQuota, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse used quota: %v", err)
	}
	total, err := strconv.ParseInt(quota.GetUserInfo3.TotalQuota, 10, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse total quota: %v", err)
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}
