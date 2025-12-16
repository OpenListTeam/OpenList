package model

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/OpenListTeam/go-cache"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/pkg/errors"
)

const (
	GENERAL = iota
	GUEST   // only one exists
	ADMIN
)

const StaticHashSalt = "https://github.com/alist-org/alist"

var LoginCache = cache.NewMemCache[int]()

var (
	DefaultLockDuration   = time.Minute * 5
	DefaultMaxAuthRetries = 5
)

type User struct {
	ID       uint   `json:"id" gorm:"primaryKey"`                      // unique key
	Username string `json:"username" gorm:"unique" binding:"required"` // username
	PwdHash  string `json:"-"`                                         // password hash
	PwdTS    int64  `json:"-"`                                         // password timestamp
	Salt     string `json:"-"`                                         // unique salt
	Password string `json:"password"`                                  // password
	BasePath string `json:"base_path"`                                 // base path
	Role     int    `json:"role"`                                      // user's role
	Disabled bool   `json:"disabled"`
	// Determine permissions by bit
	//   0:  can see hidden files
	//   1:  can access without password
	//   2:  can add offline download tasks
	//   3:  can mkdir and upload
	//   4:  can rename
	//   5:  can move
	//   6:  can copy
	//   7:  can remove
	//   8:  webdav read
	//   9:  webdav write
	//   10: ftp/sftp login and read
	//   11: ftp/sftp write
	//   12: can read archives
	//   13: can decompress archives
	//   14: can share
	Permission int32  `json:"permission"`
	OtpSecret  string `json:"-"`
	SsoID      string `json:"sso_id"` // unique by sso platform
	Authn      string `gorm:"type:text" json:"-"`
	AllowLdap  bool   `json:"allow_ldap" gorm:"default:true"`
}

func (u *User) IsGuest() bool {
	return u.Role == GUEST
}

func (u *User) IsAdmin() bool {
	return u.Role == ADMIN
}

func (u *User) ValidateRawPassword(password string) error {
	return u.ValidatePwdStaticHash(StaticHash(password))
}

func (u *User) ValidatePwdStaticHash(pwdStaticHash string) error {
	if pwdStaticHash == "" {
		return errors.WithStack(errs.EmptyPassword)
	}
	if u.PwdHash != HashPwd(pwdStaticHash, u.Salt) {
		return errors.WithStack(errs.WrongPassword)
	}
	return nil
}

func (u *User) SetPassword(pwd string) *User {
	u.Salt = random.String(16)
	u.PwdHash = TwoHashPwd(pwd, u.Salt)
	u.PwdTS = time.Now().Unix()
	return u
}

func CanSeeHides(permission int32) bool {
	return permission&1 == 1
}

func (u *User) CanSeeHides() bool {
	return CanSeeHides(u.Permission)
}

func CanAccessWithoutPassword(permission int32) bool {
	return (permission>>1)&1 == 1
}

func (u *User) CanAccessWithoutPassword() bool {
	return CanAccessWithoutPassword(u.Permission)
}

func CanAddOfflineDownloadTasks(permission int32) bool {
	return (permission>>2)&1 == 1
}

func (u *User) CanAddOfflineDownloadTasks() bool {
	return CanAddOfflineDownloadTasks(u.Permission)
}

func CanWrite(permission int32) bool {
	return (permission>>3)&1 == 1
}

func (u *User) CanWrite() bool {
	return CanWrite(u.Permission)
}

func CanRename(permission int32) bool {
	return (permission>>4)&1 == 1
}

func (u *User) CanRename() bool {
	return CanRename(u.Permission)
}

func CanMove(permission int32) bool {
	return (permission>>5)&1 == 1
}

func (u *User) CanMove() bool {
	return CanMove(u.Permission)
}

func CanCopy(permission int32) bool {
	return (permission>>6)&1 == 1
}

func (u *User) CanCopy() bool {
	return CanCopy(u.Permission)
}

func CanRemove(permission int32) bool {
	return (permission>>7)&1 == 1
}

func (u *User) CanRemove() bool {
	return CanRemove(u.Permission)
}

func CanWebdavRead(permission int32) bool {
	return (permission>>8)&1 == 1
}

func (u *User) CanWebdavRead() bool {
	return CanWebdavRead(u.Permission)
}

func CanWebdavManage(permission int32) bool {
	return (permission>>9)&1 == 1
}

func (u *User) CanWebdavManage() bool {
	return CanWebdavManage(u.Permission)
}

func CanFTPAccess(permission int32) bool {
	return (permission>>10)&1 == 1
}

func (u *User) CanFTPAccess() bool {
	return CanFTPAccess(u.Permission)
}

func CanFTPManage(permission int32) bool {
	return (permission>>11)&1 == 1
}

func (u *User) CanFTPManage() bool {
	return CanFTPManage(u.Permission)
}

func CanReadArchives(permission int32) bool {
	return (permission>>12)&1 == 1
}

func (u *User) CanReadArchives() bool {
	return CanReadArchives(u.Permission)
}

func CanDecompress(permission int32) bool {
	return (permission>>13)&1 == 1
}

func (u *User) CanDecompress() bool {
	return CanDecompress(u.Permission)
}

func CanShare(permission int32) bool {
	return (permission>>14)&1 == 1
}

func (u *User) CanShare() bool {
	return CanShare(u.Permission)
}

func (u *User) JoinPath(reqPath string) (string, error) {
	return utils.JoinBasePath(u.BasePath, reqPath)
}

func StaticHash(password string) string {
	return utils.HashData(utils.SHA256, []byte(fmt.Sprintf("%s-%s", password, StaticHashSalt)))
}

func HashPwd(static string, salt string) string {
	return utils.HashData(utils.SHA256, []byte(fmt.Sprintf("%s-%s", static, salt)))
}

func TwoHashPwd(password string, salt string) string {
	return HashPwd(StaticHash(password), salt)
}

func (u *User) WebAuthnID() []byte {
	bs := make([]byte, 8)
	binary.LittleEndian.PutUint64(bs, uint64(u.ID))
	return bs
}

func (u *User) WebAuthnName() string {
	return u.Username
}

func (u *User) WebAuthnDisplayName() string {
	return u.Username
}

func (u *User) WebAuthnCredentials() []webauthn.Credential {
	var res []webauthn.Credential
	err := json.Unmarshal([]byte(u.Authn), &res)
	if err != nil {
		fmt.Println(err)
	}
	return res
}

func (u *User) WebAuthnIcon() string {
	return "https://res.oplist.org/logo/logo.svg"
}
