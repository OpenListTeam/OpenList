package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search/searcher"
)

type statsSearchStub struct {
	filteredSearchStub
	stats searcher.IndexStats
	err   error
	calls int
}

func (s *statsSearchStub) GetIndexStats(context.Context) (searcher.IndexStats, error) {
	s.calls++
	return s.stats, s.err
}

func TestRefreshProgressStats(t *testing.T) {
	for _, tc := range []struct {
		name      string
		building  bool
		stats     searcher.IndexStats
		err       error
		wantCount uint64
		wantDone  bool
		wantCalls int
	}{
		{name: "updated total", stats: searcher.IndexStats{ObjectCount: 12}, wantCount: 12, wantDone: true, wantCalls: 1},
		{name: "all documents removed", wantCount: 0, wantDone: true, wantCalls: 1},
		{name: "asynchronous update", stats: searcher.IndexStats{ObjectCount: 5, IsIndexing: true}, wantCount: 8, wantCalls: 1},
		{name: "statistics unavailable", err: errors.New("stats access denied"), wantCount: 8, wantDone: true, wantCalls: 1},
		{name: "full build in progress", building: true, wantCount: 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			completedAt := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			progress := &model.IndexProgress{ObjCount: 8, IsDone: !tc.building, LastDoneTime: &completedAt, Error: "saved build error"}
			backend := &statsSearchStub{stats: tc.stats, err: tc.err}
			refreshProgressStats(context.Background(), progress, backend)
			if progress.ObjCount != tc.wantCount || progress.IsDone != tc.wantDone || backend.calls != tc.wantCalls {
				t.Fatalf("progress = %+v, calls = %d; want count %d, done %v, calls %d", progress, backend.calls, tc.wantCount, tc.wantDone, tc.wantCalls)
			}
			if progress.LastDoneTime != &completedAt || progress.Error != "saved build error" {
				t.Fatalf("saved build history changed: %+v", progress)
			}
		})
	}
}

func TestRefreshProgressStatsUnsupportedBackend(t *testing.T) {
	progress := &model.IndexProgress{ObjCount: 8, IsDone: true}
	refreshProgressStats(context.Background(), progress, &filteredSearchStub{})
	if progress.ObjCount != 8 || !progress.IsDone {
		t.Fatalf("unsupported backend changed progress: %+v", progress)
	}
}

func TestProgressWithStatsPreservesSavedProgress(t *testing.T) {
	for _, running := range []bool{false, true} {
		t.Run(fmt.Sprintf("running=%v", running), func(t *testing.T) {
			oldInstance := instance
			oldQuit := Quit.Load()
			oldSetting, exists := op.Cache.GetSetting(conf.IndexProgress)
			t.Cleanup(func() {
				instance = oldInstance
				Quit.Store(oldQuit)
				if exists {
					op.Cache.SetSetting(conf.IndexProgress, oldSetting)
				} else {
					op.Cache.ClearAll()
				}
			})
			const saved = `{"obj_count":8,"is_done":true,"last_done_time":null,"error":""}`
			op.Cache.SetSetting(conf.IndexProgress, &model.SettingItem{Key: conf.IndexProgress, Value: saved})
			backend := &statsSearchStub{stats: searcher.IndexStats{ObjectCount: 12}}
			instance = backend
			Quit.Store(nil)
			if running {
				quit := make(chan struct{})
				Quit.Store(&quit)
			}
			progress, err := ProgressWithStats(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if running {
				if progress.IsDone || progress.ObjCount != 8 || backend.calls != 0 {
					t.Fatalf("active scan did not retain its progress: %+v, calls %d", progress, backend.calls)
				}
			} else if !progress.IsDone || progress.ObjCount != 12 || backend.calls != 1 {
				t.Fatalf("idle index did not refresh its count: %+v, calls %d", progress, backend.calls)
			}
			stored, _ := op.Cache.GetSetting(conf.IndexProgress)
			var storedProgress model.IndexProgress
			if err := json.Unmarshal([]byte(stored.Value), &storedProgress); err != nil {
				t.Fatal(err)
			}
			if stored.Value != saved {
				t.Fatalf("read-only refresh changed saved progress: %+v", storedProgress)
			}
		})
	}
}
