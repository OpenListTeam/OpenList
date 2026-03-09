package model

type VirtualHost struct {
	ID         uint   `json:"id" gorm:"primaryKey"`
	Enabled    bool   `json:"enabled"`
	Domain     string `json:"domain" gorm:"unique" binding:"required"`
	Path       string `json:"path" binding:"required"`
	WebHosting bool   `json:"web_hosting"`
}
