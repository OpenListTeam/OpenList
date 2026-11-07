//go:build !sqlite_cgo_compat && !(linux && (mips || mips64 || mips64le || mipsle || loong64)) && !(windows && 386)

package bootstrap

import (
	"net/url"
	"strings"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	return sqlite.Open(normalizeGlebarezSQLiteDSN(dsn))
}

// normalizeGlebarezSQLiteDSN translates mattn/go-sqlite3 style query parameters
// (_journal, _vacuum) into the _pragma syntax required by github.com/glebarez/go-sqlite
// (modernc.org/sqlite), and configures busy_timeout and immediate transaction locking
// for file-backed databases to prevent SQLITE_BUSY under concurrent WebDAV/API access.
func normalizeGlebarezSQLiteDSN(dsn string) string {
	base, rawQuery, hasQuery := strings.Cut(dsn, "?")
	if !hasQuery {
		return dsn
	}
	q, err := url.ParseQuery(rawQuery)
	if err != nil {
		return dsn
	}

	existingPragmas := q["_pragma"]
	hasJournal := false
	hasVacuum := false
	hasBusyTimeout := false
	for _, p := range existingPragmas {
		lower := strings.ToLower(strings.TrimSpace(p))
		switch {
		case strings.HasPrefix(lower, "journal_mode"):
			hasJournal = true
		case strings.HasPrefix(lower, "auto_vacuum"):
			hasVacuum = true
		case strings.HasPrefix(lower, "busy_timeout"):
			hasBusyTimeout = true
		}
	}

	journal := q.Get("_journal")
	q.Del("_journal")
	vacuum := q.Get("_vacuum")
	q.Del("_vacuum")

	isMemory := strings.Contains(base, ":memory:") || strings.EqualFold(q.Get("mode"), "memory")

	var pragmas []string
	if !isMemory && !hasBusyTimeout {
		pragmas = append(pragmas, "busy_timeout(10000)")
	}
	if journal != "" && !hasJournal {
		pragmas = append(pragmas, "journal_mode("+journal+")")
	}
	if vacuum != "" && !hasVacuum {
		pragmas = append(pragmas, "auto_vacuum("+vacuum+")")
	}
	pragmas = append(pragmas, existingPragmas...)
	if len(pragmas) > 0 {
		q["_pragma"] = pragmas
	}

	if !isMemory && q.Get("_txlock") == "" {
		q.Set("_txlock", "immediate")
	}

	encoded := q.Encode()
	if encoded == "" {
		return base
	}
	return base + "?" + encoded
}
