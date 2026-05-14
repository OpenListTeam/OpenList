package model

import "time"

type SharingDB struct {
	ID          string     `json:"id" gorm:"type:char(12);primaryKey"`
	FilesRaw    string     `json:"-" gorm:"type:text"`
	Expires     *time.Time `json:"expires"`
	Pwd         string     `json:"pwd"`
	Accessed    int        `json:"accessed"`
	MaxAccessed int        `json:"max_accessed"`
	CreatorId   uint       `json:"-"`
	Disabled    bool       `json:"disabled"`
	Remark      string     `json:"remark"`
	Readme      string     `json:"readme" gorm:"type:text"`
	Header      string     `json:"header" gorm:"type:text"`
	// Domain 绑定的域名，可为空；非空时该条记录额外作为虚拟主机参与 Host 匹配（与旧 VirtualHost.Domain 等价）。
	Domain string `json:"domain" gorm:"uniqueIndex"`
	// WebHosting 仅在 Domain 非空时有效；为 true 时启用 Web 托管模式（直接响应文件内容），为 false 时仅做路径重映射。
	WebHosting bool `json:"web_hosting"`
	Sort
}

type Sharing struct {
	*SharingDB
	Files   []string `json:"files"`
	Creator *User    `json:"-"`
}

func (s *Sharing) Valid() bool {
	if s.Disabled {
		return false
	}
	if s.MaxAccessed > 0 && s.Accessed >= s.MaxAccessed {
		return false
	}
	if len(s.Files) == 0 {
		return false
	}
	if s.Creator == nil || !s.Creator.CanShare() {
		return false
	}
	if s.Expires != nil && !s.Expires.IsZero() && s.Expires.Before(time.Now()) {
		return false
	}
	return true
}

func (s *Sharing) Verify(pwd string) bool {
	return s.Pwd == "" || s.Pwd == pwd
}
