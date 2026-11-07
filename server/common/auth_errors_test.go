package common

import (
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	pkgerr "github.com/pkg/errors"
	"gorm.io/gorm"
)

func TestClassifyUserLookupError(t *testing.T) {
	busy := ClassifyUserLookupError(errors.New("database is locked (5) (SQLITE_BUSY)"))
	if busy.Code != 503 || busy.Message != model.DatabaseTemporarilyUnavailable || busy.CountAsAuthFailure {
		t.Fatalf("busy = %+v", busy)
	}

	nested := ClassifyUserLookupError(errors.New("cannot start a transaction within a transaction (1)"))
	if nested.Code != 503 || nested.CountAsAuthFailure {
		t.Fatalf("nested = %+v", nested)
	}

	missing := ClassifyUserLookupError(pkgerr.Wrapf(gorm.ErrRecordNotFound, "failed find user"))
	if missing.Code != 401 || missing.Message != model.InvalidUsernameOrPassword || !missing.CountAsAuthFailure {
		t.Fatalf("missing = %+v", missing)
	}

	other := ClassifyUserLookupError(errors.New("disk I/O error"))
	if other.Code != 500 || other.CountAsAuthFailure {
		t.Fatalf("other = %+v", other)
	}
}

func TestClassifyAuthErrorCredentialFailures(t *testing.T) {
	for _, err := range []error{errs.WrongPassword, errs.EmptyPassword, ErrFailedLdapAuth} {
		got := ClassifyAuthError(err)
		if got.Code != 401 || !got.CountAsAuthFailure {
			t.Fatalf("ClassifyAuthError(%v) = %+v, want 401 counted failure", err, got)
		}
	}
}
