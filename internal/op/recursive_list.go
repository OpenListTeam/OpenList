package op

import (
	"context"
	stdpath "path"
	"sync"
	"sync/atomic"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

var (
	ManualScanCancel = atomic.Pointer[context.CancelFunc]{}
	ScannedCount     = atomic.Uint64{}
)

func ManualScanRunning() bool {
	return ManualScanCancel.Load() != nil
}

func BeginManualScan(rawPath string, limit float64) error {
	rawPath = utils.FixAndCleanPath(rawPath)
	ctx, cancel := context.WithCancel(context.Background())
	if !ManualScanCancel.CompareAndSwap(nil, &cancel) {
		cancel()
		return errors.New("manual scan is running, please try later")
	}
	ScannedCount.Store(0)
	go func() {
		defer func() { (*ManualScanCancel.Swap(nil))() }()
		err := RecursivelyList(ctx, rawPath, rate.Limit(limit), &ScannedCount)
		if err != nil {
			log.Errorf("failed recursively list: %v", err)
		}
	}()
	return nil
}

func StopManualScan() {
	c := ManualScanCancel.Load()
	if c != nil {
		(*c)()
	}
}

func RecursivelyList(ctx context.Context, rawPath string, limit rate.Limit, counter *atomic.Uint64) error {
	storage, actualPath, err := GetStorageAndActualPath(rawPath)
	if err != nil && !errors.Is(err, errs.StorageNotFound) {
		return err
	} else if err == nil {
		var limiter *rate.Limiter
		if limit > .0 {
			limiter = rate.NewLimiter(limit, 1)
		}
		RecursivelyListStorage(ctx, storage, actualPath, limiter, counter)
	} else {
		var wg sync.WaitGroup
		recursivelyListVirtual(ctx, rawPath, limit, counter, &wg)
		wg.Wait()
	}
	return nil
}

func recursivelyListVirtual(ctx context.Context, rawPath string, limit rate.Limit, counter *atomic.Uint64, wg *sync.WaitGroup) {
	objs := GetStorageVirtualFilesByPath(rawPath)
	if counter != nil {
		counter.Add(uint64(len(objs)))
	}
	for _, obj := range objs {
		if utils.IsCanceled(ctx) {
			return
		}
		nextPath := stdpath.Join(rawPath, obj.GetName())
		storage, actualPath, err := GetStorageAndActualPath(nextPath)
		if err != nil && !errors.Is(err, errs.StorageNotFound) {
			log.Errorf("error recursively list: failed get storage [%s]: %v", nextPath, err)
		} else if err == nil {
			var limiter *rate.Limiter
			if limit > .0 {
				limiter = rate.NewLimiter(limit, 1)
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				RecursivelyListStorage(ctx, storage, actualPath, limiter, counter)
			}()
		} else {
			recursivelyListVirtual(ctx, nextPath, limit, counter, wg)
		}
	}
}

func RecursivelyListStorage(ctx context.Context, storage driver.Driver, actualPath string, limiter *rate.Limiter, counter *atomic.Uint64) {
	objs, err := List(ctx, storage, actualPath, model.ListArgs{Refresh: true})
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			log.Errorf("error recursively list: failed list (%s)[%s]: %v", storage.GetStorage().MountPath, actualPath, err)
		}
		return
	}
	if counter != nil {
		counter.Add(uint64(len(objs)))
	}
	for _, obj := range objs {
		if utils.IsCanceled(ctx) {
			return
		}
		if !obj.IsDir() {
			continue
		}
		if limiter != nil {
			if err = limiter.Wait(ctx); err != nil {
				return
			}
		}
		nextPath := stdpath.Join(actualPath, obj.GetName())
		RecursivelyListStorage(ctx, storage, nextPath, limiter, counter)
	}
}
