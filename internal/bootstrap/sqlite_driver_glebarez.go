//go:build !sqlite_cgo_compat && !(linux && (mips || mips64 || mipsle))

package bootstrap

import (
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openSQLite(dsn string) gorm.Dialector {
	return sqlite.Open(dsn)
}
