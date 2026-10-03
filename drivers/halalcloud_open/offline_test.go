package halalcloudopen

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	sdkModel "github.com/halalcloud/golang-sdk-lite/halalcloud/model"
	sdkOffline "github.com/halalcloud/golang-sdk-lite/halalcloud/services/offline"
)

type fakeOfflineTaskService struct {
	add    func(context.Context, *sdkOffline.UserTask) (*sdkOffline.UserTask, error)
	list   func(context.Context, *sdkOffline.OfflineTaskListRequest) (*sdkOffline.OfflineTaskListResponse, error)
	delete func(context.Context, *sdkOffline.OfflineTaskDeleteRequest) (*sdkOffline.OfflineTaskDeleteResponse, error)
}

func (f *fakeOfflineTaskService) Add(ctx context.Context, req *sdkOffline.UserTask) (*sdkOffline.UserTask, error) {
	if f.add == nil {
		return nil, errors.New("unexpected Add call")
	}
	return f.add(ctx, req)
}

func (f *fakeOfflineTaskService) List(ctx context.Context, req *sdkOffline.OfflineTaskListRequest) (*sdkOffline.OfflineTaskListResponse, error) {
	if f.list == nil {
		return nil, errors.New("unexpected List call")
	}
	return f.list(ctx, req)
}

func (f *fakeOfflineTaskService) Delete(ctx context.Context, req *sdkOffline.OfflineTaskDeleteRequest) (*sdkOffline.OfflineTaskDeleteResponse, error) {
	if f.delete == nil {
		return nil, errors.New("unexpected Delete call")
	}
	return f.delete(ctx, req)
}

func TestOfflineDownload(t *testing.T) {
	var got *sdkOffline.UserTask
	driver := &HalalCloudOpen{
		offlineTaskService: &fakeOfflineTaskService{
			add: func(_ context.Context, req *sdkOffline.UserTask) (*sdkOffline.UserTask, error) {
				got = req
				return &sdkOffline.UserTask{Identity: "task-id"}, nil
			},
		},
	}
	parent := &model.Object{Path: "/downloads", IsFolder: true}

	task, err := driver.OfflineDownload(context.Background(), "  https://example.com/file  ", parent)
	if err != nil {
		t.Fatalf("OfflineDownload() error = %v", err)
	}
	if task.Identity != "task-id" {
		t.Fatalf("OfflineDownload() identity = %q, want %q", task.Identity, "task-id")
	}
	if got == nil {
		t.Fatal("OfflineDownload() did not call the SDK service")
	}
	if got.Url != "https://example.com/file" {
		t.Errorf("Add request URL = %q, want trimmed task URL", got.Url)
	}
	if got.SavePath != "/downloads" {
		t.Errorf("Add request SavePath = %q, want %q", got.SavePath, "/downloads")
	}
}

func TestOfflineDownloadRejectsEmptyURL(t *testing.T) {
	called := false
	driver := &HalalCloudOpen{
		offlineTaskService: &fakeOfflineTaskService{
			add: func(_ context.Context, _ *sdkOffline.UserTask) (*sdkOffline.UserTask, error) {
				called = true
				return nil, nil
			},
		},
	}
	parent := &model.Object{Path: "/downloads", IsFolder: true}

	_, err := driver.OfflineDownload(context.Background(), "  ", parent)
	if err == nil || !strings.Contains(err.Error(), "URL is empty") {
		t.Fatalf("OfflineDownload() error = %v, want empty URL error", err)
	}
	if called {
		t.Fatal("OfflineDownload() called the SDK service for an empty URL")
	}
}

func TestOfflineDownloadRequiresTaskIdentity(t *testing.T) {
	driver := &HalalCloudOpen{
		offlineTaskService: &fakeOfflineTaskService{
			add: func(_ context.Context, _ *sdkOffline.UserTask) (*sdkOffline.UserTask, error) {
				return &sdkOffline.UserTask{}, nil
			},
		},
	}

	_, err := driver.OfflineDownload(context.Background(), "https://example.com/file", &model.Object{Path: "/"})
	if err == nil || !strings.Contains(err.Error(), "empty task identity") {
		t.Fatalf("OfflineDownload() error = %v, want empty identity error", err)
	}
}

