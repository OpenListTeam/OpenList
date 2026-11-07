package errs

import (
	"errors"
	"fmt"
	"testing"

	pkgerr "github.com/pkg/errors"
	"gorm.io/gorm"
)

func TestIsDatabaseBusy(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"unrelated", errors.New("boom"), false},
		{"sentinel", ErrDatabaseBusy, true},
		{"wrapped sentinel", fmt.Errorf("save: %w", ErrDatabaseBusy), true},
		{"locked", errors.New("failed update storage in database: database is locked (5) (SQLITE_BUSY)"), true},
		{"nested tx", errors.New("SQL logic error: cannot start a transaction within a transaction (1)"), true},
		{"pkg wrap locked", pkgerr.Wrap(errors.New("database is locked (5)"), "failed find user"), true},
		{"record not found", gorm.ErrRecordNotFound, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsDatabaseBusy(tc.err); got != tc.want {
				t.Fatalf("IsDatabaseBusy(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

func TestIsRecordNotFound(t *testing.T) {
	if !IsRecordNotFound(gorm.ErrRecordNotFound) {
		t.Fatal("expected gorm.ErrRecordNotFound")
	}
	if !IsRecordNotFound(pkgerr.Wrapf(gorm.ErrRecordNotFound, "failed find user")) {
		t.Fatal("expected wrapped record not found")
	}
	if IsRecordNotFound(errors.New("database is locked")) {
		t.Fatal("busy should not be treated as not found")
	}
	if IsRecordNotFound(nil) {
		t.Fatal("nil")
	}
}
