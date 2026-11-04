package op

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
)

const (
	defaultDetailsTimeoutSec = 15
	maxDetailsTimeoutSec     = 600
)

// detailsState belongs to one mount path. Its generation is drawn from a
// process-wide sequence, and the state is deleted whenever the mount changes,
// so a probe started before a change can never publish into the cache after
// it: its generation no longer matches (or its state no longer exists).
type detailsState struct {
	generation uint64
	failed     bool
}

// A task owns the mount's slot until GetDetails actually exits or a lifecycle
// change invalidates the mount. Its result is immutable once done is closed.
type detailsTask struct {
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}
	storage    driver.Driver
	key        string
	generation uint64
	val        *model.StorageDetails
	err        error
}

var (
	detailsTasks    = make(map[string]*detailsTask)
	detailsLock     sync.Mutex
	detailsStates   = make(map[string]*detailsState)
	detailsChanging = make(map[string]int) // mount path -> active lifecycle changes
	detailsSeq      uint64
)

func invalidateStorageDetailsLocked(key string) {
	if task := detailsTasks[key]; task != nil {
		task.cancel()
		delete(detailsTasks, key)
	}
	delete(detailsStates, key)
	Cache.detailCache.Delete(key)
}

// beginStorageDetailsChange marks mount paths as changing (init, update,
// disable, delete) and drops their cached capacity. While a path is changing
// no probe is admitted for it. Call the returned function when the change is
// done; it drops anything cached in between. Changes may nest.
func beginStorageDetailsChange(mountPaths ...string) (end func()) {
	keys := make([]string, 0, len(mountPaths))
	for _, p := range mountPaths {
		if key := utils.FixAndCleanPath(p); !utils.SliceContains(keys, key) {
			keys = append(keys, key)
		}
	}
	detailsLock.Lock()
	for _, key := range keys {
		detailsChanging[key]++
		invalidateStorageDetailsLocked(key)
	}
	detailsLock.Unlock()
	return func() {
		detailsLock.Lock()
		defer detailsLock.Unlock()
		for _, key := range keys {
			if detailsChanging[key]--; detailsChanging[key] <= 0 {
				delete(detailsChanging, key)
			}
			invalidateStorageDetailsLocked(key)
		}
	}
}

// Invalid settings must not turn the independent task deadline off.
func storageDetailsSettings() time.Duration {
	timeoutSec := getSettingInt(conf.StorageDetailsTimeoutSeconds, defaultDetailsTimeoutSec)
	if timeoutSec <= 0 {
		timeoutSec = defaultDetailsTimeoutSec
	}
	timeoutSec = min(timeoutSec, maxDetailsTimeoutSec)
	return time.Duration(timeoutSec) * time.Second
}

func getSettingInt(key string, defaultValue int) int {
	if item, err := GetSettingItemByKey(key); err == nil && item != nil {
		if val, err := strconv.Atoi(item.Value); err == nil {
			return val
		}
	}
	return defaultValue
}

