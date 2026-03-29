package teldrive

import (
	"context"
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"golang.org/x/sync/errgroup"
	"golang.org/x/sync/semaphore"
)

type ErrResp struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type Object struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	MimeType  string    `json:"mimeType"`
	Category  string    `json:"category,omitempty"`
	ParentId  string    `json:"parentId"`
	Size      int64     `json:"size"`
	Encrypted bool      `json:"encrypted"`
	UpdatedAt time.Time `json:"updatedAt"`
}

type ListResp struct {
	Items []Object `json:"items"`
	Meta  struct {
		Count       int `json:"count"`
		TotalPages  int `json:"totalPages"`
		CurrentPage int `json:"currentPage"`
	} `json:"meta"`
}

type FilePart struct {
	Name      string `json:"name"`
	PartId    int    `json:"partId"`
	PartNo    int    `json:"partNo"`
	ChannelId int    `json:"channelId"`
	Size      int    `json:"size"`
	Encrypted bool   `json:"encrypted"`
	Salt      string `json:"salt"`
}

type chunkTask struct {
	chunkIdx  int
	fileName  string
	chunkSize int64
	reader    io.ReadSeeker
	ss        stream.StreamSectionReaderIF
}

type CopyManager struct {
	TaskChan chan CopyTask
	Sem      *semaphore.Weighted
	G        *errgroup.Group
	Ctx      context.Context
	d        *Teldrive
}

type CopyTask struct {
	SrcObj model.Obj
	DstDir model.Obj
}

type ShareObj struct {
	Id        string    `json:"id"`
	Protected bool      `json:"protected"`
	UserId    int       `json:"userId"`
	Type      string    `json:"type"`
	Name      string    `json:"name"`
	ExpiresAt time.Time `json:"expiresAt"`
}
