package driver

import (
	"io"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// SeedHashStream is a model.FileStreamer that only carries file metadata and
// precomputed hashes; it never yields real content.
//
// It exists so that hash-driven rapid upload (秒传/CAS) implementations can reuse
// the drivers' existing Put/RapidUpload code paths, which expect a
// model.FileStreamer but only read the name/size/hash. Drivers that additionally
// need the real content (e.g. to compute a leading proof hash) should supply
// Source, which is exposed via GetReadCloser-like lazy opening.
type SeedHashStream struct {
	name     string
	size     int64
	hashInfo utils.HashInfo
	// Source lazily opens the underlying content streamer. May be nil.
	Source func() (model.FileStreamer, error)
}

var (
	_ model.FileStreamer = (*SeedHashStream)(nil)
	_ utils.ClosersIF    = (*SeedHashStream)(nil)
)

// NewSeedHashStream builds a hash-only streamer from a rapid-upload request.
func NewSeedHashStream(req *SeedRapidUploadRequest) *SeedHashStream {
	s := &SeedHashStream{name: req.Name, size: req.Size, Source: req.Open}
	if req.Whole != nil {
		s.hashInfo = *req.Whole
	}
	return s
}

func (s *SeedHashStream) GetName() string         { return s.name }
func (s *SeedHashStream) GetSize() int64          { return s.size }
func (s *SeedHashStream) GetHash() utils.HashInfo { return s.hashInfo }
func (s *SeedHashStream) GetMimetype() string     { return "" }
func (s *SeedHashStream) ModTime() time.Time      { return time.Now() }
func (s *SeedHashStream) CreateTime() time.Time   { return time.Now() }
func (s *SeedHashStream) IsDir() bool             { return false }
func (s *SeedHashStream) GetID() string           { return "" }
func (s *SeedHashStream) GetPath() string         { return "" }

func (s *SeedHashStream) NeedStore() bool            { return false }
func (s *SeedHashStream) IsForceStreamUpload() bool  { return true }
func (s *SeedHashStream) GetExist() model.Obj        { return nil }
func (s *SeedHashStream) SetExist(model.Obj)         {}
func (s *SeedHashStream) GetFile() model.File        { return nil }
func (s *SeedHashStream) Add(io.Closer)              {}
func (s *SeedHashStream) AddIfCloser(any)            {}
func (s *SeedHashStream) Close() error               { return nil }

// Read returns EOF: the stream carries hashes only, no content.
func (s *SeedHashStream) Read([]byte) (int, error) { return 0, io.EOF }

// RangeRead returns an empty reader, since no content is available.
func (s *SeedHashStream) RangeRead(http_range.Range) (io.Reader, error) {
	return nil, io.EOF
}

// CacheFullAndWriter reports that the content cannot be materialized.
func (s *SeedHashStream) CacheFullAndWriter(*model.UpdateProgress, io.Writer) (model.File, error) {
	return nil, io.EOF
}
