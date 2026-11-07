package handles

import (
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

// S3ObjectAccessList lists the objects the built-in S3 server has served, with
// the time each was last read. The storage drivers keep no access time, so this
// is the only place a hot/cold split can be derived from: an object nobody has
// read for a month can be archived to slow storage without guessing from its
// modification time.
//
// Query parameters:
//
//	path            only objects whose path starts with this string
//	older_than_days only objects last read at least this many days ago
//	limit           at most this many rows (default 200, maximum 5000)
//
// Rows are ordered oldest read first, because that is the order in which
// objects become archiving candidates.
func S3ObjectAccessList(c *gin.Context) {
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

	query := db.GetDb().Model(&model.S3ObjectMetadata{})
	// An object nobody ever read is the best archiving candidate there is, so it
	// can be included; without the flag the answer only covers objects that were
	// read at least once.
	includeNeverRead := c.Query("include_never_read") == "true"
	if prefix := c.Query("path"); prefix != "" {
		query = query.Where(`path LIKE ? ESCAPE '\'`, escapeLikePrefix(prefix)+"%")
	}
	if raw := c.Query("older_than_days"); raw != "" {
		if days, err := strconv.Atoi(raw); err == nil && days >= 0 {
			cutoff := time.Now().UTC().AddDate(0, 0, -days)
			if includeNeverRead {
				query = query.Where("(last_access IS NULL OR last_access < ?)", cutoff)
			} else {
				query = query.Where("last_access IS NOT NULL AND last_access < ?", cutoff)
			}
		}
	} else if !includeNeverRead {
		query = query.Where("last_access IS NOT NULL")
	}

	var total int64
	if err := query.Count(&total).Error; err != nil {
		common.ErrorResp(c, err, 500)
		return
	}

	var items []model.S3ObjectMetadata
	// Never read first: they are the oldest candidates. Among the rest, oldest
	// read first, which is the order in which objects become archivable.
	order := "last_access ASC"
	if includeNeverRead {
		order = "last_access IS NOT NULL ASC, last_access ASC"
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

// escapeLikePrefix makes a path safe to use as the prefix of a LIKE pattern:
// without it a key containing % or _ would match objects it does not name.
func escapeLikePrefix(prefix string) string {
	replacer := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return replacer.Replace(prefix)
}
