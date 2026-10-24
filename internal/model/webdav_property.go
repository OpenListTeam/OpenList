package model

// WebDAVProperty stores a dead WebDAV property for a resource.
//
// Path, Namespace and Name are TEXT columns so that long WebDAV paths are
// never truncated or rejected. MySQL cannot index TEXT columns without a key
// prefix length, so the unique index uses prefixes there. The prefixes sum to
// 512+128+128 = 768 characters, i.e. 3072 bytes in utf8mb4, which is the
// InnoDB (DYNAMIC/COMPRESSED row format) index key limit. SQLite and
// PostgreSQL ignore the prefix length and index the full values.
type WebDAVProperty struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	Path      string `json:"path" gorm:"type:text;uniqueIndex:idx_webdav_property,length:512"`
	Namespace string `json:"namespace" gorm:"type:text;uniqueIndex:idx_webdav_property,length:128"`
	Name      string `json:"name" gorm:"type:text;uniqueIndex:idx_webdav_property,length:128"`
	Lang      string `json:"lang"`
	InnerXML  []byte `json:"inner_xml"`
}
