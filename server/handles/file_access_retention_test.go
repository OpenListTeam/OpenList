package handles

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// seedAccess writes one access row per (path, bucket, age).
func seedAccess(t *testing.T, rows []model.FileAccess) {
	t.Helper()
	database := setupAccessFilterDB(t)
	for i := range rows {
		row := rows[i]
		if err := database.Create(&row).Error; err != nil {
			t.Fatalf("seed %q: %v", row.Path, err)
		}
	}
}

// dataOf unwraps the {code,data,message} envelope the admin API replies with.
func dataOf(t *testing.T, body map[string]any) map[string]any {
	t.Helper()
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("reply has no data object: %v", body)
	}
	return data
}

func setBucketSetting(t *testing.T, key, value string) {
	t.Helper()
	op.Cache.SetSetting(key, &model.SettingItem{Key: key, Value: value})
	t.Cleanup(func() { op.Cache.SetSetting(key, &model.SettingItem{Key: key, Value: ""}) })
}

// TestFileAccessPruneUsesOneCutoffForEveryBucket pins the shape that replaced the
// hot/cold split: one cutoff decides for every row, whatever bucket recorded it.
func TestFileAccessPruneUsesOneCutoffForEveryBucket(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -900)
	seedAccess(t, []model.FileAccess{
		{Path: "/本机储存/热层/hot.txt", Bucket: "hot", LastAccess: &old, FirstAccess: &old, Hits: 2},
		{Path: "/本机储存/冷层/cold.txt", Bucket: "cold", LastAccess: &old, FirstAccess: &old, Hits: 1},
	})

	body := dataOf(t, callJSON(t, FileAccessPrune, "/api/admin/file_access/prune?older_than_days=30"))
	if body["deleted"] != float64(2) || body["days"] != float64(30) {
		t.Fatalf("prune replied %v, want two rows deleted on a 30 day cutoff", body)
	}
	var left []model.FileAccess
	if err := db.GetDb().Model(&model.FileAccess{}).Find(&left).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if len(left) != 0 {
		t.Fatalf("rows left = %v, want none", left)
	}
}

// TestFileAccessPruneDryRunCountsWithoutDeleting pins the preview: an operator
// looking at the wrong cutoff sees the number before the rows are gone.
func TestFileAccessPruneDryRunCountsWithoutDeleting(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -900)
	seedAccess(t, []model.FileAccess{
		{Path: "/本机储存/热层/a.txt", Bucket: "hot", LastAccess: &old, FirstAccess: &old, Hits: 1},
		{Path: "/本机储存/热层/b.txt", Bucket: "hot", LastAccess: &old, FirstAccess: &old, Hits: 1},
	})

	body := dataOf(t, callJSON(t, FileAccessPrune, "/api/admin/file_access/prune?older_than_days=30&dry_run=true"))
	if body["would_delete"] != float64(2) || body["dry_run"] != true {
		t.Fatalf("dry run replied %v, want two rows reported", body)
	}
	var count int64
	if err := db.GetDb().Model(&model.FileAccess{}).Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 2 {
		t.Fatalf("rows left = %d, want both kept by a dry run", count)
	}
}

// TestFileAccessListFiltersByBucket pins the isolation: the tiering script asks
// for one bucket's reads and gets only those, even though both buckets front the
// same instance.
func TestFileAccessListFiltersByBucket(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -5)
	seedAccess(t, []model.FileAccess{
		{Path: "/本机储存/热层/a.txt", Bucket: "hot", LastAccess: &old, FirstAccess: &old, Hits: 1},
		{Path: "/本机储存/冷层/b.txt", Bucket: "cold", LastAccess: &old, FirstAccess: &old, Hits: 1},
	})

	body := dataOf(t, callJSON(t, FileAccessList, "/api/admin/file_access/list?bucket=cold"))
	content, _ := body["content"].([]any)
	if len(content) != 1 {
		t.Fatalf("content = %v, want one cold row", body["content"])
	}
	row, _ := content[0].(map[string]any)
	if row["bucket"] != "cold" {
		t.Fatalf("row = %v, want the cold bucket row", row)
	}
}

