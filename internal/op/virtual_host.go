package op

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/go-cache"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

var vhostCache = cache.NewMemCache(cache.WithShards[*model.VirtualHost](2))

// GetVirtualHostByDomain 根据域名获取虚拟主机配置（带缓存）
func GetVirtualHostByDomain(domain string) (*model.VirtualHost, error) {
	if v, ok := vhostCache.Get(domain); ok {
		if v == nil {
			utils.Log.Infof("[VirtualHost] cache hit (nil) for domain=%q", domain)
			return nil, errors.New("virtual host not found")
		}
		utils.Log.Infof("[VirtualHost] cache hit for domain=%q id=%d", domain, v.ID)
		return v, nil
	}
	utils.Log.Infof("[VirtualHost] cache miss for domain=%q, querying db...", domain)
	v, err := db.GetVirtualHostByDomain(domain)
	if err != nil {
		if errors.Is(errors.Cause(err), gorm.ErrRecordNotFound) {
			utils.Log.Infof("[VirtualHost] domain=%q not found in db, caching nil", domain)
			vhostCache.Set(domain, nil, cache.WithEx[*model.VirtualHost](time.Minute*5))
			return nil, errors.New("virtual host not found")
		}
		utils.Log.Errorf("[VirtualHost] db error for domain=%q: %v", domain, err)
		return nil, err
	}
	utils.Log.Infof("[VirtualHost] db found domain=%q id=%d enabled=%v web_hosting=%v", domain, v.ID, v.Enabled, v.WebHosting)
	vhostCache.Set(domain, v, cache.WithEx[*model.VirtualHost](time.Hour))
	return v, nil
}

func GetVirtualHostById(id uint) (*model.VirtualHost, error) {
	return db.GetVirtualHostById(id)
}

func CreateVirtualHost(v *model.VirtualHost) error {
	v.Path = utils.FixAndCleanPath(v.Path)
	vhostCache.Del(v.Domain)
	return db.CreateVirtualHost(v)
}

func UpdateVirtualHost(v *model.VirtualHost) error {
	v.Path = utils.FixAndCleanPath(v.Path)
	old, err := db.GetVirtualHostById(v.ID)
	if err != nil {
		return err
	}
	// 如果域名变更，清除旧域名缓存
	vhostCache.Del(old.Domain)
	vhostCache.Del(v.Domain)
	return db.UpdateVirtualHost(v)
}

func DeleteVirtualHostById(id uint) error {
	old, err := db.GetVirtualHostById(id)
	if err != nil {
		return err
	}
	vhostCache.Del(old.Domain)
	return db.DeleteVirtualHostById(id)
}

func GetVirtualHosts(pageIndex, pageSize int) ([]model.VirtualHost, int64, error) {
	return db.GetVirtualHosts(pageIndex, pageSize)
}
