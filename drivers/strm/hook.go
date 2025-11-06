package strm

import (
	"context"
	"errors"
	"os"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
	"github.com/tchap/go-patricia/v2/patricia"
)

var strmTrie = patricia.NewTrie()

func UpdateLocalStrm(ctx context.Context, path string, objs []model.Obj) {
	path = utils.FixAndCleanPath(path)
	updateLocal := func(driver *Strm, basePath string, objs []model.Obj) {
		relParent := strings.TrimPrefix(basePath, driver.MountPath)
		localParentPath := stdpath.Join(driver.SaveStrmLocalPath, relParent)
		for _, obj := range objs {
			localPath := stdpath.Join(localParentPath, obj.GetName())
			generateStrm(ctx, driver, obj, localPath)
		}
		deleteExtraFiles(localParentPath, objs)
	}

	_ = strmTrie.VisitPrefixes(patricia.Prefix(path), func(needPathPrefix patricia.Prefix, item patricia.Item) error {
		strmDrivers := item.([]*Strm)
		needPath := string(needPathPrefix)
		restPath := strings.TrimPrefix(path, needPath)
		if len(restPath) > 0 && restPath[0] != '/' {
			return nil
		}
		for _, strmDriver := range strmDrivers {
			strmObjs := strmDriver.convert2strmObjs(ctx, path, objs)
			updateLocal(strmDriver, stdpath.Join(stdpath.Base(needPath), restPath), strmObjs)
		}
		return nil
	})
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

func generateStrm(ctx context.Context, driver *Strm, obj model.Obj, localPath string) {
	if obj.IsDir() {
		err := utils.CreateNestedDirectory(localPath)
		if err != nil {
			log.Warnf("failed to generate strm dir %s: failed to create dir: %v", localPath, err)
			return
		}
	} else {
		link, err := driver.Link(ctx, obj, model.LinkArgs{})
		if err != nil {
			log.Warnf("failed to generate strm of obj %s: failed to link: %v", localPath, err)
			return
		}
		defer link.Close()
		size := link.ContentLength
		if size <= 0 {
			size = obj.GetSize()
		}
		rrf, err := stream.GetRangeReaderFromLink(size, link)
		if err != nil {
			log.Warnf("failed to generate strm of obj %s: failed to get range reader: %v", localPath, err)
			return
		}
		rc, err := rrf.RangeRead(ctx, http_range.Range{Length: -1})
		if err != nil {
			log.Warnf("failed to generate strm of obj %s: failed to read range: %v", localPath, err)
			return
		}
		defer rc.Close()
		file, err := utils.CreateNestedFile(localPath)
		if err != nil {
			log.Warnf("failed to generate strm of obj %s: failed to create local file: %v", localPath, err)
			return
		}
		defer file.Close()
		if _, err := utils.CopyWithBuffer(file, rc); err != nil {
			log.Warnf("failed to generate strm of obj %s: copy failed: %v", localPath, err)
		}
	}
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
