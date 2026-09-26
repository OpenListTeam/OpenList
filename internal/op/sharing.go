package op

import (
	"fmt"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/go-cache"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gorm.io/gorm"
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

// domainSharingCache 按虚拟主机 domain 作为 key 缓存对应的 *model.Sharing。
// 允许缓存为 nil 以实现"负缓存"防止穿透。
var domainSharingCache = cache.NewMemCache(cache.WithShards[*model.Sharing](2))
var domainSharingG singleflight.Group[*model.Sharing]

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

// GetSharingByDomain 根据 domain 获取可用的虚拟主机 sharing（带缓存）。
// 仅当 sharing.Domain 非空、Disabled=false、Files 非空、Expires 未过期时才视为有效。
// 如果在 DB 中未找到，会负缓存 5 分钟，避免反复穿透 DB。
func GetSharingByDomain(domain string) (*model.Sharing, error) {
	domain = strings.ToLower(strings.TrimSpace(domain))
	if domain == "" {
		return nil, errors.New("empty domain")
	}
	if s, ok := domainSharingCache.Get(domain); ok {
		if s == nil {
			log.Debugf("[Sharing] domain cache hit (nil) for %q", domain)
			return nil, errors.New("sharing not found by domain")
		}
		log.Debugf("[Sharing] domain cache hit for %q id=%s", domain, s.ID)
		if !s.ValidForVhost() {
			return nil, errors.New("sharing not valid")
		}
		return s, nil
	}
	sharing, err, _ := domainSharingG.Do(domain, func() (*model.Sharing, error) {
		sdb, err := db.GetSharingByDomain(domain)
		if err != nil {
			if errors.Is(errors.Cause(err), gorm.ErrRecordNotFound) {
				log.Debugf("[Sharing] domain=%q not found in db, caching nil", domain)
				domainSharingCache.Set(domain, nil, cache.WithEx[*model.Sharing](time.Minute*5))
				return nil, errors.New("sharing not found by domain")
			}
			return nil, errors.WithMessagef(err, "failed get sharing by domain [%s]", domain)
		}
		// 虚拟主机场景不需要 creator，跳过 creator 查询以避免 CanShare 校验阻断 Web Hosting
		var files []string
		if err = utils.Json.UnmarshalFromString(sdb.FilesRaw, &files); err != nil {
			files = make([]string, 0)
		}
		s := &model.Sharing{
			SharingDB: sdb,
			Files:     files,
			Creator:   nil, // 虚拟主机匹配不依赖 creator 权限
		}
		domainSharingCache.Set(domain, s, cache.WithEx[*model.Sharing](time.Hour))
		return s, nil
	})
	if err != nil {
		return nil, err
	}
	if sharing == nil || !sharing.ValidForVhost() {
		return nil, errors.New("sharing not valid for domain")
	}
	return sharing, nil
}

// invalidateDomainCache 在创建/更新/删除记录时调用，同时传入新/旧 domain 以使两者都失效。
func invalidateDomainCache(domains ...string) {
	for _, d := range domains {
		if d != "" {
			domainSharingCache.Del(d)
		}
	}
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
	// Re-validate that the shared paths are still within the creator's current
	// BasePath. This prevents access to files that fell out-of-scope after the
	// creator's BasePath was changed by an admin.
	if sharing.Creator != nil && !sharing.Creator.IsAdmin() {
		for _, f := range sharing.Files {
			if !utils.IsSubPath(sharing.Creator.BasePath, f) {
				return "", errors.Errorf("sharing path [%s] is outside the creator's base path", f)
			}
		}
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
	id, err = db.CreateSharing(sharing.SharingDB)
	if err == nil {
		invalidateDomainCache(sharing.Domain)
	}
	return id, err
}

func UpdateSharing(sharing *model.Sharing, skipMarshal ...bool) (err error) {
	if !utils.IsBool(skipMarshal...) {
		sharing.CreatorId = sharing.Creator.ID
		sharing.FilesRaw, err = utils.Json.MarshalToString(utils.MustSliceConvert(sharing.Files, utils.FixAndCleanPath))
		if err != nil {
			return errors.WithStack(err)
		}
	}
	// 读取旧记录以便同时失效旧 domain 缓存
	var oldDomain string
	if old, e := db.GetSharingById(sharing.ID); e == nil {
		oldDomain = old.Domain
	}
	err = db.UpdateSharing(sharing.SharingDB)
	if err == nil {
		sharingCache.Del(sharing.ID)
		invalidateDomainCache(oldDomain, sharing.Domain)
	}
	return err
}

func UpdateSharingId(sharing *model.Sharing, newId string) error {
	sharingCache.Del(sharing.ID)
	if err := db.UpdateSharingId(sharing.ID, newId); err != nil {
		return err
	}
	sharing.ID = newId
	return nil
}

func DeleteSharing(sid string) error {
	// 先读取 domain 用于失效缓存
	var oldDomain string
	if old, e := db.GetSharingById(sid); e == nil {
		oldDomain = old.Domain
	}
	err := db.DeleteSharingById(sid)
	if err == nil {
		sharingCache.Del(sid)
		invalidateDomainCache(oldDomain)
	}
	return err
}

func DeleteSharingsByCreatorId(creatorId uint) error {
	return db.DeleteSharingsByCreatorId(creatorId)
}
