package op

import (
	stdpath "path"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/go-cache"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

var metaCache = cache.NewMemCache(cache.WithShards[*model.Meta](2))
var metaSnapshotMu sync.Mutex
var metaSnapshot []model.Meta
var metaSnapshotLoaded bool

// metaG maybe not needed
var metaG singleflight.Group[*model.Meta]

func GetNearestMeta(path string) (*model.Meta, error) {
	return getNearestMeta(utils.FixAndCleanPath(path))
}
func getNearestMeta(path string) (*model.Meta, error) {
	var metas []model.Meta
	loaded := false
	for {
		meta, err := GetMetaByPath(path)
		if err == nil {
			return meta, nil
		}
		if errors.Cause(err) != errs.MetaNotFound {
			return nil, err
		}
		if !loaded {
			metas, err = getMetaSnapshot()
			if err != nil {
				return nil, err
			}
			loaded = true
		}
		var matched *model.Meta
		for i := range metas {
			if !strings.EqualFold(utils.FixAndCleanPath(metas[i].Path), path) {
				continue
			}
			if matched != nil {
				return nil, errors.Errorf("multiple metas match %q case-insensitively", path)
			}
			matched = &metas[i]
		}
		if matched != nil {
			return matched, nil
		}
		if path == "/" {
			return nil, errs.MetaNotFound
		}
		path = stdpath.Dir(path)
	}
}

func getMetaSnapshot() ([]model.Meta, error) {
	metaSnapshotMu.Lock()
	defer metaSnapshotMu.Unlock()
	if metaSnapshotLoaded {
		return metaSnapshot, nil
	}
	metas, err := db.GetAllMetas()
	if err != nil {
		return nil, err
	}
	metaSnapshot = metas
	metaSnapshotLoaded = true
	return metaSnapshot, nil
}

func GetMetaByPath(path string) (*model.Meta, error) {
	return getMetaByPath(utils.FixAndCleanPath(path))
}
func getMetaByPath(path string) (*model.Meta, error) {
	meta, ok := metaCache.Get(path)
	if ok {
		if meta == nil {
			return meta, errs.MetaNotFound
		}
		return meta, nil
	}
	meta, err, _ := metaG.Do(path, func() (*model.Meta, error) {
		_meta, err := db.GetMetaByPath(path)
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				metaCache.Set(path, nil)
				return nil, errs.MetaNotFound
			}
			return nil, err
		}
		metaCache.Set(path, _meta, cache.WithEx[*model.Meta](time.Hour))
		return _meta, nil
	})
	return meta, err
}

func DeleteMetaById(id uint) error {
	old, err := db.GetMetaById(id)
	if err != nil {
		return err
	}
	metaCache.Del(old.Path)
	metaSnapshotMu.Lock()
	defer metaSnapshotMu.Unlock()
	err = db.DeleteMetaById(id)
	metaSnapshot = nil
	metaSnapshotLoaded = false
	return err
}

func UpdateMeta(u *model.Meta) error {
	u.Path = utils.FixAndCleanPath(u.Path)
	old, err := db.GetMetaById(u.ID)
	if err != nil {
		return err
	}
	metaCache.Del(old.Path)
	metaCache.Del(u.Path)
	metaSnapshotMu.Lock()
	defer metaSnapshotMu.Unlock()
	err = db.UpdateMeta(u)
	metaSnapshot = nil
	metaSnapshotLoaded = false
	return err
}

func CreateMeta(u *model.Meta) error {
	u.Path = utils.FixAndCleanPath(u.Path)
	metaCache.Del(u.Path)
	metaSnapshotMu.Lock()
	defer metaSnapshotMu.Unlock()
	err := db.CreateMeta(u)
	metaSnapshot = nil
	metaSnapshotLoaded = false
	return err
}

func GetMetaById(id uint) (*model.Meta, error) {
	return db.GetMetaById(id)
}

func GetMetas(pageIndex, pageSize int) (metas []model.Meta, count int64, err error) {
	return db.GetMetas(pageIndex, pageSize)
}
