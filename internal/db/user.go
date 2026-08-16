package db

import (
	"encoding/base64"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pkg/errors"
)

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
	return errors.WithStack(db.Save(u).Error)
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

func RegisterAuthn(u *model.User, credential *webauthn.Credential, residentKey, creatorIP, creatorUA string) error {
	if u == nil {
		return errors.New("user is nil")
	}
	exists := u.WebAuthnCredentialRecords()
	if credential != nil {
		exists = append(exists, model.WebAuthnCredentialRecord{
			Credential:  *credential,
			ResidentKey: residentKey,
			CreatorIP:   creatorIP,
			CreatorUA:   creatorUA,
		})
	}
	res, err := utils.Json.Marshal(exists)
	if err != nil {
		return err
	}
	return UpdateAuthn(u.ID, string(res))
}

func RemoveAuthn(u *model.User, id string) error {
	exists := u.WebAuthnCredentialRecords()
	for i := 0; i < len(exists); i++ {
		idEncoded := base64.StdEncoding.EncodeToString(exists[i].Credential.ID)
		if idEncoded == id {
			exists[len(exists)-1], exists[i] = exists[i], exists[len(exists)-1]
			exists = exists[:len(exists)-1]
			break
		}
	}

	res, err := utils.Json.Marshal(exists)
	if err != nil {
		return err
	}
	return UpdateAuthn(u.ID, string(res))
}

func HasLegacyAuthnCredentials() (bool, error) {
	var users []model.User
	err := db.Select("id", "authn").Where("authn <> '' AND authn <> '[]'").Find(&users).Error
	if err != nil {
		return false, err
	}
	for i := range users {
		if users[i].HasLegacyWebAuthnCredential() {
			return true, nil
		}
	}
	return false, nil
}
