package strm

import (
	"context"
	"io"
	"os"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

var strmMap = make(map[uint]*Strm)

func UpdateLocalStrm(ctx context.Context, parent string, objs []model.Obj) {
	storage, _, err := op.GetStorageAndActualPath(parent)
	if err != nil {
		return
	}
	if d, ok := storage.(*Strm); !ok {
		// 判断非strm驱动的路径是否被strm驱动挂载
		for id := range strmMap {
			strmDriver := strmMap[id]
			if !strmDriver.SaveStrmToLocal {
				continue
			}
			for _, path := range strings.Split(strmDriver.Paths, "\n") {
				path = strings.TrimSpace(path)
				if path == "" {
					continue
				}
				// 如果被挂载则访问strm对应路径触发更新
				if strings.HasPrefix(parent, path) || strings.HasPrefix(parent, path+"/") || parent == path {
					strmPath := path[strings.LastIndex(path, "/"):]
					relPath := stdpath.Join(strmDriver.MountPath, strmPath, strings.TrimPrefix(parent, path))
					if len(relPath) > 0 {
						_, _ = fs.List(ctx, relPath, &fs.ListArgs{Refresh: false, NoLog: true})
					}
				}
			}
		}
	} else {
		if d.SaveStrmToLocal {
			relParent := strings.TrimPrefix(parent, d.MountPath)
			localParentPath := stdpath.Join(d.SaveStrmLocalPath, relParent)

			generateStrm(ctx, d, localParentPath, objs)
			deleteExtraFiles(localParentPath, objs)

			log.Infof("Updating Strm Path %s", localParentPath)
		}
	}
}

func getLocalFiles(localPath string) ([]string, error) {
	var files []string
	entries, err := os.ReadDir(localPath)
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			files = append(files, stdpath.Join(localPath, entry.Name()))
		}
	}
	return files, nil
}

func deleteExtraFiles(localPath string, objs []model.Obj) {
	localFiles, err := getLocalFiles(localPath)
	if err != nil {
		log.Errorf("Failed to read local files from %s: %v", localPath, err)
		return
	}

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
			continue
		}
		localPath := stdpath.Join(localParentPath, obj.GetName())
		file, createErr := utils.CreateNestedFile(localPath)
		if createErr != nil {
			log.Errorf("create nested file failed, %s", createErr)
			continue
		}
		_, copyErr := io.Copy(file, link.MFile)
		if copyErr != nil {
			log.Errorf("copy nested file failed: %s", copyErr)
			continue
		}
		_ = file.Close()
	}
}

func init() {
	op.RegisterObjsUpdateHook(UpdateLocalStrm)
}
