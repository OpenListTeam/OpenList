package tool

import (
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/tache"
)

func TestSetFileInfoKeepsLastKnownValues(t *testing.T) {
	task := &DownloadTask{}
	task.SetFileInfo("a.txt", 10)
	task.SetFileInfo("", 20)
	if task.GetFileName() != "a.txt" || task.GetFileSize() != 20 || task.GetTotalBytes() != 20 {
		t.Fatalf("name=%q size=%d total=%d", task.GetFileName(), task.GetFileSize(), task.GetTotalBytes())
	}
	task.SetFileInfo("b.txt", 0)
	if task.GetFileName() != "b.txt" || task.GetFileSize() != 20 {
		t.Fatalf("name=%q size=%d", task.GetFileName(), task.GetFileSize())
	}
}

func TestUpdateClearsFileInfoWhenGIDChanges(t *testing.T) {
	task := &DownloadTask{
		GID: "old",
		tool: &stubTool{
			name: "aria2",
			status: &Status{
				NewGID:     "new",
				FileName:   "meta.torrent",
				TotalBytes: 42,
				Progress:   100,
				Status:     "complete",
			},
		},
	}
	task.SetFileInfo("from-url.bin", 999)
	task.SetProgress(40)

	done, err := task.Update()
	if err != nil || done {
		t.Fatalf("Update() done=%v err=%v", done, err)
	}
	if task.GID != "new" {
		t.Fatalf("GID = %s, want new", task.GID)
	}
	if task.GetFileName() != "" || task.GetFileSize() != 0 || task.GetTotalBytes() != 0 {
		t.Fatalf("stale file info name=%q size=%d total=%d", task.GetFileName(), task.GetFileSize(), task.GetTotalBytes())
	}
	if task.GetProgress() != 100 {
		t.Fatalf("progress = %v, want 100", task.GetProgress())
	}

	task.tool = &stubTool{
		name: "aria2",
		status: &Status{
			FileName:   "video.mkv",
			TotalBytes: 2048,
			Progress:   10,
			Status:     "active",
		},
	}
	done, err = task.Update()
	if err != nil || done {
		t.Fatalf("second Update() done=%v err=%v", done, err)
	}
	if task.GetFileName() != "video.mkv" || task.GetFileSize() != 2048 {
		t.Fatalf("name=%q size=%d", task.GetFileName(), task.GetFileSize())
	}
	if task.GetProgress() != 10 {
		t.Fatalf("progress = %v, want 10", task.GetProgress())
	}
}

func TestUpdateKeepsFileNameWhenStatusOmitsIt(t *testing.T) {
	task := &DownloadTask{
		tool: &stubTool{
			name: "aria2",
			status: &Status{
				FileName:   "",
				TotalBytes: 0,
				Status:     "waiting",
			},
		},
	}
	task.SetFileInfo("keep.mkv", 10)
	done, err := task.Update()
	if err != nil || done {
		t.Fatalf("Update() done=%v err=%v", done, err)
	}
	if task.GetFileName() != "keep.mkv" || task.GetFileSize() != 10 {
		t.Fatalf("name=%q size=%d", task.GetFileName(), task.GetFileSize())
	}
}

func TestDownloadTaskFileInfoJSON(t *testing.T) {
	task := &DownloadTask{
		Url:          "https://example.com/a.bin",
		DstDirPath:   "/dst",
		TempDir:      "/tmp",
		DeletePolicy: DeleteAlways,
		Toolname:     "aria2",
	}
	task.ID = "id1"
	task.State = tache.StateRunning
	task.Retry = 1
	task.MaxRetry = 3
	task.ApiUrl = "http://api"
	task.SetFileInfo("a.bin", 42)

	data, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["file_size"]; ok {
		t.Fatalf("persisted file_size, payload=%s", data)
	}
	if payload["file_name"] != "a.bin" {
		t.Fatalf("file_name = %v", payload["file_name"])
	}
	if payload["TotalBytes"] != float64(42) {
		t.Fatalf("TotalBytes = %v", payload["TotalBytes"])
	}

	var decoded DownloadTask
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.GetFileName() != "a.bin" || decoded.GetTotalBytes() != 42 {
		t.Fatalf("decoded name=%q total=%d", decoded.GetFileName(), decoded.GetTotalBytes())
	}
	if decoded.ID != "id1" || decoded.Url != task.Url || decoded.Toolname != "aria2" || decoded.DeletePolicy != DeleteAlways {
		t.Fatalf("decoded id=%s url=%s tool=%s policy=%s", decoded.ID, decoded.Url, decoded.Toolname, decoded.DeletePolicy)
	}
	if decoded.ApiUrl != "http://api" || decoded.DstDirPath != "/dst" || decoded.TempDir != "/tmp" {
		t.Fatalf("decoded paths = %s %s %s", decoded.ApiUrl, decoded.DstDirPath, decoded.TempDir)
	}
	if decoded.State != tache.StateRunning || decoded.Retry != 1 || decoded.MaxRetry != 3 {
		t.Fatalf("decoded state=%d retry=%d max=%d", decoded.State, decoded.Retry, decoded.MaxRetry)
	}

	legacy := []byte(`{"url":"https://example.com/old.bin","file_name":"old.bin","file_size":77}`)
	var fromLegacy DownloadTask
	if err := json.Unmarshal(legacy, &fromLegacy); err != nil {
		t.Fatal(err)
	}
	if fromLegacy.GetFileName() != "old.bin" || fromLegacy.GetTotalBytes() != 77 {
		t.Fatalf("legacy name=%q total=%d", fromLegacy.GetFileName(), fromLegacy.GetTotalBytes())
	}

	both := []byte(`{"file_name":"both.bin","file_size":77,"TotalBytes":10}`)
	var fromBoth DownloadTask
	if err := json.Unmarshal(both, &fromBoth); err != nil {
		t.Fatal(err)
	}
	if fromBoth.GetTotalBytes() != 10 {
		t.Fatalf("TotalBytes = %d, want the stored total instead of legacy file_size", fromBoth.GetTotalBytes())
	}
}

func TestFileInfoConcurrentAccess(t *testing.T) {
	task := &DownloadTask{}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			task.SetFileInfo(fmt.Sprintf("file-%d.txt", i), int64(i+1))
			if i%3 == 0 {
				task.clearFileInfo()
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 200; i++ {
			_ = task.GetFileName()
			_ = task.GetFileSize()
			if _, err := json.Marshal(task); err != nil {
				t.Errorf("marshal: %v", err)
				return
			}
		}
	}()
	wg.Wait()
}

type stubTool struct {
	name   string
	status *Status
}

func (s *stubTool) Name() string                       { return s.name }
func (s *stubTool) Items() []model.SettingItem         { return nil }
func (s *stubTool) Init() (string, error)              { return "", nil }
func (s *stubTool) IsReady() bool                      { return true }
func (s *stubTool) AddURL(*AddUrlArgs) (string, error) { return "", nil }
func (s *stubTool) Remove(*DownloadTask) error         { return nil }
func (s *stubTool) Status(*DownloadTask) (*Status, error) {
	return s.status, nil
}
func (s *stubTool) Run(*DownloadTask) error { return nil }
