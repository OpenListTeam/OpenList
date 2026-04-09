package model

type Meta struct {
	ID            uint   `json:"id" gorm:"primaryKey"`
	Path          string `json:"path" gorm:"unique" binding:"required"`
	ReadUsers     []uint `json:"read_users" gorm:"serializer:json"`
	ReadUsersSub  bool   `json:"read_users_sub"`
	WriteUsers    []uint `json:"write_users" gorm:"serializer:json"`
	WriteUsersSub bool   `json:"write_users_sub"`
	Password      string `json:"password"`
	PSub          bool   `json:"p_sub"`
	Write         bool   `json:"write"`
	WSub          bool   `json:"w_sub"`
	Hide          string `json:"hide"`
	HSub          bool   `json:"h_sub"`
	Readme        string `json:"readme"`
	RSub          bool   `json:"r_sub"`
	Header        string `json:"header"`
	HeaderSub     bool   `json:"header_sub"`
}
