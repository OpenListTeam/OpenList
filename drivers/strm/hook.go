package strm

import (
	"context"
	"errors"
	"io"
	"os"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
	"github.com/tchap/go-patricia/v2/patricia"
)

var strmTrie = patricia.NewTrie()

func UpdateLocalStrm(ctx context.Context, path string, objs []model.Obj) {
	updateLocal := func(driver *Strm, basePath string, objs []model.Obj) {
		relParent := strings.TrimPrefix(basePath, driver.MountPath)
		localParentPath := stdpath.Join(driver.SaveStrmLocalPath, relParent)
		for _, obj := range objs {
			localPath := stdpath.Join(localParentPath, obj.GetName())
			link := driver.getLink(ctx, stdpath.Join(basePath, obj.GetName()))
			generateStrm(localPath, link)
		}
		deleteExtraFiles(localParentPath, objs)
	}
	storage, _, err := op.GetStorageAndActualPath(path)
	if err != nil {
		return
	}

	// 如果 path 本身是 Strm 驱动
	if d, ok := storage.(*Strm); ok && d.SaveStrmToLocal {
		updateLocal(d, path, objs)
		return
	}

	strmList := FindAllStrmForPath(path)
	if strmList == nil || len(strmList) == 0 {
		return
	}

	for _, strmDriver := range strmList {
		if !strmDriver.SaveStrmToLocal {
			continue
		}
		for _, needPath := range strings.Split(strmDriver.Paths, "\n") {
			needPath = strings.TrimSpace(needPath)
			if needPath == "" {
				continue
			}
			if path == needPath || strings.HasPrefix(path, needPath) || strings.HasPrefix(path, needPath+"/") {
				var strmObjs []model.Obj
				for _, obj := range objs {
					strmObj := strmDriver.convert2strmObj(ctx, path, obj)
					strmObjs = append(strmObjs, &strmObj)
				}
				updateLocal(strmDriver, stdpath.Join(stdpath.Base(needPath), strings.TrimPrefix(path, needPath)), strmObjs)
				break
			}
		}
	}
}

func FindAllStrmForPath(target string) []*Strm {
	target = strings.TrimRight(target, "/")
	var matches []*Strm
	err := strmTrie.VisitPrefixes(patricia.Prefix(target), func(prefix patricia.Prefix, item patricia.Item) error {
		if lst, ok := item.([]*Strm); ok {
			matches = append(matches, lst...)
		}
		return nil
	})

	if err != nil {
		return nil
	}
	return matches
}

func InsertStrm(dstPath string, d *Strm) error {
	prefix := patricia.Prefix(strings.TrimRight(dstPath, "/"))
	existing := strmTrie.Get(prefix)

	if existing == nil {
		if !strmTrie.Insert(prefix, []*Strm{d}) {
			return errors.New("failed to insert strm")
		}
		return nil
	}
	if lst, ok := existing.([]*Strm); ok {
		strmTrie.Set(prefix, append(lst, d))
	} else {
		return errors.New("invalid trie item type")
	}

	return nil
}

func RemoveStrm(dstPath string, d *Strm) {
	prefix := patricia.Prefix(strings.TrimRight(dstPath, "/"))
	existing := strmTrie.Get(prefix)
	if existing == nil {
		return
	}
	lst, ok := existing.([]*Strm)
	if !ok {
		return
	}
	if len(lst) == 1 && lst[0] == d {
		strmTrie.Delete(prefix)
		return
	}

	for i, di := range lst {
		if di == d {
			newList := append(lst[:i], lst[i+1:]...)
			strmTrie.Set(prefix, newList)
			return
		}
	}
}

func generateStrm(localPath, link string) {
	file, err := utils.CreateNestedFile(localPath)
	if err != nil {
		log.Warnf("skip obj %s: failed to create file: %v", localPath, err)
		return
	}

	if _, err := io.Copy(file, strings.NewReader(link)); err != nil {
		log.Warnf("copy failed for obj %s: %v", localPath, err)
	}
	_ = file.Close()

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

func init() {
	op.RegisterObjsUpdateHook(UpdateLocalStrm)
}
