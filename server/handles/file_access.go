package handles

import (
	"errors"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/OpenListTeam/OpenList/v4/internal/access"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
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
//	order            "old" (default, oldest read first) or "new"
//	limit            at most this many rows (default 200, maximum 5000)
func FileAccessList(c *gin.Context) {
	limit := 200
	if raw := c.Query("limit"); raw != "" {
		if n, err := strconv.Atoi(raw); err == nil && n > 0 {
			if n > 5000 {
				n = 5000
			}
			limit = n
		}
	}

	if db.GetDb() == nil {
		common.ErrorResp(c, errors.New("database is not initialized"), 500)
		return
	}

	query := db.GetDb().Model(&model.FileAccess{})
	if prefix := c.Query("path"); prefix != "" {
		query = query.Where(`path LIKE ? ESCAPE '\'`, escapeLikePrefix(prefix)+"%")
	}
	if raw := c.Query("older_than_days"); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days >= 0 {
			query = query.Where("last_access < ?", time.Now().UTC().AddDate(0, 0, -days))
		}
	}
	if raw := c.Query("newer_than_days"); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days >= 0 {
			query = query.Where("last_access >= ?", time.Now().UTC().AddDate(0, 0, -days))
		}
	}
	if raw := c.Query("min_hits"); raw != "" {
		if hits, err := strconv.Atoi(raw); err == nil && hits > 0 {
			query = query.Where("hits >= ?", hits)
		}
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	var items []model.FileAccess
	order := "last_access ASC"
	if c.Query("order") == "new" {
		order = "last_access DESC"
	}
	if err := query.Order(order).Limit(limit).Find(&items).Error; err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{
		"total":   total,
		"content": items,
	})
}

// FileAccessPrune drops access records whose last read is older than
// older_than_days (default 365, minimum 1) and reports how many were removed.
//
// One row per file, not per request: the table is proportional to the library,
// not to traffic. It still needs a cutoff, because a file that was deleted or
// renamed leaves its row behind and a re-imported library re-adds one row per
// incarnation. Pruning costs the count of reads older than the cutoff; the next
// read of that path starts again at one.
func FileAccessPrune(c *gin.Context) {
	days := 365
	if raw := c.Query("older_than_days"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 {
			common.ErrorResp(c, errors.New("older_than_days must be a positive integer"), 400)
			return
		}
		days = n
	}

	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	deleted, err := access.Prune(cutoff)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{
		"deleted":         deleted,
		"older_than_days": days,
		"cutoff":          cutoff.Format(time.RFC3339),
	})
}
