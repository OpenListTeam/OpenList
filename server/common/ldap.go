package common

import (
	"crypto/tls"
	"fmt"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/pkg/errors"
	log "github.com/sirupsen/logrus"
	"gopkg.in/ldap.v3"
)

var ErrFailedLdapAuth = errors.New("failed to auth")

func HandleLdapLogin(username, password string) error {
	// Auth start
	ldapServer := setting.GetStr(conf.LdapServer)
	skipTlsVerify := setting.GetBool(conf.LdapSkipTlsVerify)
	ldapManagerDN := setting.GetStr(conf.LdapManagerDN)
	ldapManagerPassword := setting.GetStr(conf.LdapManagerPassword)
	ldapUserSearchBase := setting.GetStr(conf.LdapUserSearchBase)
	ldapUserSearchFilter := setting.GetStr(conf.LdapUserSearchFilter) // (uid=%s)

	// Connect to LdapServer
	l, err := dial(ldapServer, skipTlsVerify)
	if err != nil {
		return errors.WithMessagef(err, "failed to connect to LDAP")
	}
	defer l.Close()

	// First bind with a read only user
	if ldapManagerDN != "" && ldapManagerPassword != "" {
		err = l.Bind(ldapManagerDN, ldapManagerPassword)
		if err != nil {
			return errors.WithMessagef(err, "failed to bind to LDAP")
		}
	}

	// Search for the given username
	searchRequest := ldap.NewSearchRequest(
		ldapUserSearchBase,
		ldap.ScopeWholeSubtree, ldap.NeverDerefAliases, 0, 0, false,
		fmt.Sprintf(ldapUserSearchFilter, ldap.EscapeFilter(username)),
		[]string{"dn"},
		nil,
	)
	sr, err := l.Search(searchRequest)
	if err != nil {
		return errors.WithMessagef(err, "failed login ldap: LDAP search failed")
	}
	if len(sr.Entries) != 1 {
		return errors.New("failed login ldap: user does not exist or too many entries returned")
	}
	userDN := sr.Entries[0].DN

	// Bind as the user to verify their password
	err = l.Bind(userDN, password)
	if err != nil {
		return errors.WithMessagef(ErrFailedLdapAuth, "%v", err)
	}
	log.Infof("LDAP auth successful for %s", username)
	// Auth finished
	return nil
}

func LdapRegister(username string) (*model.User, error) {
	if username == "" {
		return nil, errors.New("cannot get username from ldap provider")
	}
	user := &model.User{
		Username:   username,
		Password:   "",
		Authn:      "[]",
		Permission: int32(setting.GetInt(conf.LdapDefaultPermission, 0)),
		BasePath:   setting.GetStr(conf.LdapDefaultDir),
		Role:       0,
		Disabled:   false,
		AllowLdap:  true,
	}
	user.SetPassword(random.String(16))
	if err := op.CreateUser(user); err != nil {
		return nil, err
	}
	return user, nil
}

func dial(ldapServer string, skipTlsVerify ...bool) (*ldap.Conn, error) {
	tlsEnabled := false
	if strings.HasPrefix(ldapServer, "ldaps://") {
		tlsEnabled = true
		ldapServer = strings.TrimPrefix(ldapServer, "ldaps://")
	} else if strings.HasPrefix(ldapServer, "ldap://") {
		ldapServer = strings.TrimPrefix(ldapServer, "ldap://")
	}

	if tlsEnabled {
		return ldap.DialTLS("tcp", ldapServer, &tls.Config{InsecureSkipVerify: utils.IsBool(skipTlsVerify...)})
	} else {
		return ldap.Dial("tcp", ldapServer)
	}
}
