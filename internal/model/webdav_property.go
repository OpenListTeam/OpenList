package model

import (
	"crypto/sha256"
	"encoding/hex"

	"gorm.io/gorm"
)

// WebDAVProperty stores a dead WebDAV property for a resource.
//
// Path, Namespace and Name are TEXT columns so that long WebDAV paths are
// never truncated or rejected. MySQL cannot index TEXT columns without a key
// prefix length, so the unique index uses a SHA-256 hash of the complete
// property key instead.
type WebDAVProperty struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	Path      string `json:"path" gorm:"type:text"`
	Namespace string `json:"namespace" gorm:"type:text"`
	Name      string `json:"name" gorm:"type:text"`
	KeyHash   string `json:"-" gorm:"size:64;uniqueIndex:idx_webdav_property"`
	Lang      string `json:"lang"`
	InnerXML  []byte `json:"inner_xml"`
}

func (p *WebDAVProperty) BeforeSave(*gorm.DB) error {
	p.KeyHash = HashWebDAVPropertyKey(p.Path, p.Namespace, p.Name)
	return nil
}

// HashWebDAVPropertyKey returns a stable hash for the complete property key.
func HashWebDAVPropertyKey(path, namespace, name string) string {
	sum := sha256.Sum256([]byte(path + "\x00" + namespace + "\x00" + name))
	return hex.EncodeToString(sum[:])
}
