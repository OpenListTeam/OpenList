package alias

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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

type BalancedObj struct {
	model.Obj
	ExactReqPath string
}

func (b *BalancedObj) Unwrap() model.Obj {
	return b.Obj
}

func GetExactReqPath(obj model.Obj) string {
	if b, ok := obj.(*BalancedObj); ok {
		return b.ExactReqPath
	}
	if unwrap, ok := obj.(model.ObjUnwrap); ok {
		return GetExactReqPath(unwrap.Unwrap())
	}
	return ""
}
