package halalcloudopen

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	sdkModel "github.com/halalcloud/golang-sdk-lite/halalcloud/model"
	sdkOffline "github.com/halalcloud/golang-sdk-lite/halalcloud/services/offline"
)

const offlineTaskListPageSize int64 = 200

type offlineTaskService interface {
	Add(ctx context.Context, req *sdkOffline.UserTask) (*sdkOffline.UserTask, error)
	List(ctx context.Context, req *sdkOffline.OfflineTaskListRequest) (*sdkOffline.OfflineTaskListResponse, error)
	Delete(ctx context.Context, req *sdkOffline.OfflineTaskDeleteRequest) (*sdkOffline.OfflineTaskDeleteResponse, error)
}

func (d *HalalCloudOpen) OfflineDownload(ctx context.Context, fileURL string, parentDir model.Obj) (*sdkOffline.UserTask, error) {
	if d.offlineTaskService == nil {
		return nil, errors.New("HalalCloudOpen offline task service is not initialized")
	}

	fileURL = strings.TrimSpace(fileURL)
	if fileURL == "" {
		return nil, errors.New("HalalCloudOpen offline download URL is empty")
	}

	// The API accepts a generic task URL. Do not restrict URL schemes here; the
	// HalalCloud service is the source of truth for supported task types.
	// OpenList's Tool interface submits a whole URL and has no per-file selection
	// data, so Parse/File/IgnoreFiles belong to a future selective-download flow.
	task, err := d.offlineTaskService.Add(ctx, &sdkOffline.UserTask{
		Url:      fileURL,
		SavePath: parentDir.GetPath(),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create HalalCloudOpen offline task: %w", err)
	}
	if task == nil || strings.TrimSpace(task.Identity) == "" {
		return nil, errors.New("failed to create HalalCloudOpen offline task: empty task identity")
	}
	return task, nil
}

func (d *HalalCloudOpen) OfflineList(ctx context.Context) ([]*sdkOffline.UserTask, error) {
	if d.offlineTaskService == nil {
		return nil, errors.New("HalalCloudOpen offline task service is not initialized")
	}

	tasks := make([]*sdkOffline.UserTask, 0)
	token := ""
	seenTokens := make(map[string]struct{})
	for {
		resp, err := d.offlineTaskService.List(ctx, &sdkOffline.OfflineTaskListRequest{
			ListInfo: &sdkModel.ScanListRequest{
				Limit: offlineTaskListPageSize,
				Token: token,
			},
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list HalalCloudOpen offline tasks: %w", err)
		}
		if resp == nil {
			return nil, errors.New("failed to list HalalCloudOpen offline tasks: empty response")
		}
		tasks = append(tasks, resp.Tasks...)

		if resp.ListInfo == nil || resp.ListInfo.Token == "" {
			break
		}
		// ListInfo.Token is opaque. Guard against a malformed response repeating
		// a token so one status poll cannot loop forever.
		nextToken := resp.ListInfo.Token
		if nextToken == token {
			return nil, errors.New("failed to list HalalCloudOpen offline tasks: pagination token did not advance")
		}
		if _, ok := seenTokens[nextToken]; ok {
			return nil, errors.New("failed to list HalalCloudOpen offline tasks: pagination token repeated")
		}
		seenTokens[nextToken] = struct{}{}
		token = nextToken
	}
	return tasks, nil
}

func (d *HalalCloudOpen) DeleteOfflineTasks(ctx context.Context, taskIDs []string, deleteFiles bool) error {
	if d.offlineTaskService == nil {
		return errors.New("HalalCloudOpen offline task service is not initialized")
	}

	identities := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if taskID = strings.TrimSpace(taskID); taskID != "" {
			identities = append(identities, taskID)
		}
	}
	if len(identities) == 0 {
		return nil
	}

	_, err := d.offlineTaskService.Delete(ctx, &sdkOffline.OfflineTaskDeleteRequest{
		Identity:    identities,
		DeleteFiles: deleteFiles,
	})
	if err != nil {
		return fmt.Errorf("failed to delete HalalCloudOpen offline tasks: %w", err)
	}
	return nil
}

var _ offlineTaskService = (*sdkOffline.OfflineTaskService)(nil)
