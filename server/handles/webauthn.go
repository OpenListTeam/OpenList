package handles

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"

	"github.com/OpenListTeam/OpenList/v4/internal/authn"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	log "github.com/sirupsen/logrus"
)

const maxPasskeyResponseBytes = 1 << 20

func BeginAuthnLogin(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	enabled := setting.GetBool(conf.WebauthnLoginEnabled)
	if !enabled {
		common.ErrorStrResp(c, "WebAuthn is not enabled", 403)
		return
	}
	clientKey, err := authn.AdmissionClientKey(c.Request, conf.Conf.PasskeyTrustedProxies)
	if err != nil {
		log.WithError(err).Warn("passkey login client admission failed")
		common.ErrorResp(c, err, 400)
		return
	}
	admissionKey := "login:" + clientKey
	if !admitAuthnChallenge(c, authn.CeremonyLogin, admissionKey) {
		return
	}
	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	var (
		options     *protocol.CredentialAssertion
		sessionData *webauthn.SessionData
		userID      uint
	)
	if username := c.Query("username"); username != "" {
		var user *model.User
		user, err = db.GetUserByName(username)
		if err == nil {
			userID = user.ID
			options, sessionData, err = authnInstance.BeginLogin(
				user,
				webauthn.WithUserVerification(protocol.VerificationRequired),
			)
		}
	} else { // client-side discoverable login
		options, sessionData, err = authnInstance.BeginDiscoverableLogin(
			webauthn.WithUserVerification(protocol.VerificationRequired),
		)
	}
	if err != nil {
		log.WithError(err).Warn("passkey login challenge creation failed")
		common.ErrorResp(c, err, 400)
		return
	}

	sessionID, ok := storeAuthnChallenge(c, authn.Challenge{
		Session:  *sessionData,
		Ceremony: authn.CeremonyLogin,
		UserID:   userID,
	}, admissionKey)
	if !ok {
		return
	}
	common.SuccessResp(c, gin.H{
		"options": options,
		"session": sessionID,
	})
}

func FinishAuthnLogin(c *gin.Context) {
	limitPasskeyResponseBody(c)
	enabled := setting.GetBool(conf.WebauthnLoginEnabled)
	if !enabled {
		common.ErrorStrResp(c, "WebAuthn is not enabled", 403)
		return
	}
	var user *model.User
	var userID uint
	if username := c.Query("username"); username != "" {
		var err error
		user, err = db.GetUserByName(username)
		if err != nil {
			common.ErrorResp(c, err, 400)
			return
		}
		userID = user.ID
	}
	challenge, err := authn.ConsumeChallenge(c.GetHeader("session"), authn.CeremonyLogin, userID)
	if err != nil {
		log.WithError(err).Warn("passkey login challenge rejected")
		common.ErrorResp(c, err, 400)
		return
	}
	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	var credential *webauthn.Credential
	if user != nil {
		credential, err = authnInstance.FinishLogin(user, challenge.Session, c.Request)
	} else { // client-side discoverable login
		credential, err = authnInstance.FinishDiscoverableLogin(func(_, userHandle []byte) (webauthn.User, error) {
			if len(userHandle) != 8 {
				return nil, errors.New("invalid passkey user handle")
			}
			userID := uint(binary.LittleEndian.Uint64(userHandle))
			user, err = db.GetUserById(userID)
			if err != nil {
				return nil, err
			}

			return user, nil
		}, challenge.Session, c.Request)
	}
	if err != nil {
		log.WithError(err).Warn("passkey login verification failed")
		common.ErrorResp(c, err, 400)
		return
	}
	if user == nil || user.Disabled {
		common.ErrorStrResp(c, "passkey account is unavailable", 401)
		return
	}
	if credential == nil {
		log.Error("passkey login verification returned no credential")
		common.ErrorStrResp(c, "passkey verification returned no credential", 500)
		return
	}
	if credential.Authenticator.CloneWarning {
		log.WithField("user_id", user.ID).Warn("passkey login rejected due to a non-advancing signature counter")
		common.ErrorStrResp(c, "passkey signature counter did not advance", 401)
		return
	}
	if err = db.UpdateAuthnUsage(user.ID, credential); err != nil {
		if errors.Is(err, db.ErrPasskeyCounterDidNotAdvance) {
			log.WithError(err).WithField("user_id", user.ID).Warn("passkey login rejected due to a concurrent counter update")
			common.ErrorStrResp(c, err.Error(), 401)
			return
		}
		log.WithError(err).Error("passkey counter persistence failed")
		common.ErrorResp(c, err, 500)
		return
	}
	if err = op.DelUserCache(user.Username); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	token, err := common.GenerateToken(user)
	if err != nil {
		common.ErrorResp(c, err, 500, true)
		return
	}
	log.WithField("user_id", user.ID).Info("passkey login succeeded")
	common.SuccessResp(c, gin.H{"token": token})
}

