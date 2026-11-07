package access

import (
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

// TestRecordBucketKeepsBucketThroughUnnamedRead pins the attribution rule: a row
// records the bucket the last named read came through, and a reader that knows
// nothing about buckets (/d, /p, WebDAV) does not erase it. Without the rule, one
// direct-link read would make an S3-served file look like it never came through a
// bucket at all, and the two tiers would stop being distinguishable.
func TestRecordBucketKeepsBucketThroughUnnamedRead(t *testing.T) {
	prepare(t)
	setEnabled(t, "true")

	RecordBucket("hot-bucket", "/本机储存/热层/a.mkv")
	Flush()
	got := row(t, "/本机储存/热层/a.mkv")
	if got == nil || got.Bucket != "hot-bucket" {
		t.Fatalf("bucket = %v, want hot-bucket", got)
	}

	Record("/本机储存/热层/a.mkv")
	Flush()
	got = row(t, "/本机储存/热层/a.mkv")
	if got == nil || got.Bucket != "hot-bucket" {
		t.Fatalf("bucket after an unnamed read = %v, want hot-bucket kept", got)
	}

	// The write throttle keeps a path out of the database for writeInterval, so
	// the second attribution is written on the next allowed flush. Clearing the
	// throttle stands in for that hour passing.
	defaultTracker.mu.Lock()
	defaultTracker.written = map[string]time.Time{}
	defaultTracker.mu.Unlock()
	RecordBucket("cold-bucket", "/本机储存/热层/a.mkv")
	Flush()
	got = row(t, "/本机储存/热层/a.mkv")
	if got == nil || got.Bucket != "cold-bucket" {
		t.Fatalf("bucket after a named read = %v, want cold-bucket", got)
	}
	var rows int64
	if err := db.GetDb().Model(&model.FileAccess{}).Count(&rows).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if rows != 1 {
		t.Fatalf("rows = %d, want one row per file whichever bucket served it", rows)
	}
}

// TestPruneDaysUsesOneCutoffForEveryBucket pins the shape that replaced the
// hot/cold split: one cutoff decides for every row, whatever bucket it names,
// because the cutoff exists to bound the table rather than to protect a tier.
func TestPruneDaysUsesOneCutoffForEveryBucket(t *testing.T) {
	prepare(t)
	old := time.Now().UTC().AddDate(0, 0, -900)
	fresh := time.Now().UTC().AddDate(0, 0, -10)
	seed := []model.FileAccess{
		{Path: "/本机储存/热层/hot.txt", Bucket: "hot", LastAccess: &old, FirstAccess: &old, Hits: 3},
		{Path: "/本机储存/冷层/cold.txt", Bucket: "cold", LastAccess: &old, FirstAccess: &old, Hits: 1},
		{Path: "/本机储存/热层/unnamed.txt", Bucket: "", LastAccess: &old, FirstAccess: &old, Hits: 1},
		{Path: "/本机储存/热层/fresh.txt", Bucket: "hot", LastAccess: &fresh, FirstAccess: &fresh, Hits: 2},
	}
	for i := range seed {
		if err := db.GetDb().Create(&seed[i]).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	if n, err := CountPrunableDays(30); err != nil || n != 3 {
		t.Fatalf("count = %d (%v), want 3", n, err)
	}
	n, err := PruneDays(30)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 3 {
		t.Fatalf("pruned %d rows, want 3", n)
	}
	for _, path := range []string{"/本机储存/热层/hot.txt", "/本机储存/冷层/cold.txt", "/本机储存/热层/unnamed.txt"} {
		if got := row(t, path); got != nil {
			t.Fatalf("%s survived a cutoff it was past", path)
		}
	}
	if got := row(t, "/本机储存/热层/fresh.txt"); got == nil {
		t.Fatal("a row inside the cutoff was pruned")
	}
}

// TestPruneDaysZeroKeepsEverything pins that 0 means "no cutoff" rather than
// "cutoff of zero days", which would empty the table on one call.
func TestPruneDaysZeroKeepsEverything(t *testing.T) {
	prepare(t)
	old := time.Now().UTC().AddDate(0, 0, -900)
	seed := model.FileAccess{Path: "/本机储存/热层/keep.txt", Bucket: "hot", LastAccess: &old, FirstAccess: &old, Hits: 1}
	if err := db.GetDb().Create(&seed).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	n, err := PruneDays(0)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if n != 0 {
		t.Fatalf("pruned %d rows with no cutoff, want 0", n)
	}
	if got := row(t, "/本机储存/热层/keep.txt"); got == nil {
		t.Fatal("a row was pruned although no cutoff was set")
	}
}
