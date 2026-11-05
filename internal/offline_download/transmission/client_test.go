package transmission

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/hekmon/transmissionrpc/v3"
)

// Remove is called during cleanup after the task context has been cancelled,
// so the torrent-remove request must still be sent.
func TestRemoveWithCancelledTaskCtx(t *testing.T) {
	var removed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var req struct {
			Method    string `json:"method"`
			Tag       int    `json:"tag"`
			Arguments struct {
				IDs []int64 `json:"ids"`
			} `json:"arguments"`
		}
		_ = json.Unmarshal(body, &req)
		if req.Method == "torrent-remove" && len(req.Arguments.IDs) == 1 && req.Arguments.IDs[0] == 1 {
			removed.Add(1)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"result":"success","arguments":{},"tag":%d}`, req.Tag)
	}))
	defer srv.Close()

	endpoint, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	c, err := transmissionrpc.New(endpoint, nil)
	if err != nil {
		t.Fatal(err)
	}
	tr := &Transmission{client: c}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	task := &tool.DownloadTask{GID: "1"}
	task.SetCtx(ctx)

	if err := tr.Remove(task); err != nil {
		t.Fatalf("Remove with cancelled task ctx failed: %v", err)
	}
	if removed.Load() != 1 {
		t.Fatalf("expected 1 torrent-remove request, got %d", removed.Load())
	}
}
