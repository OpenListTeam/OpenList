package op

import (
	"fmt"
	stdpath "path"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/go-cache"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

func makeJoined(sdb []model.SharingDB) []model.Sharing {
	creator := make(map[uint]*model.User)
	return utils.MustSliceConvert(sdb, func(s model.SharingDB) model.Sharing {
		var c *model.User
		var ok bool
		if c, ok = creator[s.CreatorId]; !ok {
			var err error
			if c, err = GetUserById(s.CreatorId); err != nil {
				c = nil
			} else {
				creator[s.CreatorId] = c
			}
		}
		var files []string
		if err := utils.Json.UnmarshalFromString(s.FilesRaw, &files); err != nil {
			files = make([]string, 0)
		}
		return model.Sharing{
			SharingDB: &s,
			Files:     files,
			Creator:   c,
		}
	})
}

var sharingCache = cache.NewMemCache(cache.WithShards[*model.Sharing](8))
var sharingG singleflight.Group[*model.Sharing]

func GetSharingById(id string, refresh ...bool) (*model.Sharing, error) {
	if !utils.IsBool(refresh...) {
		if sharing, ok := sharingCache.Get(id); ok {
			log.Debugf("use cache when get sharing %s", id)
			return sharing, nil
		}
	}
	sharing, err, _ := sharingG.Do(id, func() (*model.Sharing, error) {
		s, err := db.GetSharingById(id)
		if err != nil {
			return nil, errors.WithMessagef(err, "failed get sharing [%s]", id)
		}
		creator, err := GetUserById(s.CreatorId)
		if err != nil {
			return nil, errors.WithMessagef(err, "failed get sharing creator [%s]", id)
		}
		var files []string
		if err = utils.Json.UnmarshalFromString(s.FilesRaw, &files); err != nil {
			files = make([]string, 0)
		}
		return &model.Sharing{
			SharingDB: s,
			Files:     files,
			Creator:   creator,
		}, nil
	})
	return sharing, err
}

func GetSharings(pageIndex, pageSize int) ([]model.Sharing, int64, error) {
	s, cnt, err := db.GetSharings(pageIndex, pageSize)
	if err != nil {
		return nil, 0, errors.WithStack(err)
	}
	return makeJoined(s), cnt, nil
}

func GetSharingsByCreatorId(userId uint, pageIndex, pageSize int) ([]model.Sharing, int64, error) {
	s, cnt, err := db.GetSharingsByCreatorId(userId, pageIndex, pageSize)
	if err != nil {
		return nil, 0, errors.WithStack(err)
	}
	return makeJoined(s), cnt, nil
}

func GetSharingUnwrapPath(sharing *model.Sharing, path string) (unwrapPath string, err error) {
	if len(sharing.Files) == 0 {
		return "", errors.New("cannot get actual path of an invalid sharing")
	}
	if len(sharing.Files) == 1 {
		return stdpath.Join(sharing.Files[0], path), nil
	}
	path = utils.FixAndCleanPath(path)[1:]
	if len(path) == 0 {
		return "", errors.New("cannot get actual path of a sharing root path")
	}
	mapPath := ""
	child, rest, _ := strings.Cut(path, "/")
	for _, c := range sharing.Files {
		if child == stdpath.Base(c) {
			mapPath = c
			break
		}
	}
	if mapPath == "" {
		return "", fmt.Errorf("failed find child [%s] of sharing [%s]", child, sharing.ID)
	}
	return stdpath.Join(mapPath, rest), nil
}

func CreateSharing(sharing *model.Sharing) (id string, err error) {
	sharing.CreatorId = sharing.Creator.ID
	sharing.FilesRaw, err = utils.Json.MarshalToString(utils.MustSliceConvert(sharing.Files, utils.FixAndCleanPath))
	if err != nil {
		return "", errors.WithStack(err)
	}
	return db.CreateSharing(sharing.SharingDB)
}

func UpdateSharing(sharing *model.Sharing, skipMarshal ...bool) (err error) {
	if !utils.IsBool(skipMarshal...) {
		sharing.CreatorId = sharing.Creator.ID
		sharing.FilesRaw, err = utils.Json.MarshalToString(utils.MustSliceConvert(sharing.Files, utils.FixAndCleanPath))
		if err != nil {
			return errors.WithStack(err)
		}
	}
	sharingCache.Del(sharing.ID)
	return db.UpdateSharing(sharing.SharingDB)
}

func DeleteSharing(sid string) error {
	sharingCache.Del(sid)
	return db.DeleteSharingById(sid)
}

func DeleteSharingsByCreatorId(creatorId uint) error {
	return db.DeleteSharingsByCreatorId(creatorId)
}
