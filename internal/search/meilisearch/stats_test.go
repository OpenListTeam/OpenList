package meilisearch

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/meilisearch/meilisearch-go"
)

func TestGetIndexStats(t *testing.T) {
	for _, tc := range []struct {
		name         string
		tasks        string
		stats        string
		status       int
		wantCount    uint64
		wantIndexing bool
		wantError    bool
	}{
		{name: "committed documents", stats: `{"numberOfDocuments":42,"isIndexing":false}`, wantCount: 42},
		{name: "empty index", stats: `{"numberOfDocuments":0,"isIndexing":false}`},
		{name: "enqueued task", tasks: `{"results":[{"uid":1,"status":"enqueued"}]}`, wantIndexing: true},
		{name: "processing task", tasks: `{"results":[{"uid":1,"status":"processing"}]}`, wantIndexing: true},
		{name: "indexing started after task check", stats: `{"numberOfDocuments":20,"isIndexing":true}`, wantCount: 20, wantIndexing: true},
		{name: "stats permission denied", status: http.StatusForbidden, wantError: true},
		{name: "invalid count", stats: `{"numberOfDocuments":-1,"isIndexing":false}`, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requestCount atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestCount.Add(1)
				if r.Method != http.MethodGet {
					t.Errorf("unexpected write request: %s %s", r.Method, r.URL.Path)
				}
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/tasks":
					query := r.URL.Query()
					if query.Get("indexUids") != "library" || query.Get("limit") != "1" || query.Get("statuses") != "enqueued,processing" {
						t.Errorf("unexpected task filter: %s", r.URL.RawQuery)
					}
					if tc.tasks != "" {
						fmt.Fprint(w, tc.tasks)
					} else {
						fmt.Fprint(w, `{"results":[]}`)
					}
				case "/indexes/library/stats":
					if tc.status != 0 {
						w.WriteHeader(tc.status)
						fmt.Fprint(w, `{"message":"stats permission denied","code":"invalid_api_key","type":"auth"}`)
					} else {
						fmt.Fprint(w, tc.stats)
					}
				default:
					t.Errorf("unexpected request: %s", r.URL)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			m := &Meilisearch{Client: meilisearch.New(server.URL), IndexUid: "library"}
			stats, err := m.GetIndexStats(context.Background())
			if (err != nil) != tc.wantError {
				t.Fatalf("GetIndexStats() error = %v, want error %v", err, tc.wantError)
			}
			if err == nil && (stats.ObjectCount != tc.wantCount || stats.IsIndexing != tc.wantIndexing) {
				t.Fatalf("GetIndexStats() = %+v, want count %d, indexing %v", stats, tc.wantCount, tc.wantIndexing)
			}
			if tc.tasks != "" && requestCount.Load() != 1 {
				t.Fatalf("read statistics before pending tasks completed: %d requests", requestCount.Load())
			}
		})
	}
}

func TestGetIndexStatsChecksLocalQueue(t *testing.T) {
	for _, consuming := range []bool{false, true} {
		t.Run(fmt.Sprintf("consuming=%v", consuming), func(t *testing.T) {
			m := &Meilisearch{}
			m.taskQueue = NewTaskQueueManager(m)
			if consuming {
				m.taskQueue.consuming.Store(true)
			} else {
				m.taskQueue.Enqueue("/new-folder", nil)
			}
			// A nil client also ensures pending local work needs no remote requests.
			stats, err := m.GetIndexStats(context.Background())
			if err != nil || !stats.IsIndexing {
				t.Fatalf("GetIndexStats() = %+v, %v, want pending local work", stats, err)
			}
		})
	}
}

func TestGetIndexStatsHonorsCanceledContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("canceled request reached the server")
	}))
	defer server.Close()
	m := &Meilisearch{Client: meilisearch.New(server.URL), IndexUid: "library"}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := m.GetIndexStats(ctx); err == nil {
		t.Fatal("GetIndexStats() ignored context cancellation")
	}
}
