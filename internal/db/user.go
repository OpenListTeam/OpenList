package db

import (
	"encoding/base64"
	"encoding/json"
	"strconv"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pkg/errors"
	"gorm.io/gorm"
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

func RegisterAuthn(u *model.User, credential *webauthn.Credential) error {
	if u == nil {
		return errors.New("user is nil")
	}
	exists := u.WebAuthnCredentials()
	if credential != nil {
		exists = append(exists, *credential)
	}
	res, err := utils.Json.Marshal(exists)
	if err != nil {
		return err
	}
	return UpdateAuthn(u.ID, string(res))
}

func RemoveAuthn(u *model.User, id string) error {
	exists := u.WebAuthnCredentials()
	for i := 0; i < len(exists); i++ {
		idEncoded := base64.StdEncoding.EncodeToString(exists[i].ID)
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

// migrateUserRoles normalizes the legacy `role` values that AList left behind.
//
// AList v3.46.0 and later model the user role as a list of ints and serialize it
// into a text column, e.g. `"[1]"` or `"[2]"`. OpenList has always used a single
// int, so a legacy row is no longer equal to the value we query with:
// `WHERE role = 2` never matches the text `[2]`. GetAdmin and GetGuest therefore
// both report ErrRecordNotFound, which means the administrator loses the admin
// panel and initGuest tries to insert a second `guest` row, hits the unique index
// on username and kills the process on startup.
func migrateUserRoles() error {
	stmt := &gorm.Statement{DB: db}
	if err := stmt.Parse(&model.User{}); err != nil {
		return err
	}
	table := stmt.Schema.Table
	var rows []struct {
		ID   uint
		Role string
	}
	// Scanning into string keeps this portable: text and blob columns on SQLite,
	// TEXT on MySQL, and both TEXT and integer columns on PostgreSQL all convert.
	if err := db.Table(table).Select(columnName("id") + ", " + columnName("role")).Find(&rows).Error; err != nil {
		return errors.WithMessage(err, "failed to read user roles")
	}
	migrated := 0
	for _, row := range rows {
		role, ok := convertLegacyRole(row.Role)
		if !ok {
			continue
		}
		if err := db.Table(table).Where(columnName("id")+" = ?", row.ID).Update(columnName("role"), role).Error; err != nil {
			return errors.WithMessagef(err, "failed to migrate role of user %d", row.ID)
		}
		migrated++
	}
	if migrated > 0 {
		utils.Log.Infof("[migrate user role] converted %d legacy AList role value(s)", migrated)
	}
	return nil
}

// convertLegacyRole turns an AList style role into the single OpenList role.
// The second return value reports whether the value actually needs rewriting.
func convertLegacyRole(raw string) (int, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}
	if _, err := strconv.Atoi(raw); err == nil {
		// Already stored in the current single-int form.
		return 0, false
	}
	var roles []int
	if err := json.Unmarshal([]byte(raw), &roles); err != nil {
		// Unknown format, leave it alone rather than guessing.
		return 0, false
	}
	// AList allowed several roles per user, OpenList keeps only one, so pick the
	// most privileged one present.
	for _, want := range []int{model.ADMIN, model.GUEST} {
		for _, role := range roles {
			if role == want {
				return want, true
			}
		}
	}
	return model.GENERAL, true
}
