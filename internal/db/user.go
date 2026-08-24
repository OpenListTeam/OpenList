package db

import (
	"bytes"
	"encoding/base64"
	"sync"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pkg/errors"
)

// A process-wide lock matches the supported single-process auth topology.
// Multi-replica deployments need database row locks together with a shared challenge store.
var passkeyMu sync.Mutex

var ErrPasskeyCounterDidNotAdvance = errors.New("passkey signature counter did not advance")

func GetUserByRole(role int) (*model.User, error) {
	user := model.User{Role: role}
	if err := db.Where(user).Take(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func GetUserByName(username string) (*model.User, error) {
	user := model.User{Username: username}
	if err := db.Where(user).First(&user).Error; err != nil {
		return nil, errors.Wrapf(err, "failed find user")
	}
	return &user, nil
}

func GetUserBySSOID(ssoID string) (*model.User, error) {
	user := model.User{SsoID: ssoID}
	if err := db.Where(user).First(&user).Error; err != nil {
		return nil, errors.Wrapf(err, "The single sign on platform is not bound to any users")
	}
	return &user, nil
}

func GetUserById(id uint) (*model.User, error) {
	var u model.User
	if err := db.First(&u, id).Error; err != nil {
		return nil, errors.Wrapf(err, "failed get old user")
	}
	return &u, nil
}

func CreateUser(u *model.User) error {
	return errors.WithStack(db.Create(u).Error)
}

func UpdateUser(u *model.User) error {
	// Authn has its own read-modify-write path and must not be overwritten by stale user snapshots.
	return errors.WithStack(db.Omit("authn").Save(u).Error)
}

func GetUsers(pageIndex, pageSize int) (users []model.User, count int64, err error) {
	userDB := db.Model(&model.User{})
	if err := userDB.Count(&count).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get users count")
	}
	if err := userDB.Order(columnName("id")).Offset((pageIndex - 1) * pageSize).Limit(pageSize).Find(&users).Error; err != nil {
		return nil, 0, errors.Wrapf(err, "failed get find users")
	}
	return users, count, nil
}

func DeleteUserById(id uint) error {
	return errors.WithStack(db.Delete(&model.User{}, id).Error)
}

func UpdateAuthn(userID uint, authn string) error {
	return db.Model(&model.User{ID: userID}).Update("authn", authn).Error
}

func RegisterAuthn(u *model.User, credential *webauthn.Credential) error {
	if u == nil {
		return errors.New("user is nil")
	}
	if credential == nil {
		return errors.New("credential is nil")
	}
	passkeyMu.Lock()
	defer passkeyMu.Unlock()

	current, err := GetUserById(u.ID)
	if err != nil {
		return err
	}
	exists := current.WebAuthnCredentials()
	for i := range exists {
		if bytes.Equal(exists[i].ID, credential.ID) {
			return errors.New("credential is already registered")
		}
	}
	exists = append(exists, *credential)
	res, err := utils.Json.Marshal(exists)
	if err != nil {
		return err
	}
	return UpdateAuthn(u.ID, string(res))
}

func RemoveAuthn(u *model.User, id string) error {
	if u == nil {
		return errors.New("user is nil")
	}
	passkeyMu.Lock()
	defer passkeyMu.Unlock()

	current, err := GetUserById(u.ID)
	if err != nil {
		return err
	}
	exists := current.WebAuthnCredentials()
	found := false
	for i := 0; i < len(exists); i++ {
		idEncoded := base64.StdEncoding.EncodeToString(exists[i].ID)
		if idEncoded == id {
			exists = append(exists[:i], exists[i+1:]...)
			found = true
			break
		}
	}
	if !found {
		return errors.New("credential not found or already revoked")
	}
	res, err := utils.Json.Marshal(exists)
	if err != nil {
		return err
	}
	return UpdateAuthn(u.ID, string(res))
}

func UpdateAuthnUsage(userID uint, credential *webauthn.Credential) error {
	if credential == nil {
		return errors.New("credential is nil")
	}
	passkeyMu.Lock()
	defer passkeyMu.Unlock()

	current, err := GetUserById(userID)
	if err != nil {
		return err
	}
	exists := current.WebAuthnCredentials()
	for i := range exists {
		if bytes.Equal(exists[i].ID, credential.ID) {
			storedCount := exists[i].Authenticator.SignCount
			incomingCount := credential.Authenticator.SignCount
			if (storedCount != 0 || incomingCount != 0) && incomingCount <= storedCount {
				return ErrPasskeyCounterDidNotAdvance
			}
			exists[i] = *credential
			res, err := utils.Json.Marshal(exists)
			if err != nil {
				return err
			}
			return UpdateAuthn(userID, string(res))
		}
	}
	return errors.New("credential not found or already revoked")
}
