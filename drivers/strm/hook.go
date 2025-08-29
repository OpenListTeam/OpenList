package strm

import (
	"context"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
	"io"
	"os"
	stdpath "path"
	"strings"
)

func UpdateLocalStrm(ctx context.Context, parent string, objs []model.Obj) {
	storage, _, err := op.GetStorageAndActualPath(parent)
	if err != nil {
		return
	}

	d, ok := storage.(*Strm)
	if !ok || !d.SaveStrmToLocal {
		return
	}

	relParent := strings.TrimPrefix(parent, d.MountPath)
	localParentPath := stdpath.Join(d.SaveStrmLocalPath, relParent)

	generateStrm(ctx, d, localParentPath, objs)
	deleteExtraFiles(localParentPath, objs)

	log.Infof("Updating Strm Path %s", localParentPath)
}

func getLocalFiles(localPath string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(localPath)
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, stdpath.Join(localPath, entry.Name()))
		}
	}
	return files, err
}

func deleteExtraFiles(localPath string, objs []model.Obj) {
	localFiles, _ := getLocalFiles(localPath)

	objsSet := make(map[string]struct{})
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		objsSet[stdpath.Join(localPath, obj.GetName())] = struct{}{}
	}

	for _, localFile := range localFiles {
		if _, exists := objsSet[localFile]; !exists {
			err := os.Remove(localFile)
			if err != nil {
				log.Errorf("Failed to delete file: %s, error: %v\n", localFile, err)
			} else {
				log.Infof("Deleted file %s", localFile)
			}
		}
	}
}

func generateStrm(ctx context.Context, d *Strm, localParentPath string, objs []model.Obj) {
	for _, obj := range objs {
		if obj.IsDir() {
			continue
		}
		link, linkErr := d.Link(ctx, obj, model.LinkArgs{})
		if linkErr != nil {
			log.Errorf("get link failed, %s", linkErr)
			return
		}
		localPath := stdpath.Join(localParentPath, obj.GetName())
		file, createErr := utils.CreateNestedFile(localPath)
		if createErr != nil {
			log.Errorf("create nested file failed, %s", createErr)
			return
		}
		_, copyErr := io.Copy(file, link.MFile)
		if copyErr != nil {
			log.Errorf("copy nested file failed: %s", copyErr)
			return
		}
		_ = file.Close()
	}
}

func init() {
	op.RegisterObjsUpdateHook(UpdateLocalStrm)
}
