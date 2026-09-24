//go:build benchmark

package bootstrap

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	_ "github.com/OpenListTeam/OpenList/v4/drivers/local"
	"github.com/OpenListTeam/OpenList/v4/drivers/strm"
	_ "github.com/OpenListTeam/OpenList/v4/drivers/webdav"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/search"
	"github.com/OpenListTeam/OpenList/v4/pkg/mq"
	"github.com/glebarez/sqlite"
	xwebdav "golang.org/x/net/webdav"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// BenchmarkConfiguredProjectionMutation measures one changed 100-object snapshot
// through the configured search and STRM sinks. Set OPENLIST_BENCH_REMOTE=webdav
// to exercise the real WebDav Driver, and set OPENLIST_BENCH_MEILI_URL to select
// a disposable Meilisearch instance instead of SQLite search.
func BenchmarkConfiguredProjectionMutation(b *testing.B) {
	root := b.TempDir()
	sourceRoot := filepath.Join(root, "source")
	projectionRoot := filepath.Join(root, "projection")
	if err := os.MkdirAll(sourceRoot, 0o755); err != nil {
		b.Fatal(err)
	}
	for i := range 99 {
		name := filepath.Join(sourceRoot, fmt.Sprintf("subtitle-%02d.srt", i))
		if err := os.WriteFile(name, []byte("subtitle"), 0o644); err != nil {
			b.Fatal(err)
		}
	}
	toggleA := filepath.Join(sourceRoot, "toggle-a.srt")
	toggleB := filepath.Join(sourceRoot, "toggle-b.srt")
	if err := os.WriteFile(toggleA, make([]byte, 1<<20), 0o644); err != nil {
		b.Fatal(err)
	}

	conf.Conf = conf.DefaultConfig(root)
	database, err := gorm.Open(sqlite.Open(filepath.Join(root, "benchmark.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		b.Fatal(err)
	}
	sqlDB, err := database.DB()
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = sqlDB.Close() })
	db.Init(database)
	op.Cache.ClearAll()

	saveBenchmarkSetting(b, conf.AutoUpdateIndex, "true")
	progress, err := json.Marshal(model.IndexProgress{IsDone: true})
	if err != nil {
		b.Fatal(err)
	}
	saveBenchmarkSetting(b, conf.IndexProgress, string(progress))
	searchMode := "database"
	if host := os.Getenv("OPENLIST_BENCH_MEILI_URL"); host != "" {
		searchMode = "meilisearch"
		conf.Conf.Meilisearch.Host = host
		conf.Conf.Meilisearch.Index = fmt.Sprintf("openlist-benchmark-%d", time.Now().UnixNano())
	}
	if err := search.Init(searchMode); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = search.Init("none") })

	storageDriver := "Local"
	additionConfig := map[string]any{"root_folder_path": sourceRoot, "thumbnail": false}
	if os.Getenv("OPENLIST_BENCH_REMOTE") == "webdav" {
		server := httptest.NewServer(&xwebdav.Handler{
			Prefix: "/", FileSystem: xwebdav.Dir(sourceRoot), LockSystem: xwebdav.NewMemLS(),
		})
		b.Cleanup(server.Close)
		storageDriver = "WebDav"
		additionConfig = map[string]any{
			"vendor": "other", "address": server.URL,
			"username": "benchmark", "password": "benchmark", "root_folder_path": "/",
		}
	}
	addition, err := json.Marshal(additionConfig)
	if err != nil {
		b.Fatal(err)
	}
	storageID, err := op.CreateStorage(context.Background(), model.Storage{
		Driver: storageDriver, MountPath: "/source", Addition: string(addition), CacheExpiration: 5,
	})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = op.DeleteStorageById(context.Background(), storageID) })

	strmDriver := &strm.Strm{
		Storage: model.Storage{MountPath: "/strm"},
		Addition: strm.Addition{
			Paths: "/source", DownloadFileTypes: "srt", SaveStrmToLocal: true,
			SaveStrmLocalPath: projectionRoot, SaveLocalMode: strm.SaveLocalSyncMode, Version: 5,
		},
	}
	if err := strmDriver.Init(context.Background()); err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = strmDriver.Drop(context.Background()) })

	searchTiming, strmTiming := newBenchmarkSinkTiming(), newBenchmarkSinkTiming()
	searchProjection = mq.NewLatestProcessor(256, 65536, 4, func(ctx context.Context, parent string, objs []model.Obj) {
		search.UpdateSnapshot(ctx, parent, objs)
		searchTiming.complete(parent)
	})
	strmProjection = mq.NewLatestProcessor(128, 32768, 1, func(ctx context.Context, parent string, objs []model.Obj) {
		strm.UpdateLocalStrm(ctx, parent, objs)
		strmTiming.complete(parent)
	})
	op.SetSnapshotProjector(func(_ context.Context, parent string, objs []model.Obj) {
		searchTiming.offer(parent)
		offerSnapshot("search", searchProjection, parent, objs)
		strmTiming.offer(parent)
		offerSnapshot("strm", strmProjection, parent, objs)
	})
	b.Cleanup(func() {
		op.SetSnapshotProjector(nil)
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = searchProjection.Stop(ctx)
		_ = strmProjection.Stop(ctx)
	})

	if _, err := fs.List(context.Background(), "/source", &fs.ListArgs{Refresh: true}); err != nil {
		b.Fatal(err)
	}
	waitBenchmarkProjectionDrain(b)
	searchTiming.reset()
	strmTiming.reset()

	b.ReportAllocs()
	b.ResetTimer()
	current, next := toggleA, toggleB
	for b.Loop() {
		if err := os.Rename(current, next); err != nil {
			b.Fatal(err)
		}
		current, next = next, current
		if _, err := fs.List(context.Background(), "/source", &fs.ListArgs{Refresh: true}); err != nil {
			b.Fatal(err)
		}
		waitBenchmarkProjectionDrain(b)
	}
	b.StopTimer()

	searchTotal, searchCompleted := searchTiming.result()
	strmTotal, strmCompleted := strmTiming.result()
	if searchCompleted != int64(b.N) || strmCompleted != int64(b.N) {
		b.Fatalf("completed search=%d strm=%d, want %d", searchCompleted, strmCompleted, b.N)
	}
	b.ReportMetric(float64(searchTotal.Nanoseconds())/float64(b.N), "search-ns/op")
	b.ReportMetric(float64(strmTotal.Nanoseconds())/float64(b.N), "strm-ns/op")
	if searchProjection.Stats().Rejected != 0 || strmProjection.Stats().Rejected != 0 {
		b.Fatalf("projection rejected work: search=%+v strm=%+v", searchProjection.Stats(), strmProjection.Stats())
	}
	validateBenchmarkProjection(b, projectionRoot, filepath.Base(current), filepath.Base(next))
}

