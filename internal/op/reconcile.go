package op

import (
	"context"
	"strconv"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/mq"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type reconciliationTarget struct {
	storage   driver.Driver
	path      string
	recursive bool
}

type snapshotFrameKey struct{}
type mutationFrameKey struct{}

type mutationFrame struct {
	dirty   atomic.Bool
	storage driver.Driver
	targets []reconciliationTarget
}

var reconciliation atomic.Pointer[mq.LatestProcessor[string, reconciliationTarget]]

func StartSnapshotReconciliation() {
	if reconciliation.Load() != nil {
		return
	}
	reconciliation.Store(mq.NewLatestProcessor(
		256,
		256,
		2,
		func(ctx context.Context, _ string, request reconciliationTarget) {
			reconcileSnapshot(ctx, request.storage, request.path, request.recursive)
		},
	))
}

func StopSnapshotReconciliation(ctx context.Context) error {
	processor := reconciliation.Swap(nil)
	if processor == nil {
		return nil
	}
	return processor.Stop(ctx)
}

func ScheduleSnapshotReconciliation(storage driver.Driver, path string, recursive bool) {
	if !hasSnapshotProjector() {
		return
	}
	setting, _ := GetSettingItemByKey(conf.HandleHookAfterWriting)
	if setting == nil || (setting.Value != "true" && setting.Value != "1") {
		return
	}
	path = utils.FixAndCleanPath(path)
	key := Key(storage, path)
	if recursive {
		key += "\x00recursive"
	}
	processor := reconciliation.Load()
	if processor != nil {
		result := processor.Offer(key, reconciliationTarget{storage: storage, path: path, recursive: recursive}, 1)
		if result == mq.OfferRejectedCapacity {
			if count := processor.Stats().Rejected; count == 1 || count%100 == 0 {
				log.Warnf("snapshot reconciliation capacity reached for %s (rejected=%d)", key, count)
			}
		}
	}
}

func enterMutationFrame(ctx context.Context, storage driver.Driver, targets ...reconciliationTarget) (context.Context, func()) {
	if ctx.Value(mutationFrameKey{}) != nil {
		return ctx, func() {}
	}
	frame := &mutationFrame{storage: storage, targets: targets}
	framed := context.WithValue(ctx, mutationFrameKey{}, frame)
	return framed, func() {
		if frame.dirty.Load() {
			for _, target := range frame.targets {
				ScheduleSnapshotReconciliation(frame.storage, target.path, target.recursive)
			}
		}
	}
}

func enterSnapshotFrame(ctx context.Context) (context.Context, bool) {
	if ctx.Value(snapshotFrameKey{}) != nil || ctx.Value(mutationFrameKey{}) != nil {
		return ctx, false
	}
	return context.WithValue(ctx, snapshotFrameKey{}, struct{}{}), true
}

func commitNamespaceMutation(ctx context.Context, storage driver.Driver, cacheKeys []string, mutateCache func(), targets ...reconciliationTarget) {
	Cache.mutateDirectories(cacheKeys, mutateCache)
	if frame, _ := ctx.Value(mutationFrameKey{}).(*mutationFrame); frame != nil {
		frame.dirty.Store(true)
		return
	}
	for _, target := range targets {
		ScheduleSnapshotReconciliation(storage, target.path, target.recursive)
	}
}

func reconcileSnapshot(ctx context.Context, storage driver.Driver, path string, recursive bool) {
	if recursive {
		var limiter *rate.Limiter
		if item, _ := GetSettingItemByKey(conf.HandleHookRateLimit); item != nil {
			if limit, err := strconv.ParseFloat(item.Value, 64); err == nil && limit > 0 {
				limiter = rate.NewLimiter(rate.Limit(limit), 1)
			}
		}
		RecursivelyListStorage(ctx, storage, path, limiter, nil)
		return
	}
	if _, err := List(ctx, storage, path, model.ListArgs{Refresh: true}); err != nil && ctx.Err() == nil {
		log.Warnf("reconcile snapshot %s: %v", Key(storage, path), err)
	}
}
