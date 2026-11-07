package handles

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
)

// Access listings (S3ObjectAccessList, FileAccessList) share one paging and
// prefix policy so a client cannot get two different answers for the same
// query depending on which table it asks.

const (
	accessDefaultLimit = 200
	accessMaxLimit     = 5000
)

// accessPaging parses the limit/offset pair. An invalid value is a client error:
// silently falling back to the default answered "give me 0 rows" with 200 rows,
// and an ignored min_hits/older_than_days filter (which is a 400 on the same
// endpoint) told the caller it got a filtered list when it did not.
func accessPaging(c *gin.Context) (limit, offset int, err error) {
	limit = accessDefaultLimit
	if raw := strings.TrimSpace(c.Query("limit")); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n <= 0 {
			return 0, 0, fmt.Errorf("limit must be a positive integer, got %q", raw)
		}
		if n > accessMaxLimit {
			n = accessMaxLimit
		}
		limit = n
	}
	if raw := strings.TrimSpace(c.Query("offset")); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 0 {
			return 0, 0, fmt.Errorf("offset must be a non-negative integer, got %q", raw)
		}
		offset = n
	}
	return limit, offset, nil
}

// accessPrefixCondition builds a case-sensitive "path starts with prefix" test.
// Comparing substr(path, 1, n) with the prefix instead of using LIKE keeps "%"
// and "_" in the prefix literal, but it is case-sensitive only where the
// collation is: SQLite and PostgreSQL compare byte-wise, while MySQL compares
// with the column collation, which is case-insensitive by default - so
// "/Movies" would again match "/movies/..." from a different mount and an
// archiving job fed with this list would move objects outside the tree it was
// pointed at. BINARY is MySQL-only syntax, hence the branch.
func accessPrefixCondition(dialect, column, prefix string) (string, []any) {
	args := []any{utf8.RuneCountInString(prefix), prefix}
	if strings.EqualFold(dialect, "mysql") {
		return fmt.Sprintf("BINARY substr(%s, 1, ?) = ?", column), args
	}
	return fmt.Sprintf("substr(%s, 1, ?) = ?", column), args
}

// accessBoolFlag accepts the spellings a shell script or an admin panel sends
// for a boolean query parameter; anything else is rejected rather than treated
// as false, because silently dropping "1" or "yes" changes the row set.
func accessBoolFlag(raw string) (bool, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return false, nil
	}
	switch strings.ToLower(trimmed) {
	case "true", "1", "yes", "y", "on":
		return true, nil
	case "false", "0", "no", "n", "off":
		return false, nil
	}
	return false, fmt.Errorf("expected a boolean, got %q", raw)
}