func TestOfflineListPaginates(t *testing.T) {
	var requests []*sdkOffline.OfflineTaskListRequest
	driver := &HalalCloudOpen{
		offlineTaskService: &fakeOfflineTaskService{
			list: func(_ context.Context, req *sdkOffline.OfflineTaskListRequest) (*sdkOffline.OfflineTaskListResponse, error) {
				requests = append(requests, req)
				if req.ListInfo.Token == "" {
					return &sdkOffline.OfflineTaskListResponse{
						Tasks:    []*sdkOffline.UserTask{{Identity: "first"}},
						ListInfo: &sdkModel.ScanListRequest{Token: "next"},
					}, nil
				}
				return &sdkOffline.OfflineTaskListResponse{
					Tasks:    []*sdkOffline.UserTask{{Identity: "second"}},
					ListInfo: &sdkModel.ScanListRequest{},
				}, nil
			},
		},
	}

	tasks, err := driver.OfflineList(context.Background())
	if err != nil {
		t.Fatalf("OfflineList() error = %v", err)
	}
	if len(tasks) != 2 || tasks[0].Identity != "first" || tasks[1].Identity != "second" {
		t.Fatalf("OfflineList() tasks = %#v, want first and second tasks", tasks)
	}
	if len(requests) != 2 {
		t.Fatalf("OfflineList() request count = %d, want 2", len(requests))
	}
	if requests[0].ListInfo.Limit != offlineTaskListPageSize || requests[0].ListInfo.Token != "" {
		t.Errorf("first List request = %#v, want initial page", requests[0].ListInfo)
	}
	if requests[1].ListInfo.Limit != offlineTaskListPageSize || requests[1].ListInfo.Token != "next" {
		t.Errorf("second List request = %#v, want next page", requests[1].ListInfo)
	}
}

func TestOfflineListRejectsRepeatedToken(t *testing.T) {
	driver := &HalalCloudOpen{
		offlineTaskService: &fakeOfflineTaskService{
			list: func(_ context.Context, req *sdkOffline.OfflineTaskListRequest) (*sdkOffline.OfflineTaskListResponse, error) {
				if req.ListInfo.Token == "" {
					return &sdkOffline.OfflineTaskListResponse{ListInfo: &sdkModel.ScanListRequest{Token: "same"}}, nil
				}
				return &sdkOffline.OfflineTaskListResponse{ListInfo: &sdkModel.ScanListRequest{Token: "same"}}, nil
			},
		},
	}

	_, err := driver.OfflineList(context.Background())
	if err == nil || !strings.Contains(err.Error(), "pagination token did not advance") {
		t.Fatalf("OfflineList() error = %v, want repeated token error", err)
	}
}

func TestDeleteOfflineTasks(t *testing.T) {
	var got *sdkOffline.OfflineTaskDeleteRequest
	driver := &HalalCloudOpen{
		offlineTaskService: &fakeOfflineTaskService{
			delete: func(_ context.Context, req *sdkOffline.OfflineTaskDeleteRequest) (*sdkOffline.OfflineTaskDeleteResponse, error) {
				got = req
				return &sdkOffline.OfflineTaskDeleteResponse{Count: 2}, nil
			},
		},
	}

	if err := driver.DeleteOfflineTasks(context.Background(), []string{" first ", "", "second"}, false); err != nil {
		t.Fatalf("DeleteOfflineTasks() error = %v", err)
	}
	if got == nil {
		t.Fatal("DeleteOfflineTasks() did not call the SDK service")
	}
	if len(got.Identity) != 2 || got.Identity[0] != "first" || got.Identity[1] != "second" {
		t.Errorf("Delete request identities = %#v, want trimmed non-empty identities", got.Identity)
	}
	if got.DeleteFiles {
		t.Error("Delete request DeleteFiles = true, want false")
	}
}

var _ offlineTaskService = (*fakeOfflineTaskService)(nil)
