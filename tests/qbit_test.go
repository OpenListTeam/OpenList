package tests

import (
	"testing"

	qbit "github.com/OpenListTeam/OpenList/v4/internal/offline_download/qbit"
	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/pkg/qbittorrent"
)

type statusClient struct {
	qbittorrent.Client
	info qbittorrent.TorrentInfo
}

func (c *statusClient) GetInfo(string) (qbittorrent.TorrentInfo, error) {
	return c.info, nil
}

func TestQbittorrentStatusUsesSavePath(t *testing.T) {
	client := &statusClient{info: qbittorrent.TorrentInfo{
		SavePath:  "/downloads/existing-task",
		Size:      42,
		Completed: 42,
		State:     qbittorrent.UPLOADING,
	}}
	qbitTool := qbit.New(client)
	task := &tool.DownloadTask{TempDir: "/downloads/new-task"}

	status, err := qbitTool.Status(task)
	if err != nil {
		t.Fatal(err)
	}
	if task.TempDir != client.info.SavePath {
		t.Fatalf("TempDir = %q, want %q", task.TempDir, client.info.SavePath)
	}
	if !status.Completed {
		t.Fatal("completed torrent was not reported as completed")
	}
}
