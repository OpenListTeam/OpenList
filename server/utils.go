package server

import (
	"errors"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

// canLdapAutoRegister is true only when the local user does not exist.
// Database failures and empty usernames must not fall through to LDAP.
func canLdapAutoRegister(lookupErr error) bool {
	return errs.IsRecordNotFound(lookupErr)
}

func protocolAuthError(err error) error {
	if errs.IsDatabaseBusy(err) {
		return errs.ErrDatabaseBusy
	}
	if common.ClassifyAuthError(err).Code >= 500 {
		return errors.New("internal server error")
	}
	return err
}

func tryLdapLoginAndRegister(user, pass string) (*model.User, error) {
	err := common.HandleLdapLogin(user, pass)
	if err != nil {
		return nil, err
	}
	return common.LdapRegister(user)
}
