//go:build !sqlite_cgo_compat && !(linux && (mips || mips64 || mips64le || mipsle || loong64)) && !(windows && 386)

package bootstrap

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	// glebarez/go-sqlite only understands "_pragma=" parameters; the mattn style
	// "_journal=", "_busy_timeout=" ones are silently ignored, which left the
	// database in rollback-journal mode with the driver default 5s busy timeout.
	// Readers and the single writer then block each other, so concurrent saves
	// (e.g. a rotated 115 Open refresh token) fail with SQLITE_BUSY.
	return sqlite.Open(appendDSNParams(dsn,
		"_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)"))
}