// TestFileAccessSummaryGroupsByBucket pins the overview the admin page reads: one
// row per bucket (plus the no-bucket group), the total across all of them, and the
// retention cutoff that a prune would use.
func TestFileAccessSummaryGroupsByBucket(t *testing.T) {
	now := time.Now().UTC()
	older := now.AddDate(0, 0, -40)
	seedAccess(t, []model.FileAccess{
		{Path: "/hot/a.txt", Bucket: "hot", LastAccess: &now, FirstAccess: &now, Hits: 5},
		{Path: "/hot/b.txt", Bucket: "hot", LastAccess: &older, FirstAccess: &older, Hits: 3},
		{Path: "/cold/c.txt", Bucket: "cold", LastAccess: &older, FirstAccess: &older, Hits: 1},
		// A row recorded by a reader that named no bucket (direct link, WebDAV).
		{Path: "/raw/d.txt", Bucket: "", LastAccess: &now, FirstAccess: &now, Hits: 2},
	})
	setBucketSetting(t, conf.FileAccessRetentionDays, "365")

	body := dataOf(t, callJSON(t, FileAccessSummary, "/api/admin/file_access/summary"))
	if body["total"] != float64(4) {
		t.Fatalf("total = %v, want 4", body["total"])
	}
	buckets, _ := body["buckets"].([]any)
	if len(buckets) != 3 {
		t.Fatalf("buckets = %v, want three groups (hot, cold, no bucket)", body["buckets"])
	}
	counts := map[string]float64{}
	hits := map[string]float64{}
	for _, raw := range buckets {
		entry, _ := raw.(map[string]any)
		counts[entry["bucket"].(string)] = entry["count"].(float64)
		hits[entry["bucket"].(string)] = entry["hits"].(float64)
	}
	if counts["hot"] != 2 || counts["cold"] != 1 || counts[""] != 1 {
		t.Fatalf("counts = %v, want hot=2 cold=1 unnamed=1", counts)
	}
	if hits["hot"] != 8 || hits["cold"] != 1 || hits[""] != 2 {
		t.Fatalf("hits = %v, want hot=8 cold=1 unnamed=2", hits)
	}
	retention, _ := body["retention"].(map[string]any)
	if retention["days"] != float64(365) {
		t.Fatalf("retention = %v, want 365 days", retention)
	}
}

// TestFileAccessListOrderHitsPutsMostReadFirst pins the top-N view: ordering by
// hits must put the most read file first, not the most recently read one.
func TestFileAccessListOrderHitsPutsMostReadFirst(t *testing.T) {
	now := time.Now().UTC()
	recent := now.Add(-time.Hour)
	seedAccess(t, []model.FileAccess{
		{Path: "/a.txt", Bucket: "hot", LastAccess: &now, FirstAccess: &now, Hits: 1},
		{Path: "/b.txt", Bucket: "hot", LastAccess: &recent, FirstAccess: &recent, Hits: 7},
		{Path: "/c.txt", Bucket: "hot", LastAccess: &now, FirstAccess: &now, Hits: 3},
	})

	body := dataOf(t, callJSON(t, FileAccessList, "/api/admin/file_access/list?order=hits"))
	content, _ := body["content"].([]any)
	if len(content) != 3 {
		t.Fatalf("content = %v, want three rows", body["content"])
	}
	first, _ := content[0].(map[string]any)
	if first["path"] != "/b.txt" {
		t.Fatalf("first row = %v, want /b.txt (7 hits) even though /a.txt was read later", first)
	}
}

