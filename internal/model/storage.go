package model

import (
	"encoding/json"
	"time"
)

type Storage struct {
	ID                  uint      `json:"id" gorm:"primaryKey"`                        // unique key
	MountPath           string    `json:"mount_path" gorm:"unique" binding:"required"` // must be standardized
	Order               int       `json:"order"`                                       // use to sort
	Driver              string    `json:"driver"`                                      // driver used
	CacheExpiration     int       `json:"cache_expiration"`                            // cache expire time
	CustomCachePolicies string    `json:"custom_cache_policies" gorm:"type:text"`
	Status              string    `json:"status"`
	Addition            string    `json:"addition" gorm:"type:text"` // Additional information, defined in the corresponding driver
	Remark              string    `json:"remark"`
	Modified            time.Time `json:"modified"`
	Disabled            bool      `json:"disabled"` // if disabled
	DisableIndex        bool      `json:"disable_index"`
	EnableSign          bool      `json:"enable_sign"`
	Sort
	Proxy
}

type Sort struct {
	OrderBy        string `json:"order_by"`
	OrderDirection string `json:"order_direction"`
	ExtractFolder  string `json:"extract_folder"`
}

type Proxy struct {
	WebProxy     bool   `json:"web_proxy"`
	WebdavPolicy string `json:"webdav_policy"`
	ProxyRange   bool   `json:"proxy_range"`
	DownProxyURL string `json:"down_proxy_url"`
	// Disable sign for DownProxyURL
	DisableProxySign bool `json:"disable_proxy_sign"`
}

func (s *Storage) GetStorage() *Storage {
	return s
}

func (s *Storage) SetStorage(storage Storage) {
	*s = storage
}

func (s *Storage) SetStatus(status string) {
	s.Status = status
}

func (p Proxy) Webdav302() bool {
	return p.WebdavPolicy == "302_redirect"
}

func (p Proxy) WebdavProxyURL() bool {
	return p.WebdavPolicy == "use_proxy_url"
}

type DiskUsage struct {
	TotalSpace int64
	UsedSpace  int64
}

func (d DiskUsage) FreeSpace() int64 {
	return d.TotalSpace - d.UsedSpace
}

func (d DiskUsage) MarshalJSON() ([]byte, error) {
	return json.Marshal(map[string]interface{}{
		"total_space": d.TotalSpace,
		"used_space":  d.UsedSpace,
		"free_space":  d.FreeSpace(),
	})
}

type StorageDetails struct {
	DiskUsage
}

type ObjWithStorageDetails interface {
	GetStorageDetails() *StorageDetails
}

type ObjStorageDetails struct {
	Obj
	*StorageDetails
}

func (o *ObjStorageDetails) Unwrap() Obj {
	return o.Obj
}

func (o *ObjStorageDetails) GetStorageDetails() *StorageDetails {
	return o.StorageDetails
}

func GetStorageDetails(obj Obj) (*StorageDetails, bool) {
	if obj, ok := obj.(ObjWithStorageDetails); ok {
		return obj.GetStorageDetails(), true
	}
	if unwrap, ok := obj.(ObjUnwrap); ok {
		return GetStorageDetails(unwrap.Unwrap())
	}
	return nil, false
}
