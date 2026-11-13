package errs

import (
	"errors"
	"strings"

	pkgerr "github.com/pkg/errors"
	"gorm.io/gorm"
)

// ErrDatabaseBusy is returned (or detected) when SQLite/GORM cannot proceed
// because of lock contention or a nested-transaction pool poison.
var ErrDatabaseBusy = errors.New("database temporarily unavailable")

// IsDatabaseBusy reports whether err indicates SQLite lock contention or a
// nested-transaction failure that should be surfaced as a transient 5xx,
// not as an authentication failure.
func IsDatabaseBusy(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrDatabaseBusy) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "database is locked") ||
		strings.Contains(msg, "database table is locked") ||
		strings.Contains(msg, "database schema is locked") ||
		strings.Contains(msg, "sqlite_busy") ||
		strings.Contains(msg, "sqlite_locked") ||
		strings.Contains(msg, "cannot start a transaction within a transaction")
}

// IsRecordNotFound reports whether err is (or wraps) gorm.ErrRecordNotFound.
func IsRecordNotFound(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, gorm.ErrRecordNotFound) ||
		errors.Is(pkgerr.Cause(err), gorm.ErrRecordNotFound)
}
