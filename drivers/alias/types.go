package alias

import (
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
)

const (
	DisabledWP             = "disabled"
	FirstRWP               = "first"
	DeterministicWP        = "deterministic"
	DeterministicOrAllWP   = "deterministic_or_all"
	AllWP                  = "all"
	AllStrictWP            = "all_strict"
	RandomBalancedRP       = "random"
	BalancedByQuotaP       = "quota"
	BalancedByQuotaStrictP = "quota_strict"
)

var (
	ValidReadConflictPolicy  = []string{FirstRWP, RandomBalancedRP}
	ValidWriteConflictPolicy = []string{DisabledWP, FirstRWP, DeterministicWP, DeterministicOrAllWP, AllWP,
		AllStrictWP}
	ValidPutConflictPolicy = []string{DisabledWP, FirstRWP, DeterministicWP, DeterministicOrAllWP, AllWP,
		AllStrictWP, RandomBalancedRP, BalancedByQuotaP, BalancedByQuotaStrictP}
)

var (
	ErrPathConflict  = errors.New("path conflict")
	ErrSamePathLeak  = errors.New("leak some of same-name dirs")
	ErrNoEnoughSpace = errors.New("none of same-name dirs has enough space")
)

type BalancedObjs struct {
	objs         []model.Obj
	hasFailed    bool
	unmappedPath string
}

func (b *BalancedObjs) GetSize() int64 {
	return b.objs[0].GetSize()
}

func (b *BalancedObjs) ModTime() time.Time {
	return b.objs[0].ModTime()
}

func (b *BalancedObjs) CreateTime() time.Time {
	return b.objs[0].CreateTime()
}

func (b *BalancedObjs) IsDir() bool {
	return b.objs[0].IsDir()
}

func (b *BalancedObjs) GetHash() utils.HashInfo {
	return b.objs[0].GetHash()
}

func (b *BalancedObjs) GetName() string {
	return b.objs[0].GetName()
}

func (b *BalancedObjs) GetPath() string {
	return b.objs[0].GetPath()
}

func (b *BalancedObjs) GetID() string {
	return b.objs[0].GetID()
}

func (b *BalancedObjs) Unwrap() model.Obj {
	return b.objs[0]
}

var _ model.Obj = (*BalancedObjs)(nil)

type tempObj struct{ model.Object }
