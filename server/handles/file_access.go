package handles

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/OpenListTeam/OpenList/v4/internal/access"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

// FileAccessList lists files this instance has served, with the time each was
// last read and how many times it was read.
//
// It exists for tiering: "move what nobody reads to slow storage" and "bring back
// what is read again" both need a read time, and the storage drivers have none.
// The direct link, the proxy, WebDAV and the media library record one here, so
// this answers for every file whether it is watched, streamed or downloaded.
//
// Query parameters:
//
//	path             only files whose path starts with this string
//	older_than_days  only files last read at least this many days ago
//	newer_than_days  only files last read within this many days
//	min_hits         only files read at least this many times
//	order            "old" (default, oldest read first), "new" (most recent
//	                 first) or "hits" (most read first, for a top-N view)
//	limit            at most this many rows (default 200, maximum 5000)
//	offset           skip this many rows (default 0)
func FileAccessList(c *gin.Context) {
	limit, offset, err := accessPaging(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	if db.GetDb() == nil {
		common.ErrorResp(c, errors.New("database is not initialized"), 500)
		return
	}

	query := db.GetDb().Model(&model.FileAccess{})
	if prefix := c.Query("path"); prefix != "" {
		// Byte-wise, not LIKE: LIKE ignores case on several engines and treats "%"
		// and "_" as wildcards, so a prefix from one mount also matched another
		// mount whose name differed only in case.
		clause, args := accessPrefixCondition(db.GetDb().Dialector.Name(), "path", prefix)
		query = query.Where(clause, args...)
	}
	if bucket := strings.TrimSpace(c.Query("bucket")); bucket != "" {
		// Exact match, not a prefix: a bucket is a name, and "hot" must not also
		// select "hot-archive".
		query = query.Where("bucket = ?", bucket)
	}
	if raw := c.Query("older_than_days"); raw != "" {
		days, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || days < 0 {
			common.ErrorResp(c, fmt.Errorf("older_than_days must be a non-negative integer, got %q", raw), 400)
			return
		}
		query = query.Where("last_access < ?", time.Now().UTC().AddDate(0, 0, -days))
	}
	if raw := c.Query("newer_than_days"); raw != "" {
		days, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || days < 0 {
			common.ErrorResp(c, fmt.Errorf("newer_than_days must be a non-negative integer, got %q", raw), 400)
			return
		}
		query = query.Where("last_access >= ?", time.Now().UTC().AddDate(0, 0, -days))
	}
	if raw := c.Query("min_hits"); raw != "" {
		// Ignoring the filter answered with rows below the requested hit count,
		// which reads as "this file is hot" to whatever consumes the list.
		hits, convErr := strconv.Atoi(strings.TrimSpace(raw))
		if convErr != nil || hits < 0 {
			common.ErrorResp(c, fmt.Errorf("min_hits must be a non-negative integer, got %q", raw), 400)
			return
		}
		query = query.Where("hits >= ?", hits)
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	var items []model.FileAccess
	order := "last_access ASC"
	switch c.Query("order") {
	case "new":
		order = "last_access DESC"
	case "hits":
		// Top-N: a file read a thousand times is the one worth keeping hot, and
		// ordering by recency hides it as soon as the newest touch is elsewhere.
		order = "hits DESC, last_access DESC"
	}
	if err := query.Order(order).Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{
		"total":   total,
		"content": items,
	})
}

// accessTimestamp normalises a MIN/MAX timestamp to RFC 3339 so the reply looks
// the same whichever driver produced it. SQLite hands back the stored text.
func accessTimestamp(value any) any {
	switch typed := value.(type) {
	case time.Time:
		return typed.UTC().Format(time.RFC3339)
	case *time.Time:
		if typed == nil {
			return nil
		}
		return typed.UTC().Format(time.RFC3339)
	default:
		return value
	}
}

// FileAccessSummary describes the access table as a whole: how many rows exist,
// which buckets they came through, and how old the oldest record is.
//
// The admin page needs this overview, and computing it from the list endpoint
// would mean paging through every row.
// accessSummaryFacts is the part of the overview that comes from the table. It
// is cached as a value and never mutated, so a request can copy the fields into
// its own reply map while another request reads the same struct.
type accessSummaryFacts struct {
	Total   int64
	Buckets []gin.H
	Oldest  any
	Newest  any
	Heatmap gin.H
}

// accessSummaryTTL bounds how stale the overview may be. Every number in it is
// a whole-table aggregate, and rows are rewritten by readers continuously, so
// the figure is approximate by nature; caching it for a few seconds is what
// keeps a page that reloads on every open from rescanning the table each time.
const accessSummaryTTL = 15 * time.Second

var (
	accessSummaryMu    sync.Mutex
	accessSummaryCache accessSummaryFacts
	accessSummaryAt    time.Time
)

// accessSummaryCacheKey is bumped whenever something makes the cached facts
// wrong on purpose (a prune), so a stale reply cannot outlive an admin action.
var accessSummaryGeneration uint64

func cachedAccessSummary() (accessSummaryFacts, bool) {
	accessSummaryMu.Lock()
	defer accessSummaryMu.Unlock()
	if accessSummaryAt.IsZero() || time.Since(accessSummaryAt) > accessSummaryTTL {
		return accessSummaryFacts{}, false
	}
	return accessSummaryCache, true
}

func storeAccessSummary(facts accessSummaryFacts) {
	accessSummaryMu.Lock()
	defer accessSummaryMu.Unlock()
	accessSummaryCache = facts
	accessSummaryAt = time.Now()
}

// invalidateAccessSummary drops the cached facts after a destructive change: a
// prune that just reported "deleted 800 rows" must not be followed by an
// overview still showing them.
func invalidateAccessSummary() {
	accessSummaryMu.Lock()
	defer accessSummaryMu.Unlock()
	accessSummaryAt = time.Time{}
	accessSummaryGeneration++
}

// accessSummaryFactsNow returns the cached facts when fresh, otherwise reads
// them from the table and caches the result for accessSummaryTTL.
func accessSummaryFactsNow() (accessSummaryFacts, error) {
	if facts, ok := cachedAccessSummary(); ok {
		return facts, nil
	}
	facts, err := computeAccessSummary()
	if err != nil {
		return accessSummaryFacts{}, err
	}
	storeAccessSummary(facts)
	return facts, nil
}

func FileAccessSummary(c *gin.Context) {
	if db.GetDb() == nil {
		common.ErrorResp(c, errors.New("database is not initialized"), 500)
		return
	}

	facts, err := accessSummaryFactsNow()
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	common.SuccessResp(c, gin.H{
		"total":   facts.Total,
		"buckets": facts.Buckets,
		"oldest":  facts.Oldest,
		"newest":  facts.Newest,
		"heatmap": facts.Heatmap,
		// The retention default is a setting, not a table fact: it is read on
		// every request so a changed default shows up immediately.
		"retention": gin.H{
			"days": setting.GetInt(conf.FileAccessRetentionDays, 365),
		},
	})
}

func computeAccessSummary() (accessSummaryFacts, error) {
	type bucketRow struct {
		Bucket string `gorm:"column:bucket"`
		Count  int64  `gorm:"column:count"`
		Hits   int64  `gorm:"column:hits"`
	}
	var rows []bucketRow
	// COALESCE keeps rows recorded before the bucket column existed in the
	// "no bucket" group instead of dropping them from every total.
	if err := db.GetDb().Model(&model.FileAccess{}).
		Select("COALESCE(bucket, '') AS bucket, COUNT(*) AS count, COALESCE(SUM(hits), 0) AS hits").
		Group("COALESCE(bucket, '')").
		Order("count DESC").
		Scan(&rows).Error; err != nil {
		return accessSummaryFacts{}, err
	}

	var total int64
	buckets := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		total += row.Count
		buckets = append(buckets, gin.H{
			"bucket": row.Bucket,
			"count":  row.Count,
			"hits":   row.Hits,
		})
	}

	// MIN/MAX into `any` scans as a driver-specific value and logs a parse error
	// on some drivers, so the two bounds are read as plain ordered rows instead:
	// the column keeps its native type that way and every driver agrees.
	var oldestEdge, newestEdge struct {
		LastAccess *time.Time `gorm:"column:last_access"`
	}
	if err := db.GetDb().Model(&model.FileAccess{}).
		Select("last_access").Order("last_access ASC").Limit(1).
		Scan(&oldestEdge).Error; err != nil {
		return accessSummaryFacts{}, err
	}
	if err := db.GetDb().Model(&model.FileAccess{}).
		Select("last_access").Order("last_access DESC").Limit(1).
		Scan(&newestEdge).Error; err != nil {
		return accessSummaryFacts{}, err
	}

	heatmap, err := accessHeatmap()
	if err != nil {
		return accessSummaryFacts{}, err
	}

	return accessSummaryFacts{
		Total:   total,
		Buckets: buckets,
		Oldest:  accessTimestamp(oldestEdge.LastAccess),
		Newest:  accessTimestamp(newestEdge.LastAccess),
		Heatmap: heatmap,
	}, nil
}