func TestFileAccessSummaryHeatmapBinsByRecency(t *testing.T) {
	now := time.Now().UTC()
	day := func(n int) *time.Time {
		v := now.AddDate(0, 0, -n)
		return &v
	}
	seedAccess(t, []model.FileAccess{
		{Path: "/hot/today.txt", Bucket: "hot", LastAccess: day(0), FirstAccess: day(0), Hits: 1},
		{Path: "/hot/three-days.txt", Bucket: "hot", LastAccess: day(3), FirstAccess: day(3), Hits: 1},
		{Path: "/hot/forty-days.txt", Bucket: "hot", LastAccess: day(40), FirstAccess: day(40), Hits: 1},
		{Path: "/cold/last-year.txt", Bucket: "cold", LastAccess: day(400), FirstAccess: day(400), Hits: 1},
	})

	body := dataOf(t, callJSON(t, FileAccessSummary, "/api/admin/file_access/summary"))
	heat, ok := body["heatmap"].(map[string]any)
	if !ok {
		t.Fatalf("heatmap missing from summary: %v", body)
	}
	bins, _ := heat["bins"].([]any)
	if len(bins) != 7 {
		t.Fatalf("bins = %v, want seven recency buckets", heat["bins"])
	}
	rows, _ := heat["rows"].([]any)
	if len(rows) != 2 {
		t.Fatalf("rows = %v, want one row per bucket", heat["rows"])
	}
	cells := map[string][]int64{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		rawCells, _ := row["cells"].([]any)
		got := make([]int64, 0, len(rawCells))
		for _, cell := range rawCells {
			got = append(got, int64(cell.(float64)))
		}
		cells[row["bucket"].(string)] = got
	}
	wantHot := []int64{1, 1, 0, 1, 0, 0, 0}
	wantCold := []int64{0, 0, 0, 0, 0, 0, 1}
	for i, want := range wantHot {
		if cells["hot"][i] != want {
			t.Fatalf("hot cells = %v, want %v", cells["hot"], wantHot)
		}
	}
	for i, want := range wantCold {
		if cells["cold"][i] != want {
			t.Fatalf("cold cells = %v, want %v", cells["cold"], wantCold)
		}
	}
}

// TestFileAccessSummaryIsCachedUntilInvalidated pins both halves of the cache
// contract: a reload inside the TTL does not rescan the table, and a prune that
// changed the table does not leave the old numbers on screen.
func TestFileAccessSummaryIsCachedUntilInvalidated(t *testing.T) {
	older := time.Now().UTC().AddDate(0, 0, -10)
	seedAccess(t, []model.FileAccess{
		{Path: "/store/cached.txt", Bucket: "hot", LastAccess: &older, FirstAccess: &older, Hits: 1},
	})

	first := dataOf(t, callJSON(t, FileAccessSummary, "/api/admin/file_access/summary"))
	if first["total"] != float64(1) {
		t.Fatalf("total = %v, want 1", first["total"])
	}

	// A row written behind the cache's back must not show up inside the TTL: if
	// it does, the cache is not actually saving the scan it exists to save.
	extra := model.FileAccess{Path: "/store/later.txt", Bucket: "hot", LastAccess: &older, FirstAccess: &older, Hits: 1}
	if err := db.GetDb().Create(&extra).Error; err != nil {
		t.Fatalf("insert: %v", err)
	}
	cached := dataOf(t, callJSON(t, FileAccessSummary, "/api/admin/file_access/summary"))
	if cached["total"] != float64(1) {
		t.Fatalf("total inside the TTL = %v, want the cached 1", cached["total"])
	}

	// A prune is an explicit admin action, so it must invalidate: the overview
	// right after it reports the table as it is, not as it was.
	pruned := dataOf(t, callJSON(t, FileAccessPrune, "/api/admin/file_access/prune?older_than_days=5"))
	if pruned["deleted"] != float64(2) {
		t.Fatalf("prune deleted %v, want 2", pruned["deleted"])
	}
	after := dataOf(t, callJSON(t, FileAccessSummary, "/api/admin/file_access/summary"))
	if after["total"] != float64(0) {
		t.Fatalf("total after prune = %v, want 0 (cache not invalidated)", after["total"])
	}
}
