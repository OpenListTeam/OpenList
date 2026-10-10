package op

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const lastDoneExpireThreshold = 86400

type storageDetailsState struct {
	generation uint64
	lastDone   int64
	refreshing bool
}

// A changing driver is identified by the object, since reinitialization may
// mutate its mount path. Failed or retired objects remain unavailable until a
// successful initialization; their capacity must not be published again.
type storageDetailsChange struct {
	paths  []string
	active bool
}

var (
	detailsG       singleflight.Group[*model.StorageDetails]
	detailsLock    sync.Mutex
	detailsStates  = make(map[string]*storageDetailsState)
	detailsChanges = make(map[driver.Driver]*storageDetailsChange)
)

func storageDetailsStateLocked(key string) *storageDetailsState {
	state := detailsStates[key]
	if state == nil {
		state = &storageDetailsState{}
		detailsStates[key] = state
	}
	return state
}

func invalidateStorageDetailsLocked(key string) {
	state := storageDetailsStateLocked(key)
	state.generation++
	state.lastDone = 0
	state.refreshing = false
	Cache.detailCache.Delete(key)
}

// Invalidation preserves the generation even for a deleted mount, so a late
// generation-zero probe cannot resurrect its cache or cooldown.
func InvalidateStorageDetailsState(mountPath string) {
	detailsLock.Lock()
	defer detailsLock.Unlock()
	invalidateStorageDetailsLocked(utils.FixAndCleanPath(mountPath))
	cleanExpiredLastDoneTimesLocked(time.Now().Unix())
}

func cleanExpiredLastDoneTimesLocked(now int64) {
	for _, state := range detailsStates {
		if now-state.lastDone > lastDoneExpireThreshold {
			state.lastDone = 0
		}
	}
}

func beginStorageDetailsChange(storage driver.Driver, paths ...string) (*storageDetailsChange, error) {
	detailsLock.Lock()
	defer detailsLock.Unlock()
	if previous := detailsChanges[storage]; previous != nil && previous.active {
		return nil, errors.WithMessage(errs.StorageNotInit, "storage is being reinitialized")
	}
	change := &storageDetailsChange{active: true}
	for _, path := range paths {
		key := utils.FixAndCleanPath(path)
		if utils.SliceContains(change.paths, key) {
			continue
		}
		change.paths = append(change.paths, key)
		invalidateStorageDetailsLocked(key)
	}
	detailsChanges[storage] = change
	return change, nil
}

func finishStorageDetailsChange(storage driver.Driver, change *storageDetailsChange, ready bool) {
	detailsLock.Lock()
	defer detailsLock.Unlock()
	if detailsChanges[storage] != change {
		return
	}
	for _, key := range change.paths {
		invalidateStorageDetailsLocked(key)
	}
	change.active = false
	if ready {
		delete(detailsChanges, storage)
	}
}

type storageDetailsSnapshot struct {
	key        string
	generation uint64
	ttl        time.Duration
	cache      bool
}

// Admission and DoChan registration share one short critical section. No
// caller can reserve a generation and register its flight after a later one
// has already completed. The driver call itself never holds detailsLock.
func prepareStorageDetails(ctx context.Context, storage driver.Driver, refresh bool, cooldownSec int, timeout time.Duration) (<-chan singleflight.Result[*model.StorageDetails], *model.StorageDetails, error) {
	detailsLock.Lock()
	defer detailsLock.Unlock()
	if detailsChanges[storage] != nil {
		return nil, nil, errors.WithMessage(errs.StorageNotInit, "storage details unavailable during or after an unsuccessful storage change")
	}
	config := storage.Config()
	meta := storage.GetStorage()
	if config.CheckStatus && meta.Status != WORK {
		return nil, nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", meta.Status)
	}
	wd, ok := storage.(driver.WithDetails)
	if !ok {
		return nil, nil, errs.NotImplement
	}
	snapshot := storageDetailsSnapshot{
		key:   utils.FixAndCleanPath(meta.MountPath),
		ttl:   time.Minute * time.Duration(meta.CacheExpiration),
		cache: !config.NoCache,
	}
	state := storageDetailsStateLocked(snapshot.key)
	if refresh && !state.refreshing && (cooldownSec <= 0 || time.Now().Unix()-state.lastDone >= int64(cooldownSec)) {
		invalidateStorageDetailsLocked(snapshot.key)
		state.refreshing = true
	} else if cached, exists := Cache.detailCache.Get(snapshot.key); exists && snapshot.cache {
		return nil, cached, nil
	}
	snapshot.generation = state.generation
	flightKey := fmt.Sprintf("%s:%d", snapshot.key, snapshot.generation)
	result := detailsG.DoChan(flightKey, func() (*model.StorageDetails, error) {
		return probeStorageDetails(ctx, wd, snapshot, timeout)
	})
	return result, nil, nil
}

func probeStorageDetails(ctx context.Context, storage driver.WithDetails, snapshot storageDetailsSnapshot, timeout time.Duration) (ret *model.StorageDetails, err error) {
	probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	defer func() {
		if recovered := recover(); recovered != nil {
			ret = nil
			err = fmt.Errorf("panic in driver GetDetails: %v", recovered)
			log.Errorf("panic recovered in driver GetDetails for %s: %v", snapshot.key, recovered)
		}
		detailsLock.Lock()
		defer detailsLock.Unlock()
		state := storageDetailsStateLocked(snapshot.key)
		if state.generation != snapshot.generation {
			log.Debugf("discarding stale storage details for %s: generation %d was superseded by %d", snapshot.key, snapshot.generation, state.generation)
			return
		}
		state.refreshing = false
		if err == nil {
			if snapshot.cache {
				Cache.detailCache.SetWithTTL(snapshot.key, ret, snapshot.ttl)
			}
			state.lastDone = time.Now().Unix()
			if len(detailsStates) > 256 {
				cleanExpiredLastDoneTimesLocked(state.lastDone)
			}
		}
	}()
	ret, err = storage.GetDetails(probeCtx)
	if err != nil {
		ret = nil
	} else if probeCtx.Err() != nil {
		// Do not publish a nominal success that arrives after the probe deadline.
		ret, err = nil, probeCtx.Err()
	}
	return ret, err
}

func GetStorageDetails(ctx context.Context, storage driver.Driver, refresh ...bool) (*model.StorageDetails, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cooldownSec := GetSettingInt(conf.StorageDetailsCooldownSeconds, 0)
	timeout := time.Duration(GetSettingInt(conf.StorageDetailsTimeoutSeconds, 15)) * time.Second
	result, cached, err := prepareStorageDetails(ctx, storage, utils.IsBool(refresh...), cooldownSec, timeout)
	if err != nil || result == nil {
		return cached, err
	}
	select {
	case ret := <-result:
		return ret.Val, ret.Err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
