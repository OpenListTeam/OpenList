//go:build sqlite_cgo_compat || (linux && (mips || mips64 || mipsle))

package bootstrap

import (
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}
