package halalcloudopen

import (
	"context"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	sdkModel "github.com/halalcloud/golang-sdk-lite/halalcloud/model"
	sdkUserFile "github.com/halalcloud/golang-sdk-lite/halalcloud/services/userfile"
)

func (d *HalalCloudOpen) getFiles(ctx context.Context, dir model.Obj) ([]model.Obj, error) {

	files := make([]model.Obj, 0)
	limit := int64(100)
	token := ""

	for {
		result, err := d.sdkUserFileService.List(ctx, &sdkUserFile.FileListRequest{
			Parent: &sdkUserFile.File{Path: dir.GetPath()},
			ListInfo: &sdkModel.ScanListRequest{
				Limit: limit,
				Token: token,
			},
		})
		if err != nil {
			return nil, err
		}

		for i := 0; len(result.Files) > i; i++ {
			files = append(files, NewObjFile(result.Files[i]))
		}

		if result.ListInfo == nil || result.ListInfo.Token == "" {
			break
		}
		token = result.ListInfo.Token

	}
	return files, nil
}

func (d *HalalCloudOpen) makeDir(ctx context.Context, dir model.Obj, name string) (model.Obj, error) {
	_, err := d.sdkUserFileService.Create(ctx, &sdkUserFile.File{
		Path: dir.GetPath(),
		Name: name,
	})
	return nil, err
}

func (d *HalalCloudOpen) move(ctx context.Context, obj model.Obj, dir model.Obj) (model.Obj, error) {
	oldDir := obj.GetPath()
	newDir := dir.GetPath()
	_, err := d.sdkUserFileService.Move(ctx, &sdkUserFile.BatchOperationRequest{
		Source: []*sdkUserFile.File{
			{
				Path: oldDir,
			},
		},
		Dest: &sdkUserFile.File{
			Path: newDir,
		},
	})
	return nil, err
}

func (d *HalalCloudOpen) rename(ctx context.Context, obj model.Obj, name string) (model.Obj, error) {

	_, err := d.sdkUserFileService.Rename(ctx, &sdkUserFile.File{
		Path: obj.GetPath(),
		Name: name,
	})
	return nil, err
}

func (d *HalalCloudOpen) copy(ctx context.Context, obj model.Obj, dir model.Obj) (model.Obj, error) {
	id := obj.GetID()
	sourcePath := obj.GetPath()
	if len(id) > 0 {
		sourcePath = ""
	}

	destID := dir.GetID()
	destPath := dir.GetPath()
	if len(destID) > 0 {
		destPath = ""
	}
	dest := &sdkUserFile.File{
		Path:     destPath,
		Identity: destID,
	}
	_, err := d.sdkUserFileService.Copy(ctx, &sdkUserFile.BatchOperationRequest{
		Source: []*sdkUserFile.File{
			{
				Path:     sourcePath,
				Identity: id,
			},
		},
		Dest: dest,
	})
	return nil, err
}

func (d *HalalCloudOpen) remove(ctx context.Context, obj model.Obj) error {
	id := obj.GetID()
	_, err := d.sdkUserFileService.Delete(ctx, &sdkUserFile.BatchOperationRequest{
		Source: []*sdkUserFile.File{
			{
				Identity: id,
				Path:     obj.GetPath(),
			},
		},
	})
	return err
}

func (d *HalalCloudOpen) details(ctx context.Context) (*model.StorageDetails, error) {
	ret, err := d.sdkUserService.GetStatisticsAndQuota(ctx)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: ret.DiskStatisticsQuota.BytesQuota,
			UsedSpace:  ret.DiskStatisticsQuota.BytesUsed,
		},
	}, nil
}
