package buffer

import (
	"io"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Block interface {
	io.ReaderAt
	io.WriterAt
	Size() int64
}

type WriteAtSeeker = model.FileWriter

type ReadAtSeeker = model.File

type SizedReadAtSeeker interface {
	ReadAtSeeker
	Size() int64
}
