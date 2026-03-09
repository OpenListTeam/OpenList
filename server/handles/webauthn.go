package handles

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/authn"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
)

func BeginAuthnLogin(c *gin.Context) {
	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	allowCredentials := c.DefaultQuery("allowCredentials", "")
	username := c.Query("username")
	var (
		options         *protocol.CredentialAssertion
		sessionData     *webauthn.SessionData
		requireUsername bool
	)
	switch allowCredentials {
	case "yes":
		requireUsername = true
		if username != "" {
			var user *model.User
			user, err = db.GetUserByName(username)
			if err == nil {
				options, sessionData, err = authnInstance.BeginLogin(user)
			}
		}
	case "no":
		options, sessionData, err = authnInstance.BeginDiscoverableLogin()
	default:
		if username != "" {
			var user *model.User
			user, err = db.GetUserByName(username)
			if err == nil {
				options, sessionData, err = authnInstance.BeginLogin(user)
			}
		} else { // client-side discoverable login
			requireUsername, err = db.HasLegacyAuthnCredentials()
			if err == nil && !requireUsername {
				options, sessionData, err = authnInstance.BeginDiscoverableLogin()
			}
		}
	}
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	if requireUsername && username == "" {
		common.SuccessResp(c, gin.H{"require_username": true})
		return
	}

	val, err := json.Marshal(sessionData)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, gin.H{
		"options":          options,
		"session":          val,
		"require_username": requireUsername,
	})
}

func LegacyAuthnStatus(c *gin.Context) {
	hasLegacy, err := db.HasLegacyAuthnCredentials()
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, gin.H{"has_legacy": hasLegacy})
}

func FinishAuthnLogin(c *gin.Context) {
	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	sessionDataString := c.GetHeader("session")
	sessionDataBytes, err := base64.StdEncoding.DecodeString(sessionDataString)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	var sessionData webauthn.SessionData
	if err := json.Unmarshal(sessionDataBytes, &sessionData); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	var user *model.User
	if username := c.Query("username"); username != "" {
		user, err = db.GetUserByName(username)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		_, err = authnInstance.FinishLogin(user, sessionData, c.Request)
	} else { // client-side discoverable login
		_, err = authnInstance.FinishDiscoverableLogin(func(_, userHandle []byte) (webauthn.User, error) {
			// first param `rawID` in this callback function is equal to ID in webauthn.Credential,
			// but it's unnnecessary to check it.
			// userHandle param is equal to (User).WebAuthnID().
			userID := uint(binary.LittleEndian.Uint64(userHandle))
			user, err = db.GetUserById(userID)
			if err != nil {
				return nil, err
			}

			return user, nil
		}, sessionData, c.Request)
	}
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	token, err := common.GenerateToken(user)
	if err != nil {
		common.ErrorResp(c, err, 400, true)
		return
	}
	common.SuccessResp(c, gin.H{"token": token})
}

func BeginAuthnRegistration(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	if user.HasLegacyWebAuthnCredential() && c.Query("upgrade") != "yes" {
		common.ErrorStrResp(c, "legacy security key detected, please upgrade or delete it first", 400)
		return
	}

	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
	}

	options, sessionData, err := authnInstance.BeginRegistration(
		user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)

	if err != nil {
		common.ErrorResp(c, err, 400)
	}

	val, err := json.Marshal(sessionData)
	if err != nil {
		common.ErrorResp(c, err, 400)
	}

	common.SuccessResp(c, gin.H{
		"options": options,
		"session": val,
	})
}

func FinishAuthnRegistration(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	sessionDataString := c.GetHeader("Session")

	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	sessionDataBytes, err := base64.StdEncoding.DecodeString(sessionDataString)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	var sessionData webauthn.SessionData
	if err := json.Unmarshal(sessionDataBytes, &sessionData); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	credential, err := authnInstance.FinishRegistration(user, sessionData, c.Request)

	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	err = db.RegisterAuthn(
		user,
		credential,
		string(protocol.ResidentKeyRequirementRequired),
		c.ClientIP(),
		c.Request.UserAgent(),
	)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	err = op.DelUserCache(user.Username)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, "Registered Successfully")
}

func DeleteAuthnLogin(c *gin.Context) {
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	type DeleteAuthnReq struct {
		ID string `json:"id"`
	}
	var req DeleteAuthnReq
	err := c.ShouldBind(&req)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	err = db.RemoveAuthn(user, req.ID)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	err = op.DelUserCache(user.Username)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	common.SuccessResp(c, "Deleted Successfully")
}

func GetAuthnCredentials(c *gin.Context) {
	type PasskeyCredentials struct {
		ID          []byte `json:"id"`
		FingerPrint string `json:"fingerprint"`
		CreatorIP   string `json:"creator_ip"`
		CreatorUA   string `json:"creator_ua"`
		IsLegacy    bool   `json:"is_legacy"`
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	credentials := user.WebAuthnCredentials()
	records := user.WebAuthnCredentialRecords()
	res := make([]PasskeyCredentials, 0, len(credentials))
	for _, v := range credentials {
		var creatorIP string
		var creatorUA string
		var isLegacy bool
		for i := range records {
			if string(records[i].Credential.ID) == string(v.ID) {
				creatorIP = records[i].CreatorIP
				creatorUA = records[i].CreatorUA
				isLegacy = records[i].ResidentKey == string(protocol.ResidentKeyRequirementDiscouraged)
				break
			}
		}
		credential := PasskeyCredentials{
			ID:          v.ID,
			FingerPrint: fmt.Sprintf("% X", v.Authenticator.AAGUID),
			CreatorIP:   creatorIP,
			CreatorUA:   creatorUA,
			IsLegacy:    isLegacy,
		}
		res = append(res, credential)
	}
	common.SuccessResp(c, res)
}
