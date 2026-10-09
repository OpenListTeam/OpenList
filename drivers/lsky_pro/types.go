package lsky_pro

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// Resp is the common response envelope of the Lsky Pro API.
type Resp[T any] struct {
	Status  bool   `json:"status"`
	Message string `json:"message"`
	Data    T      `json:"data"`
}

type Page[T any] struct {
	CurrentPage int `json:"current_page"`
	LastPage    int `json:"last_page"`
	Data        []T `json:"data"`
}

type Album struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Intro    string `json:"intro"`
	ImageNum int    `json:"image_num"`
}

type Links struct {
	URL          string `json:"url"`
	ThumbnailURL string `json:"thumbnail_url"`
}

type Image struct {
	Key        string  `json:"key"`
	Name       string  `json:"name"`
	OriginName string  `json:"origin_name"`
	Size       float64 `json:"size"` // KB
	Mimetype   string  `json:"mimetype"`
	MD5        string  `json:"md5"`
	SHA1       string  `json:"sha1"`
	Date       string  `json:"date"`
	Links      Links   `json:"links"`
}

type TokenData struct {
	Token string `json:"token"`
}

// imageObj carries the public url returned by the list/upload API.
type imageObj struct {
	model.ObjThumb
	url string
}

func (a Album) toObj() model.Obj {
	return &model.Object{
		ID:       itoa(a.ID),
		Name:     a.Name,
		IsFolder: true,
		Modified: time.Now(),
	}
}

func (i Image) toObj() model.Obj {
	name := i.OriginName
	if name == "" {
		name = i.Name
	}
	t, err := time.ParseInLocation("2006-01-02 15:04:05", i.Date, time.Local)
	if err != nil {
		t = time.Now()
	}
	return &imageObj{
		ObjThumb: model.ObjThumb{
			Object: model.Object{
				ID:       i.Key,
				Name:     name,
				Size:     int64(i.Size * 1024),
				Modified: t,
				Ctime:    t,
			},
			Thumbnail: model.Thumbnail{Thumbnail: i.Links.ThumbnailURL},
		},
		url: i.Links.URL,
	}
}
