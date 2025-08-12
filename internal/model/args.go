package model

import (
	"context"
	"io"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

type ListArgs struct {
	ReqPath           string
	S3ShowPlaceholder bool
	Refresh           bool
}

type LinkArgs struct {
	IP       string
	Header   http.Header
	Type     string
	Redirect bool
}

type Link struct {
	URL         string        `json:"url"`    // most common way
	Header      http.Header   `json:"header"` // needed header (for url)
	RangeReader RangeReaderIF `json:"-"`      // recommended way if can't use URL
	MFile       File          `json:"-"`      // best for local,smb... file system, which exposes MFile

	Expiration *time.Duration // local cache expire Duration

	//for accelerating request, use multi-thread downloading
	Concurrency   int   `json:"concurrency"`
	PartSize      int   `json:"part_size"`
	ContentLength int64 `json:"-"` // 转码视频、缩略图

	utils.SyncClosers `json:"-"`
}

type OtherArgs struct {
	Obj    Obj
	Method string
	Data   interface{}
}

type FsOtherArgs struct {
	Path   string      `json:"path" form:"path"`
	Method string      `json:"method" form:"method"`
	Data   interface{} `json:"data" form:"data"`
}

type ArchiveArgs struct {
	Password string
	LinkArgs
}

type ArchiveInnerArgs struct {
	ArchiveArgs
	InnerPath string
}

type ArchiveMetaArgs struct {
	ArchiveArgs
	Refresh bool
}

type ArchiveListArgs struct {
	ArchiveInnerArgs
	Refresh bool
}

type ArchiveDecompressArgs struct {
	ArchiveInnerArgs
	CacheFull     bool
	PutIntoNewDir bool
}

type RangeReaderIF interface {
	RangeRead(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error)
}

type RangeReadCloserIF interface {
	RangeReaderIF
	utils.ClosersIF
}

var _ RangeReadCloserIF = (*RangeReadCloser)(nil)

type RangeReadCloser struct {
	RangeReader RangeReaderIF
	utils.Closers
}

func (r *RangeReadCloser) RangeRead(ctx context.Context, httpRange http_range.Range) (io.ReadCloser, error) {
	rc, err := r.RangeReader.RangeRead(ctx, httpRange)
	r.Add(rc)
	return rc, err
}
