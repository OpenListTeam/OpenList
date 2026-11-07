//go:build sqlite_cgo_compat || (linux && (mips || mips64 || mips64le || mipsle || loong64)) || (windows && 386)

package bootstrap

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	return sqlite.Open(appendDSNParams(dsn,
		"_journal=WAL&_vacuum=incremental&_busy_timeout=10000&_synchronous=NORMAL"))
}
