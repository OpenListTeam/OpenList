package handles

import (
	"errors"
	"fmt"
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
//	offset          skip this many rows (default 0)
//	include_never_read  "true"/"1"/"yes" also lists objects never read
//
// Rows are ordered oldest read first, because that is the order in which
// objects become archiving candidates.
func S3ObjectAccessList(c *gin.Context) {
	limit, offset, err := accessPaging(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	if db.GetDb() == nil {
		common.ErrorResp(c, errors.New("database is not initialized"), 500)
		return
	}

	query := db.GetDb().Model(&model.S3ObjectMetadata{})
	// An object nobody ever read is the best archiving candidate there is, so it
	// can be included; without the flag the answer only covers objects that were
	// read at least once.
	includeNeverRead, err := accessBoolFlag(c.Query("include_never_read"))
	if err != nil {
		common.ErrorResp(c, fmt.Errorf("include_never_read: %w", err), 400)
		return
	}
	if prefix := c.Query("path"); prefix != "" {
		// A byte-wise prefix test, not LIKE: LIKE treats "%" and "_" as wildcards
		// and ignores case on several engines, so "/Movies" also returned
		// "/movies/..." from a different mount, and an archiving job fed with this
		// list then moved objects outside the tree it was pointed at.
		clause, args := accessPrefixCondition(db.GetDb().Dialector.Name(), "path", prefix)
		query = query.Where(clause, args...)
	}
	var cutoff *time.Time
	if raw := c.Query("older_than_days"); raw != "" {
		days, err := strconv.Atoi(strings.TrimSpace(raw))
		if err != nil || days < 0 {
			// Answering as if the filter were absent returned objects that were
			// read yesterday - and, worse, the never-read ones.
			common.ErrorResp(c, fmt.Errorf("older_than_days must be a non-negative integer, got %q", raw), 400)
			return
		}
		at := time.Now().UTC().AddDate(0, 0, -days)
		cutoff = &at
	}
	switch {
	case includeNeverRead && cutoff != nil:
		query = query.Where("(last_access IS NULL OR last_access < ?)", *cutoff)
	case cutoff != nil:
		query = query.Where("last_access IS NOT NULL AND last_access < ?", *cutoff)
	case !includeNeverRead:
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
	if err := query.Order(order).Limit(limit).Offset(offset).Find(&items).Error; err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c, gin.H{
		"total":   total,
		"content": items,
	})
}
