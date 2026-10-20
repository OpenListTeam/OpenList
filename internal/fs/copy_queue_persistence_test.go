package fs

import (
	"encoding/json"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/tache"
)

func TestMigratedCopyTaskRecoversNativeFields(t *testing.T) {
	previousConf := conf.Conf
	conf.Conf = &conf.Config{}
	t.Cleanup(func() { conf.Conf = previousConf })

	raw := []byte(`{
		"id":"task-one",
		"state":0,
		"retry":0,
		"max_retry":0,
		"Creator":{"id":1,"username":"admin","password":"","base_path":"/","role":2,"disabled":false,"permission":511,"sso_id":"","allow_ldap":true},
		"start_time":"2026-08-11T01:00:00Z",
		"end_time":"2026-08-11T01:01:00Z",
		"TotalBytes":42,
		"ApiUrl":"http://openlist.test:5244",
		"src_path":"/folder/file",
		"dst_path":"/backup",
		"src_storage_mp":"/source",
		"dst_storage_mp":"/target",
		"TaskType":0
	}`)

	var task FileTransferTask
	if err := json.Unmarshal(raw, &task); err != nil {
		t.Fatalf("unmarshal migrated task: %v", err)
	}
	if task.GetID() != "task-one" || task.GetState() != tache.StatePending {
		t.Fatalf("unexpected base fields: id=%q state=%d", task.GetID(), task.GetState())
	}
	if task.GetCreator() == nil || task.GetCreator().Username != "admin" {
		t.Fatal("creator was not recovered")
	}
	if task.GetStartTime() == nil || task.GetEndTime() == nil {
		t.Fatal("task timestamps were not recovered")
	}
	if task.TaskType != copy {
		t.Fatalf("unexpected task type: %d", task.TaskType)
	}
	if task.SrcActualPath != "/folder/file" || task.DstActualPath != "/backup" {
		t.Fatal("copy paths were not recovered")
	}

	_, maxRetry := task.GetRetry()
	if maxRetry != 0 {
		t.Fatalf("migration must defer retry initialization, got %d", maxRetry)
	}
	task.SetRetry(0, 2)
	_, maxRetry = task.GetRetry()
	if maxRetry != 2 {
		t.Fatalf("retry initialization failed, got %d", maxRetry)
	}
	if task.groupID != "/target/backup" {
		t.Fatalf("task group was not rebuilt: %q", task.groupID)
	}
}
