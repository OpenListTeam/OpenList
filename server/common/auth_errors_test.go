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
	if other.Code != 500 || other.CountAsAuthFailure || other.Message != "internal server error" {
		t.Fatalf("other = %+v", other)
	}
	if other.Message == "disk I/O error" {
		t.Fatal("client message leaked the database error")
	}
}

func TestClassifyAuthErrorEmptyUsernameIsCredentialFailure(t *testing.T) {
	got := ClassifyAuthError(errs.EmptyUsername)
	if got.Code != 401 || got.Message != model.InvalidUsernameOrPassword || !got.CountAsAuthFailure {
		t.Fatalf("ClassifyAuthError(empty username) = %+v, want 401 counted failure", got)
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

func TestClassifyAuthErrorLdapUnmatchedUserIsCredentialFailure(t *testing.T) {
	// HandleLdapLogin must mark "no matching LDAP user" as auth failure so
	// WebDAV/FTP/SFTP keep 401 + lockout counting (not 500).
	err := ldapUnmatchedUserError()
	if !errors.Is(err, ErrFailedLdapAuth) {
		t.Fatalf("ldapUnmatchedUserError() = %v, want ErrFailedLdapAuth", err)
	}
	got := ClassifyAuthError(err)
	if got.Code != 401 || got.Message != model.InvalidUsernameOrPassword || !got.CountAsAuthFailure {
		t.Fatalf("ClassifyAuthError(ldap unmatched) = %+v, want 401 counted failure", got)
	}
}

func TestClassifyAuthErrorLdapInfrastructureRemainsServerError(t *testing.T) {
	for _, err := range []error{
		pkgerr.WithMessagef(errors.New("connection refused"), "failed to connect to LDAP"),
		pkgerr.WithMessagef(errors.New("invalid credentials"), "failed to bind to LDAP"),
		pkgerr.WithMessagef(errors.New("timeout"), "failed login ldap: LDAP search failed"),
	} {
		got := ClassifyAuthError(err)
		if got.Code != 500 || got.CountAsAuthFailure {
			t.Fatalf("ClassifyAuthError(%v) = %+v, want 500 uncounted", err, got)
		}
	}
}
