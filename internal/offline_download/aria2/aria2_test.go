package aria2

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/OpenListTeam/OpenList/v4/pkg/aria2/rpc"
	"github.com/gorilla/websocket"
)

func TestRemoveFollowsMetadataAndPreservesStoppedResults(t *testing.T) {
	type step struct {
		method, gid string
		result      interface{}
		fail        bool
	}
	for _, protocol := range []string{"http", "ws"} {
		for _, tc := range []struct {
			name    string
			steps   []step
			wantErr bool
		}{
			{"active", []step{{"aria2.remove", "parent", "parent", false}}, false},
			{"metadata", []step{
				{"aria2.remove", "parent", nil, true},
				{"aria2.tellStatus", "parent", map[string]interface{}{"status": "complete", "followedBy": []string{"child1", "child2"}}, false},
				{"aria2.remove", "child1", "child1", false},
				{"aria2.remove", "child2", "child2", false},
			}, false},
			{"completed", []step{
				{"aria2.remove", "parent", nil, true},
				{"aria2.tellStatus", "parent", map[string]string{"status": "complete"}, false},
			}, false},
			{"provider_error", []step{
				{"aria2.remove", "parent", nil, true},
				{"aria2.tellStatus", "parent", map[string]string{"status": "active"}, false},
			}, true},
		} {
			t.Run(protocol+"/"+tc.name, func(t *testing.T) {
				var count atomic.Int32
				serve := func(raw []byte) interface{} {
					var req struct {
						ID     uint64            `json:"id"`
						Method string            `json:"method"`
						Params []json.RawMessage `json:"params"`
					}
					if err := json.Unmarshal(raw, &req); err != nil {
						t.Error(err)
					}
					i := int(count.Add(1)) - 1
					if i >= len(tc.steps) {
						t.Errorf("extra request: %s", raw)
						return nil
					}
					want := tc.steps[i]
					var gid, token string
					if len(req.Params) < 2 {
						t.Errorf("params = %s", raw)
						return nil
					}
					_ = json.Unmarshal(req.Params[0], &token)
					_ = json.Unmarshal(req.Params[1], &gid)
					if req.Method != want.method || gid != want.gid || token != "token:secret" {
						t.Errorf("request %d = %s", i, raw)
					}
					out := map[string]interface{}{"id": req.ID, "jsonrpc": "2.0"}
					if want.fail {
						out["error"] = map[string]interface{}{"code": 1, "message": "Active Download not found"}
					} else {
						out["result"] = want.result
					}
					return out
				}
				upgrader := websocket.Upgrader{}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if protocol == "ws" {
						conn, err := upgrader.Upgrade(w, r, nil)
						if err != nil {
							t.Error(err)
							return
						}
						defer conn.Close()
						for {
							_, raw, err := conn.ReadMessage()
							if err != nil {
								return
							}
							// A notification for a task without a receiving worker must
							// allow this same connection to deliver cleanup responses.
							if err := conn.WriteJSON(map[string]interface{}{"method": "aria2.onDownloadStart", "params": []map[string]string{{"gid": "busy"}}}); err != nil {
								t.Error(err)
								return
							}
							if err := conn.WriteJSON(serve(raw)); err != nil {
								t.Error(err)
								return
							}
						}
					}
					var raw json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
						t.Error(err)
						return
					}
					_ = json.NewEncoder(w).Encode(serve(raw))
				}))
				defer server.Close()
				endpoint := server.URL
				var notifier rpc.Notifier
				if protocol == "ws" {
					endpoint = "ws" + strings.TrimPrefix(endpoint, "http")
					notifier = notify
				}
				client, err := rpc.New(context.Background(), endpoint, "secret", 5*time.Second, notifier)
				if err != nil {
					t.Fatal(err)
				}
				defer client.Close()
				for _, gid := range []string{"parent", "child1", "child2", "busy"} {
					notify.Signals.Store(gid, make(chan int))
					defer notify.Signals.Delete(gid)
				}
				parent, cancel := context.WithCancel(context.Background())
				cancel()
				cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
				defer cleanupCancel()
				task := &tool.DownloadTask{GID: "parent"}
				task.SetCtx(parent)
				err = (&Aria2{client: client}).Remove(cleanupCtx, task)
				if (err != nil) != tc.wantErr {
					t.Fatalf("Remove() = %v", err)
				}
				if int(count.Load()) != len(tc.steps) {
					t.Fatalf("requests = %d, want %d", count.Load(), len(tc.steps))
				}
				if _, ok := notify.Signals.Load("parent"); ok {
					t.Fatal("parent notification retained")
				}
				if task.Ctx().Err() != context.Canceled {
					t.Fatal("task context changed")
				}
			})
		}
	}
}
