package alloc_test // import "github.com/ncruces/go-sqlite3/internal/alloc"

import (
	"math"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/alloc"
)

func TestVirtual(t *testing.T) {
	defer func() { _ = recover() }()
	alloc.NewMemory(math.MaxInt+2, math.MaxInt+2)
	t.Error("want panic")
}
