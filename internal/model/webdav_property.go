package model

// WebDAVProperty stores a dead WebDAV property for a resource.
type WebDAVProperty struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	Path      string `json:"path" gorm:"uniqueIndex:idx_webdav_property"`
	Namespace string `json:"namespace" gorm:"uniqueIndex:idx_webdav_property"`
	Name      string `json:"name" gorm:"uniqueIndex:idx_webdav_property"`
	Lang      string `json:"lang"`
	InnerXML  []byte `json:"inner_xml"`
}