type benchmarkSinkTiming struct {
	mu        sync.Mutex
	offered   map[string]time.Time
	total     time.Duration
	completed int64
}

func newBenchmarkSinkTiming() *benchmarkSinkTiming {
	return &benchmarkSinkTiming{offered: make(map[string]time.Time)}
}

func (s *benchmarkSinkTiming) offer(parent string) {
	s.mu.Lock()
	s.offered[parent] = time.Now()
	s.mu.Unlock()
}

func (s *benchmarkSinkTiming) complete(parent string) {
	s.mu.Lock()
	if start, ok := s.offered[parent]; ok {
		s.total += time.Since(start)
		s.completed++
		delete(s.offered, parent)
	}
	s.mu.Unlock()
}

func (s *benchmarkSinkTiming) reset() {
	s.mu.Lock()
	clear(s.offered)
	s.total = 0
	s.completed = 0
	s.mu.Unlock()
}

func (s *benchmarkSinkTiming) result() (time.Duration, int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.total, s.completed
}

func saveBenchmarkSetting(b *testing.B, key, value string) {
	b.Helper()
	if err := op.SaveSettingItem(&model.SettingItem{Key: key, Value: value}); err != nil {
		b.Fatal(err)
	}
}

func validateBenchmarkProjection(b *testing.B, projectionRoot, current, absent string) {
	b.Helper()
	nodes, _, err := search.Search(context.Background(), model.SearchReq{
		Parent: "/source", PageReq: model.PageReq{Page: 1, PerPage: 1000},
	})
	if err != nil {
		b.Fatal(err)
	}
	count, foundCurrent, foundAbsent := 0, false, false
	for _, node := range nodes {
		if node.Parent != "/source" {
			continue
		}
		count++
		foundCurrent = foundCurrent || node.Name == current
		foundAbsent = foundAbsent || node.Name == absent
	}
	if count != 100 || !foundCurrent || foundAbsent {
		b.Fatalf("search projection count=%d current=%t absent=%t", count, foundCurrent, foundAbsent)
	}
	if _, err := os.Stat(filepath.Join(projectionRoot, "source", current)); err != nil {
		b.Fatalf("current STRM projection: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectionRoot, "source", absent)); !os.IsNotExist(err) {
		b.Fatalf("stale STRM projection exists or cannot be checked: %v", err)
	}
}

func waitBenchmarkProjectionDrain(b *testing.B) {
	b.Helper()
	deadline := time.Now().Add(time.Minute)
	for {
		searchStats := searchProjection.Stats()
		strmStats := strmProjection.Stats()
		if searchStats.Pending+searchStats.InFlight+strmStats.Pending+strmStats.InFlight == 0 {
			return
		}
		if time.Now().After(deadline) {
			b.Fatalf("projection did not drain: search=%+v strm=%+v", searchStats, strmStats)
		}
		time.Sleep(time.Millisecond)
	}
}
