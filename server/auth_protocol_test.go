package server

import (
	"errors"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	pkgerr "github.com/pkg/errors"
	"gorm.io/gorm"
)

func TestCanLdapAutoRegister(t *testing.T) {
	if !canLdapAutoRegister(pkgerr.Wrapf(gorm.ErrRecordNotFound, "failed find user")) {
		t.Fatal("missing local user should fall back to LDAP registration")
	}
	for _, err := range []error{
		errs.EmptyUsername,
		errors.New("database is locked (5) (SQLITE_BUSY)"),
		errors.New("disk I/O error"),
		nil,
	} {
		if canLdapAutoRegister(err) {
			t.Fatalf("canLdapAutoRegister(%v) = true", err)
		}
	}
}

func TestProtocolAuthError(t *testing.T) {
	busy := protocolAuthError(errors.New("database is locked (5) (SQLITE_BUSY)"))
	if !errors.Is(busy, errs.ErrDatabaseBusy) {
		t.Fatalf("busy = %v", busy)
	}
	internal := protocolAuthError(errors.New("disk I/O error"))
	if internal.Error() != "internal server error" {
		t.Fatalf("internal = %v", internal)
	}
	cred := protocolAuthError(errs.EmptyUsername)
	if !errors.Is(cred, errs.EmptyUsername) {
		t.Fatalf("credential error = %v", cred)
	}
}
