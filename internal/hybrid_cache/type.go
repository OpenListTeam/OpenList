package hybrid_cache

import (
	"io"

	"github.com/OpenListTeam/OpenList/v4/pkg/buffer"
)

type BackingStore interface {
	buffer.Block
	io.Closer
	GrowTo(size int64) error
}