// Cache decisions and task admission use the same lock as completion. Force
// refresh deletes the cache but joins any actual call still in progress.
func prepareStorageDetails(ctx context.Context, storage driver.Driver, refresh bool, timeout time.Duration) (*detailsTask, *model.StorageDetails, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if storage == nil {
		return nil, nil, errs.StorageNotInit
	}
	meta := storage.GetStorage()
	if meta == nil {
		return nil, nil, errs.StorageNotInit
	}
	if storage.Config().CheckStatus && meta.Status != WORK {
		return nil, nil, errors.WithMessagef(errs.StorageNotInit, "storage status: %s", meta.Status)
	}
	wd, ok := storage.(driver.WithDetails)
	if !ok {
		return nil, nil, errs.NotImplement
	}
	key := utils.FixAndCleanPath(meta.MountPath)
	cache := !storage.Config().NoCache
	ttl := time.Minute * time.Duration(meta.CacheExpiration)

	detailsLock.Lock()
	defer detailsLock.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if detailsChanging[key] > 0 {
		return nil, nil, errors.WithMessage(errs.StorageNotInit, "storage is being changed")
	}
	// A driver that was disabled, deleted or replaced may still be held by a
	// caller; it must not report (or cache) capacity for the mount path.
	if registered, ok := storagesMap.Load(key); !ok || registered != storage {
		return nil, nil, errors.WithMessage(errs.StorageNotInit, "storage is not mounted")
	}
	state := detailsStates[key]
	if refresh {
		Cache.InvalidateStorageDetails(storage)
		if state != nil {
			state.failed = false
		}
	} else if cache {
		if cached, ok := Cache.GetStorageDetails(storage); ok && cached != nil {
			return nil, cached, nil
		}
	}
	if task := detailsTasks[key]; task != nil {
		if task.validLocked() {
			return task, nil, nil
		}
		task.cancel()
		delete(detailsTasks, key)
	}
	if !refresh && cache && state != nil && state.failed {
		return nil, nil, errs.StorageNotInit
	}
	if state == nil {
		detailsSeq++
		state = &detailsState{generation: detailsSeq}
		detailsStates[key] = state
	}
	taskCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	task := &detailsTask{
		ctx: taskCtx, cancel: cancel, done: make(chan struct{}), storage: storage,
		key: key, generation: state.generation,
	}
	detailsTasks[key] = task
	go task.run(wd, cache, ttl)
	return task, nil, nil
}

// Caller holds detailsLock; no driver methods or I/O run under this lock.
func (task *detailsTask) validLocked() bool {
	state := detailsStates[task.key]
	registered, ok := storagesMap.Load(task.key)
	return state != nil && state.generation == task.generation && ok && registered == task.storage
}

func (task *detailsTask) run(wd driver.WithDetails, cache bool, ttl time.Duration) {
	defer task.cancel()
	ret, err := callStorageDetails(task.ctx, wd, task.key)
	detailsLock.Lock()
	defer detailsLock.Unlock()
	// Check the deadline itself as well as Err: the timer callback may not
	// have run yet when a late driver return competes for this lock.
	deadline, _ := task.ctx.Deadline()
	if !time.Now().Before(deadline) {
		err = context.DeadlineExceeded
	}
	if !task.validLocked() {
		err = errs.StorageNotInit
	}
	if err != nil {
		ret = nil
	}
	if cache && task.validLocked() {
		if ret == nil {
			detailsStates[task.key].failed = true
		} else {
			detailsStates[task.key].failed = false
			Cache.detailCache.SetWithTTL(task.key, ret, ttl)
		}
	}
	task.val, task.err = ret, err
	if detailsTasks[task.key] == task {
		delete(detailsTasks, task.key)
	}
	// Publishing, freeing the slot and notifying waiters are one boundary.
	close(task.done)
}

func callStorageDetails(ctx context.Context, wd driver.WithDetails, key string) (val *model.StorageDetails, err error) {
	defer func() {
		if r := recover(); r != nil {
			log.Errorf("panic in GetDetails of %s: %v", key, r)
			val, err = nil, fmt.Errorf("panic in GetDetails: %v", r)
		}
	}()
	return wd.GetDetails(ctx)
}

func (task *detailsTask) wait(ctx context.Context) (*model.StorageDetails, error) {
	select {
	case <-task.done:
	case <-task.ctx.Done():
	case <-ctx.Done():
	}
	detailsLock.Lock()
	defer detailsLock.Unlock()
	if !task.validLocked() {
		return nil, errs.StorageNotInit
	}
	// Prefer an already published result (including NoCache) even if the
	// waiting budget and completion became ready at the same time.
	select {
	case <-task.done:
		return task.val, task.err
	default:
	}
	if err := task.ctx.Err(); err != nil {
		return nil, err
	}
	return nil, ctx.Err()
}

func GetStorageDetails(ctx context.Context, storage driver.Driver, refresh ...bool) (*model.StorageDetails, error) {
	task, cached, err := prepareStorageDetails(ctx, storage, utils.IsBool(refresh...), storageDetailsSettings())
	if err != nil || task == nil {
		return cached, err
	}
	return task.wait(ctx)
}
