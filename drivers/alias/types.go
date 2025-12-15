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

type BalancedObjs []model.Obj

func (b BalancedObjs) GetSize() int64 {
	return b[0].GetSize()
}

func (b BalancedObjs) ModTime() time.Time {
	return b[0].ModTime()
}

func (b BalancedObjs) CreateTime() time.Time {
	return b[0].CreateTime()
}

func (b BalancedObjs) IsDir() bool {
	return b[0].IsDir()
}

func (b BalancedObjs) GetHash() utils.HashInfo {
	return b[0].GetHash()
}

func (b BalancedObjs) GetName() string {
	return b[0].GetName()
}

func (b BalancedObjs) GetPath() string {
	return b[0].GetPath()
}

func (b BalancedObjs) GetID() string {
	return b[0].GetID()
}

func (b BalancedObjs) Unwrap() model.Obj {
	return b[0]
}

var _ model.Obj = (BalancedObjs)(nil)

type tempObj struct{ model.Object }
