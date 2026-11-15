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
	// Both builds also set _txlock=immediate so write transactions acquire the
	// RESERVED lock upfront instead of hitting SHARED->RESERVED upgrade deadlocks.
	return sqlite.Open(appendDSNParams(dsn,
		"_pragma=journal_mode(WAL)&_pragma=auto_vacuum(incremental)&_pragma=busy_timeout(10000)&_pragma=synchronous(NORMAL)&_txlock=immediate"))
}