// accessHeatmapBins are the recency buckets of the heatmap, in days. The last
// bin collects everything older than the second-to-last boundary, so every row
// lands in exactly one bin.
var accessHeatmapBins = []int{1, 7, 30, 90, 180, 365}

// accessHeatmap counts the rows of every bucket, split by how long ago they
// were last read. It is a "how stale is each bucket" map: one row per bucket,
// one cell per recency bin.
//
// The split is done in SQL with explicit cutoffs rather than per-row in Go: the
// table holds one row per file, and a library with a million files must not be
// streamed into memory to draw a small grid.
func accessHeatmap() (gin.H, error) {
	// UTC, like every other cutoff in this package: the column is written in
	// UTC by both trackers, so a local cutoff would be off by the server's
	// offset and move rows across a bin boundary.
	now := time.Now().UTC()
	// Cutoffs newest first, matching accessHeatmapBins.
	cuts := make([]time.Time, 0, len(accessHeatmapBins))
	for _, days := range accessHeatmapBins {
		cuts = append(cuts, now.AddDate(0, 0, -days))
	}

	// Every bin is expressed as ">= cutoff[n]" or "between cutoff[n] and
	// cutoff[n-1]", so no bin can double count and none can be missed.
	selects := []string{"COALESCE(bucket, '') AS bucket"}
	args := make([]any, 0, 2*len(cuts))
	selects = append(selects, "SUM(CASE WHEN last_access >= ? THEN 1 ELSE 0 END) AS bin0")
	args = append(args, cuts[0])
	for i := 1; i < len(cuts); i++ {
		selects = append(selects,
			fmt.Sprintf("SUM(CASE WHEN last_access < ? AND last_access >= ? THEN 1 ELSE 0 END) AS bin%d", i))
		args = append(args, cuts[i-1], cuts[i])
	}
	selects = append(selects,
		fmt.Sprintf("SUM(CASE WHEN last_access < ? THEN 1 ELSE 0 END) AS bin%d", len(cuts)))
	args = append(args, cuts[len(cuts)-1])

	type heatRow struct {
		Bucket string `gorm:"column:bucket"`
		Bin0   int64  `gorm:"column:bin0"`
		Bin1   int64  `gorm:"column:bin1"`
		Bin2   int64  `gorm:"column:bin2"`
		Bin3   int64  `gorm:"column:bin3"`
		Bin4   int64  `gorm:"column:bin4"`
		Bin5   int64  `gorm:"column:bin5"`
		Bin6   int64  `gorm:"column:bin6"`
	}
	var scanned []heatRow
	if err := db.GetDb().Model(&model.FileAccess{}).
		Select(strings.Join(selects, ", "), args...).
		Group("COALESCE(bucket, '')").
		Scan(&scanned).Error; err != nil {
		return nil, err
	}

	rows := make([]gin.H, 0, len(scanned))
	for _, row := range scanned {
		rows = append(rows, gin.H{
			"bucket": row.Bucket,
			"cells":  []int64{row.Bin0, row.Bin1, row.Bin2, row.Bin3, row.Bin4, row.Bin5, row.Bin6},
		})
	}
	// Busiest bucket first: the top of the grid is the one worth looking at.
	sort.SliceStable(rows, func(i, j int) bool {
		return cellsTotal(rows[i]) > cellsTotal(rows[j])
	})

	bins := make([]gin.H, 0, len(accessHeatmapBins)+1)
	prev := 0
	for _, days := range accessHeatmapBins {
		bins = append(bins, gin.H{"from_days": prev, "to_days": days})
		prev = days
	}
	bins = append(bins, gin.H{"from_days": prev, "to_days": 0})

	return gin.H{"bins": bins, "rows": rows}, nil
}

