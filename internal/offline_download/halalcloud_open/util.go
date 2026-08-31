package halalcloudopen

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	halalcloudopendriver "github.com/OpenListTeam/OpenList/v4/drivers/halalcloud_open"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/pkg/singleflight"
	"github.com/OpenListTeam/go-cache"
	sdkOffline "github.com/halalcloud/golang-sdk-lite/halalcloud/services/offline"
)

// HalalCloud offline task status contract:
//   - 0: waiting to be added
//   - 10: waiting for download
//   - 1000: completed
//   - negative: failed
//   - every other non-negative value: downloading
const (
	offlineStatusWaitingToAdd      = 0
	offlineStatusWaitingToDownload = 10
	offlineStatusComplete          = 1000
	statusCacheExpiration          = 10 * time.Second
)

// The manager polls every three seconds and may track several tasks from the
// same account. Cache one paginated list briefly so those tasks share a single
// provider request.
var taskCache = cache.NewMemCache(cache.WithShards[[]*sdkOffline.UserTask](16))
var taskGroup singleflight.Group[[]*sdkOffline.UserTask]

func taskCacheKey(driver *halalcloudopendriver.HalalCloudOpen) string {
	return op.Key(driver, "/v6/offline_task/list")
}

func (h *HalalCloudOpen) getTasks(driver *halalcloudopendriver.HalalCloudOpen) ([]*sdkOffline.UserTask, error) {
	key := taskCacheKey(driver)
	if tasks, ok := taskCache.Get(key); ok {
		return tasks, nil
	}

	tasks, err, _ := taskGroup.Do(key, func() ([]*sdkOffline.UserTask, error) {
		// The SDK timeout bounds this request shared across task waiters.
		tasks, err := driver.OfflineList(context.Background())
		if err != nil {
			return nil, err
		}
		taskCache.Set(key, tasks, cache.WithEx[[]*sdkOffline.UserTask](statusCacheExpiration))
		return tasks, nil
	})
	return tasks, err
}

func (*HalalCloudOpen) invalidateTaskCache(driver *halalcloudopendriver.HalalCloudOpen) {
	taskCache.Del(taskCacheKey(driver))
}

// statusFromTask translates the provider's numeric contract into the generic
// status shape consumed by OpenList's download-task manager.
func statusFromTask(task *sdkOffline.UserTask) *tool.Status {
	// Some List responses omit Size while still returning BytesTotal.
	totalBytes := task.Size
	if totalBytes <= 0 {
		totalBytes = task.BytesTotal
	}

	progress := float64(task.Progress)
	if task.Status == offlineStatusComplete {
		progress = 100
	} else {
		if progress < 0 {
			progress = 0
		}
		if progress > 100 {
			progress = 100
		}
	}

	status := &tool.Status{
		TotalBytes: totalBytes,
		Progress:   progress,
		Completed:  task.Status == offlineStatusComplete,
		Status:     taskStatusText(task, progress),
	}
	if task.Status < 0 {
		status.Err = taskStatusError(task)
	}
	return status
}

func taskStatusText(task *sdkOffline.UserTask, normalizedProgress float64) string {
	message := strings.TrimSpace(task.Message)
	switch {
	case task.Status == offlineStatusComplete:
		return "completed"
	case task.Status < 0:
		if message != "" {
			return message
		}
		return fmt.Sprintf("error (status %d)", task.Status)
	case task.Status == offlineStatusWaitingToAdd:
		return "waiting to be added"
	case task.Status == offlineStatusWaitingToDownload:
		return "waiting for download"
	default:
		// All other non-negative statuses represent downloading.
		return fmt.Sprintf("downloading (%.0f%%)", normalizedProgress)
	}
}

func taskStatusError(task *sdkOffline.UserTask) error {
	message := strings.TrimSpace(task.Message)
	if message != "" {
		return errors.New(message)
	}
	if task.Code != 0 {
		return fmt.Errorf("HalalCloudOpen offline task failed with status %d and code %d", task.Status, task.Code)
	}
	return fmt.Errorf("HalalCloudOpen offline task failed with status %d", task.Status)
}
