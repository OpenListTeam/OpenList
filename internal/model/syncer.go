package model

import "github.com/OpenListTeam/tache"

const (
	Copy          string = "copy"
	Move          string = "move"
	Delete        string = "delete"
	CopyAndDelete string = "copy_and_delete"
	MoveAndDelete string = "move_and_delete"
	TwoWaySync    string = "two_way_sync"
)

type SyncerTaskArgs struct {
	ID        uint   `json:"id" gorm:"primaryKey"`
	TaskName  string `json:"task_name"`
	SrcPath   string `json:"src_path"`
	DstPath   string `json:"dst_path"`
	TaskType  string `json:"task_type" default:"copy"`
	LazyCache bool   `json:"lazy_cache" default:"true"`
}

type SyncerChildTaskInfo struct {
	TaskId     string `json:"task_id"`
	TaskType   string `json:"task_type" `
	SrcPath    string `json:"src_path"`
	DstPath    string `json:"dst_path"`
	DeletePath string `json:"delete_path"`
	State      tache.State
	Progress   float64
}