func cellsTotal(row gin.H) int64 {
	var sum int64
	for _, cell := range row["cells"].([]int64) {
		sum += cell
	}
	return sum
}

// FileAccessPrune drops access records older than a cutoff.
//
// The cutoff comes from the settings (file_access_retention_days, default 365), so
// a scheduled call needs no parameters:
//
//	older_than_days  overrides the cutoff, in days (>= 1)
//	dry_run=true     reports what would go without deleting anything
func FileAccessPrune(c *gin.Context) {
	days := setting.GetInt(conf.FileAccessRetentionDays, 365)
	if raw := c.Query("older_than_days"); raw != "" {
		n, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || n < 1 {
			common.ErrorResp(c, errors.New("older_than_days must be a positive integer"), 400)
			return
		}
		days = n
	}

	if dryRun, _ := strconv.ParseBool(c.Query("dry_run")); dryRun {
		n, err := access.CountPrunableDays(days)
		if err != nil {
			common.ErrorResp(c, err, 500)
			return
		}
		common.SuccessResp(c, gin.H{
			"dry_run":      true,
			"would_delete": n,
			"days":         days,
		})
		return
	}

	deleted, err := access.PruneDays(days)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	invalidateAccessSummary()
	common.SuccessResp(c, gin.H{
		"deleted": deleted,
		"days":    days,
	})
}