func BeginAuthnRegistration(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	enabled := setting.GetBool(conf.WebauthnLoginEnabled)
	if !enabled {
		common.ErrorStrResp(c, "WebAuthn is not enabled", 403)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	admissionKey := fmt.Sprintf("registration:%d", user.ID)
	if !admitAuthnChallenge(c, authn.CeremonyRegistration, admissionKey) {
		return
	}
	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	options, sessionData, err := authnInstance.BeginRegistration(
		user,
		webauthn.WithResidentKeyRequirement(protocol.ResidentKeyRequirementRequired),
	)

	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	sessionID, ok := storeAuthnChallenge(c, authn.Challenge{
		Session:  *sessionData,
		Ceremony: authn.CeremonyRegistration,
		UserID:   user.ID,
	}, admissionKey)
	if !ok {
		return
	}

	common.SuccessResp(c, gin.H{
		"options": options,
		"session": sessionID,
	})
}

func storeAuthnChallenge(c *gin.Context, challenge authn.Challenge, admissionKey string) (string, bool) {
	sessionID, err := authn.StoreChallenge(challenge, admissionKey)
	if err == nil {
		return sessionID, true
	}

	status := 500
	if errors.Is(err, authn.ErrChallengeAdmission) {
		status = 429
	} else if errors.Is(err, authn.ErrChallengeCapacity) {
		status = 503
	}
	log.WithError(err).WithField("ceremony", challenge.Ceremony).Warn("passkey challenge admission failed")
	common.ErrorResp(c, err, status)
	return "", false
}

func admitAuthnChallenge(c *gin.Context, ceremony authn.Ceremony, admissionKey string) bool {
	if err := authn.AdmitChallenge(ceremony, admissionKey); err != nil {
		log.WithError(err).WithField("ceremony", ceremony).Warn("passkey challenge rate limit exceeded")
		common.ErrorResp(c, err, 429)
		return false
	}
	return true
}

func limitPasskeyResponseBody(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxPasskeyResponseBytes)
}

func FinishAuthnRegistration(c *gin.Context) {
	limitPasskeyResponseBody(c)
	enabled := setting.GetBool(conf.WebauthnLoginEnabled)
	if !enabled {
		common.ErrorStrResp(c, "WebAuthn is not enabled", 403)
		return
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	challenge, err := authn.ConsumeChallenge(
		c.GetHeader("session"),
		authn.CeremonyRegistration,
		user.ID,
	)
	if err != nil {
		log.WithError(err).Warn("passkey registration challenge rejected")
		common.ErrorResp(c, err, 400)
		return
	}
	authnInstance, err := authn.NewAuthnInstance(c)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}

	credential, err := authnInstance.FinishRegistration(user, challenge.Session, c.Request)

	if err != nil {
		log.WithError(err).Warn("passkey registration verification failed")
		common.ErrorResp(c, err, 400)
		return
	}
	err = db.RegisterAuthn(user, credential)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	err = op.DelUserCache(user.Username)
	if err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	log.WithField("user_id", user.ID).Info("passkey registered")
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
	type WebAuthnCredentials struct {
		ID          []byte `json:"id"`
		FingerPrint string `json:"fingerprint"`
	}
	user := c.Request.Context().Value(conf.UserKey).(*model.User)
	credentials := user.WebAuthnCredentials()
	res := make([]WebAuthnCredentials, 0, len(credentials))
	for _, v := range credentials {
		credential := WebAuthnCredentials{
			ID:          v.ID,
			FingerPrint: fmt.Sprintf("% X", v.Authenticator.AAGUID),
		}
		res = append(res, credential)
	}
	common.SuccessResp(c, res)
}
