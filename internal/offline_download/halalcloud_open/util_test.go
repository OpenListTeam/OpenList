package halalcloudopen

import (
	"strings"
	"testing"

	halalcloudopendriver "github.com/OpenListTeam/OpenList/v4/drivers/halalcloud_open"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	sdkOffline "github.com/halalcloud/golang-sdk-lite/halalcloud/services/offline"
)

func TestTaskCacheKeyDistinguishesBalanceMounts(t *testing.T) {
	first := &halalcloudopendriver.HalalCloudOpen{
		Storage: model.Storage{MountPath: "/downloads"},
	}
	balanced := &halalcloudopendriver.HalalCloudOpen{
		Storage: model.Storage{MountPath: "/downloads.balance"},
	}

	if taskCacheKey(first) == taskCacheKey(balanced) {
		t.Fatal("task cache key collapsed distinct balance mounts")
	}
}

func TestTaskCacheKeyIncludesEndpointAndCredentials(t *testing.T) {
	base := &halalcloudopendriver.HalalCloudOpen{
		Storage:  model.Storage{MountPath: "/downloads"},
		Addition: halalcloudopendriver.Addition{ClientID: "client-a", Host: "api.example"},
	}
	differentHost := &halalcloudopendriver.HalalCloudOpen{
		Storage:  model.Storage{MountPath: "/downloads"},
		Addition: halalcloudopendriver.Addition{ClientID: "client-a", Host: "other.example"},
	}
	differentClient := &halalcloudopendriver.HalalCloudOpen{
		Storage:  model.Storage{MountPath: "/downloads"},
		Addition: halalcloudopendriver.Addition{ClientID: "client-b", Host: "api.example"},
	}

	if taskCacheKey(base) == taskCacheKey(differentHost) {
		t.Fatal("task cache key collapsed distinct API hosts")
	}
	if taskCacheKey(base) == taskCacheKey(differentClient) {
		t.Fatal("task cache key collapsed distinct credentials")
	}
}

func TestStatusFromTask(t *testing.T) {
	tests := []struct {
		name          string
		task          *sdkOffline.UserTask
		wantProgress  float64
		wantCompleted bool
		wantError     bool
		wantStatus    string
		wantTotal     int64
	}{
		{
			name:       "waiting to add",
			task:       &sdkOffline.UserTask{Status: 0, Size: 10},
			wantStatus: "waiting to be added",
			wantTotal:  10,
		},
		{
			name:       "waiting to download",
			task:       &sdkOffline.UserTask{Status: 10, Size: 10},
			wantStatus: "waiting for download",
			wantTotal:  10,
		},
		{
			name:         "downloading status 20",
			task:         &sdkOffline.UserTask{Status: 20, Progress: 5},
			wantProgress: 5,
			wantStatus:   "downloading (5%)",
		},
		{
			name:         "downloading status 100",
			task:         &sdkOffline.UserTask{Status: 100, Progress: 42, Size: 100},
			wantProgress: 42,
			wantStatus:   "downloading (42%)",
			wantTotal:    100,
		},
		{
			name:         "downloading status 200",
			task:         &sdkOffline.UserTask{Status: 200, Progress: 50, Message: "temporary failure"},
			wantProgress: 50,
			wantStatus:   "downloading (50%)",
		},
		{
			name:         "downloading status 710",
			task:         &sdkOffline.UserTask{Status: 710, Progress: 75},
			wantProgress: 75,
			wantStatus:   "downloading (75%)",
		},
		{
			name:          "completed",
			task:          &sdkOffline.UserTask{Status: 1000, Progress: 0, BytesTotal: 200},
			wantProgress:  100,
			wantCompleted: true,
			wantStatus:    "completed",
			wantTotal:     200,
		},
		{
			name:       "negative error",
			task:       &sdkOffline.UserTask{Status: -1, Code: 7, Message: "provider rejected task"},
			wantError:  true,
			wantStatus: "provider rejected task",
		},
		{
			name:       "other non-negative status is downloading",
			task:       &sdkOffline.UserTask{Status: 1001, Code: 8},
			wantStatus: "downloading (0%)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := statusFromTask(tt.task)
			if got.Progress != tt.wantProgress {
				t.Errorf("Progress = %v, want %v", got.Progress, tt.wantProgress)
			}
			if got.Completed != tt.wantCompleted {
				t.Errorf("Completed = %v, want %v", got.Completed, tt.wantCompleted)
			}
			if (got.Err != nil) != tt.wantError {
				t.Errorf("Err = %v, wantError %v", got.Err, tt.wantError)
			}
			if got.Status != tt.wantStatus {
				t.Errorf("Status = %q, want %q", got.Status, tt.wantStatus)
			}
			if got.TotalBytes != tt.wantTotal {
				t.Errorf("TotalBytes = %d, want %d", got.TotalBytes, tt.wantTotal)
			}
			if tt.wantError && tt.task.Code != 0 && !strings.Contains(got.Err.Error(), tt.task.Message) && !strings.Contains(got.Err.Error(), "code") {
				t.Errorf("Err = %q, want provider message or code", got.Err)
			}
		})
	}
}

func TestStatusFromTaskClampsProgress(t *testing.T) {
	negative := statusFromTask(&sdkOffline.UserTask{Status: 100, Progress: -1})
	if negative.Progress != 0 {
		t.Errorf("negative Progress = %v, want 0", negative.Progress)
	}
	if negative.Status != "downloading (0%)" {
		t.Errorf("negative status text = %q, want normalized progress", negative.Status)
	}

	oversized := statusFromTask(&sdkOffline.UserTask{Status: 100, Progress: 101})
	if oversized.Progress != 100 {
		t.Errorf("oversized Progress = %v, want 100", oversized.Progress)
	}
	if oversized.Status != "downloading (100%)" {
		t.Errorf("oversized status text = %q, want normalized progress", oversized.Status)
	}
}
