package server

import (
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/server/common"
)

func tryLdapLoginAndRegister(user, pass string) (*model.User, error) {
	err := common.HandleLdapLogin(user, pass)
	if err != nil {
		return nil, err
	}
	return common.LdapRegister(user)
}
