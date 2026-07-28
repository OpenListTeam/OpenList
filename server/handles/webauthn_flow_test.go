package handles

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/OpenListTeam/OpenList/v4/server/middlewares"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/protocol/webauthncbor"
	"github.com/go-webauthn/webauthn/webauthn"
	"gorm.io/gorm"
)

const (
	passkeyHTTPTestChild = "OPENLIST_PASSKEY_HTTP_TEST_CHILD"
	passkeyTestOrigin    = "https://passkey.test"
	passkeyTestRPID      = "passkey.test"
)

type passkeyTestAuthenticator struct {
	privateKey   *ecdsa.PrivateKey
	credentialID []byte
	userHandle   []byte
	publicKey    []byte
}

type passkeyBeginData struct {
	Session string `json:"session"`
	Options struct {
		PublicKey struct {
			Challenge string `json:"challenge"`
			RPID      string `json:"rpId"`
			RP        struct {
				ID string `json:"id"`
			} `json:"rp"`
			UserVerification       protocol.UserVerificationRequirement `json:"userVerification"`
			AuthenticatorSelection struct {
				ResidentKey      protocol.ResidentKeyRequirement      `json:"residentKey"`
				UserVerification protocol.UserVerificationRequirement `json:"userVerification"`
			} `json:"authenticatorSelection"`
		} `json:"publicKey"`
	} `json:"options"`
}

func TestPasskeyHTTPHandlerLifecycle(t *testing.T) {
	if os.Getenv(passkeyHTTPTestChild) == "1" {
		testPasskeyHTTPHandlerLifecycle(t)
		return
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestPasskeyHTTPHandlerLifecycle$", "-test.count=1")
	cmd.Env = append(os.Environ(), passkeyHTTPTestChild+"=1")
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("passkey HTTP lifecycle subprocess failed: %v\n%s", err, output)
	}
}

func testPasskeyHTTPHandlerLifecycle(t *testing.T) {
	router, user, token, authenticator := setupPasskeyHTTPTest(t)

	wrongOriginBegin := beginRegistration(t, router, token)
	wrongOrigin := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_registration",
		authenticator.registrationResponse(t, wrongOriginBegin.Options.PublicKey.Challenge, "https://attacker.example", passkeyTestRPID, true),
		map[string]string{"Authorization": token, "session": wrongOriginBegin.Session},
	)
	requirePasskeyResponse(t, wrongOrigin, 400, "Error validating origin")
	requirePasskeyCredentialCount(t, user.ID, 0)

	wrongRPBegin := beginRegistration(t, router, token)
	wrongRP := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_registration",
		authenticator.registrationResponse(t, wrongRPBegin.Options.PublicKey.Challenge, passkeyTestOrigin, "wrong.example", true),
		map[string]string{"Authorization": token, "session": wrongRPBegin.Session},
	)
	requirePasskeyResponse(t, wrongRP, 400, "Error validating the authenticator response")
	requirePasskeyCredentialCount(t, user.ID, 0)

	missingUVBegin := beginRegistration(t, router, token)
	missingUV := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_registration",
		authenticator.registrationResponse(
			t,
			missingUVBegin.Options.PublicKey.Challenge,
			passkeyTestOrigin,
			passkeyTestRPID,
			false,
		),
		map[string]string{"Authorization": token, "session": missingUVBegin.Session},
	)
	requirePasskeyResponse(t, missingUV, 400, "")
	requirePasskeyCredentialCount(t, user.ID, 0)

	registrationBegin := beginRegistration(t, router, token)
	registrationBody := authenticator.registrationResponse(
		t,
		registrationBegin.Options.PublicKey.Challenge,
		passkeyTestOrigin,
		passkeyTestRPID,
		true,
	)
	registration := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_registration",
		registrationBody,
		map[string]string{"Authorization": token, "session": registrationBegin.Session},
	)
	requirePasskeyResponse(t, registration, 200, "")

	credentials := requirePasskeyCredentialCount(t, user.ID, 1)
	if !bytes.Equal(credentials[0].PublicKey, authenticator.publicKey) {
		t.Fatalf("registered credential = %#v", credentials[0])
	}

	registrationReplay := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_registration",
		registrationBody,
		map[string]string{"Authorization": token, "session": registrationBegin.Session},
	)
	requirePasskeyResponse(t, registrationReplay, 400, "passkey challenge is invalid, expired, or already used")
	requirePasskeyCredentialCount(t, user.ID, 1)

	wrongLoginOriginBegin := beginLogin(t, router)
	wrongLoginOrigin := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		authenticator.assertionResponse(
			t,
			wrongLoginOriginBegin.Options.PublicKey.Challenge,
			"https://attacker.example",
			passkeyTestRPID,
			1,
			true,
		),
		map[string]string{"session": wrongLoginOriginBegin.Session},
	)
	requirePasskeyResponse(t, wrongLoginOrigin, 400, "Error validating origin")
	requirePasskeyCounter(t, user.ID, 0)

	wrongLoginRPBegin := beginLogin(t, router)
	wrongLoginRP := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		authenticator.assertionResponse(
			t,
			wrongLoginRPBegin.Options.PublicKey.Challenge,
			passkeyTestOrigin,
			"wrong.example",
			1,
			true,
		),
		map[string]string{"session": wrongLoginRPBegin.Session},
	)
	requirePasskeyResponse(t, wrongLoginRP, 400, "Error validating the authenticator response")
	requirePasskeyCounter(t, user.ID, 0)

	missingLoginUVBegin := beginLogin(t, router)
	missingLoginUV := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		authenticator.assertionResponse(
			t,
			missingLoginUVBegin.Options.PublicKey.Challenge,
			passkeyTestOrigin,
			passkeyTestRPID,
			1,
			false,
		),
		map[string]string{"session": missingLoginUVBegin.Session},
	)
	requirePasskeyResponse(t, missingLoginUV, 400, "")
	requirePasskeyCounter(t, user.ID, 0)

	loginBegin := beginLogin(t, router)
	loginBody := authenticator.assertionResponse(
		t,
		loginBegin.Options.PublicKey.Challenge,
		passkeyTestOrigin,
		passkeyTestRPID,
		1,
		true,
	)
	login := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		loginBody,
		map[string]string{"session": loginBegin.Session},
	)
	requirePasskeyResponse(t, login, 200, "")
	requireResponseToken(t, login)
	requirePasskeyCounter(t, user.ID, 1)

	loginReplay := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		loginBody,
		map[string]string{"session": loginBegin.Session},
	)
	requirePasskeyResponse(t, loginReplay, 400, "passkey challenge is invalid, expired, or already used")
	requirePasskeyCounter(t, user.ID, 1)

	revokedLoginBegin := beginLogin(t, router)
	revoke := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/delete_authn",
		mustJSON(t, map[string]string{
			"id": base64.StdEncoding.EncodeToString(authenticator.credentialID),
		}),
		map[string]string{"Authorization": token},
	)
	requirePasskeyResponse(t, revoke, 200, "")
	requirePasskeyCredentialCount(t, user.ID, 0)

	revoked := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/authn/webauthn_finish_login",
		authenticator.assertionResponse(
			t,
			revokedLoginBegin.Options.PublicKey.Challenge,
			passkeyTestOrigin,
			passkeyTestRPID,
			2,
			true,
		),
		map[string]string{"session": revokedLoginBegin.Session},
	)
	requirePasskeyResponse(t, revoked, 400, "credential")

	password := performPasskeyRequest(
		t,
		router,
		http.MethodPost,
		"/api/auth/login",
		[]byte(`{"username":"passkey-handler-user","password":"still-works"}`),
		nil,
	)
	requirePasskeyResponse(t, password, 200, "")
	requireResponseToken(t, password)
}

func setupPasskeyHTTPTest(
	t *testing.T,
) (*gin.Engine, *model.User, string, *passkeyTestAuthenticator) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	testDir := t.TempDir()
	testID := sha256.Sum256([]byte(testDir))
	userID := uint(binary.BigEndian.Uint32(testID[:4]) | 1)
	clientAddress := fmt.Sprintf(
		"[2001:db8:%x:%x::1]:1234",
		binary.BigEndian.Uint16(testID[4:6]),
		binary.BigEndian.Uint16(testID[6:8]),
	)
	previousConfig := conf.Conf
	previousSecret := common.SecretKey
	conf.Conf = conf.DefaultConfig(testDir)
	conf.Conf.SiteURL = passkeyTestOrigin
	conf.Conf.TokenExpiresIn = 1
	common.SecretKey = []byte("passkey-handler-test-secret")
	t.Cleanup(func() {
		conf.Conf = previousConfig
		common.SecretKey = previousSecret
		op.Cache.ClearAll()
	})

	testDB, err := gorm.Open(sqlite.Open(filepath.Join(testDir, "passkey.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	db.Init(testDB)
	sqlDB, err := testDB.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := sqlDB.Close(); err != nil {
			t.Errorf("close passkey test database: %v", err)
		}
	})
	if err = db.SaveSettingItems([]model.SettingItem{
		{Key: conf.WebauthnLoginEnabled, Value: "true"},
		{Key: conf.SiteTitle, Value: "OpenList"},
		{Key: conf.Token, Value: "test-admin-token-that-does-not-match"},
	}); err != nil {
		t.Fatal(err)
	}
	op.Cache.ClearAll()

	user := &model.User{ID: userID, Username: "passkey-handler-user", Authn: "[]"}
	user.SetPassword("still-works")
	if err = db.CreateUser(user); err != nil {
		t.Fatal(err)
	}
	token, err := common.GenerateToken(user)
	if err != nil {
		t.Fatal(err)
	}

	router := gin.New()
	router.Use(func(c *gin.Context) {
		c.Request.RemoteAddr = clientAddress
		common.GinAppendValues(c, conf.ApiUrlKey, passkeyTestOrigin)
		c.Next()
	})
	api := router.Group("/api")
	api.POST("/auth/login", Login)
	api.GET("/authn/webauthn_begin_login", BeginAuthnLogin)
	api.POST("/authn/webauthn_finish_login", FinishAuthnLogin)
	passkeys := api.Group("/authn", middlewares.Auth(false), middlewares.AuthNotGuest)
	passkeys.GET("/webauthn_begin_registration", BeginAuthnRegistration)
	passkeys.POST("/webauthn_finish_registration", FinishAuthnRegistration)
	passkeys.POST("/delete_authn", DeleteAuthnLogin)

	return router, user, token, newPasskeyTestAuthenticator(t, user.WebAuthnID())
}

func newPasskeyTestAuthenticator(t *testing.T, userHandle []byte) *passkeyTestAuthenticator {
	t.Helper()
	curve := elliptic.P256()
	privateScalar := big.NewInt(1)
	x, y := curve.ScalarBaseMult(privateScalar.Bytes())
	privateKey := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{Curve: curve, X: x, Y: y},
		D:         privateScalar,
	}
	publicKey, err := webauthncbor.Marshal(map[int]any{
		1:  2,
		3:  -7,
		-1: 1,
		-2: paddedCoordinate(x),
		-3: paddedCoordinate(y),
	})
	if err != nil {
		t.Fatal(err)
	}
	return &passkeyTestAuthenticator{
		privateKey:   privateKey,
		credentialID: []byte("openlist-passkey-handler-credential"),
		userHandle:   userHandle,
		publicKey:    publicKey,
	}
}

func (a *passkeyTestAuthenticator) registrationResponse(
	t *testing.T,
	challenge, origin, rpID string,
	userVerified bool,
) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rpID))
	authenticatorData := append([]byte{}, rpHash[:]...)
	flags := protocol.FlagUserPresent | protocol.FlagAttestedCredentialData
	if userVerified {
		flags |= protocol.FlagUserVerified
	}
	authenticatorData = append(authenticatorData, byte(flags))
	authenticatorData = binary.BigEndian.AppendUint32(authenticatorData, 0)
	authenticatorData = append(authenticatorData, make([]byte, 16)...)
	authenticatorData = binary.BigEndian.AppendUint16(authenticatorData, uint16(len(a.credentialID)))
	authenticatorData = append(authenticatorData, a.credentialID...)
	authenticatorData = append(authenticatorData, a.publicKey...)

	attestationObject, err := webauthncbor.Marshal(map[string]any{
		"fmt":      "none",
		"attStmt":  map[string]any{},
		"authData": authenticatorData,
	})
	if err != nil {
		t.Fatal(err)
	}
	clientData := mustJSON(t, map[string]string{
		"challenge": challenge,
		"origin":    origin,
		"type":      string(protocol.CreateCeremony),
	})
	return mustJSON(t, map[string]any{
		"id":                      base64.RawURLEncoding.EncodeToString(a.credentialID),
		"rawId":                   base64.RawURLEncoding.EncodeToString(a.credentialID),
		"type":                    "public-key",
		"authenticatorAttachment": "platform",
		"clientExtensionResults":  map[string]any{},
		"response": map[string]any{
			"attestationObject": base64.RawURLEncoding.EncodeToString(attestationObject),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"transports":        []string{"internal"},
		},
	})
}

func (a *passkeyTestAuthenticator) assertionResponse(
	t *testing.T,
	challenge, origin, rpID string,
	counter uint32,
	userVerified bool,
) []byte {
	t.Helper()
	rpHash := sha256.Sum256([]byte(rpID))
	authenticatorData := append([]byte{}, rpHash[:]...)
	flags := protocol.FlagUserPresent
	if userVerified {
		flags |= protocol.FlagUserVerified
	}
	authenticatorData = append(authenticatorData, byte(flags))
	authenticatorData = binary.BigEndian.AppendUint32(authenticatorData, counter)
	clientData := mustJSON(t, map[string]string{
		"challenge": challenge,
		"origin":    origin,
		"type":      string(protocol.AssertCeremony),
	})
	clientDataHash := sha256.Sum256(clientData)
	signedData := append(append([]byte{}, authenticatorData...), clientDataHash[:]...)
	digest := sha256.Sum256(signedData)
	signature, err := ecdsa.SignASN1(rand.Reader, a.privateKey, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	return mustJSON(t, map[string]any{
		"id":                     base64.RawURLEncoding.EncodeToString(a.credentialID),
		"rawId":                  base64.RawURLEncoding.EncodeToString(a.credentialID),
		"type":                   "public-key",
		"clientExtensionResults": map[string]any{},
		"response": map[string]any{
			"authenticatorData": base64.RawURLEncoding.EncodeToString(authenticatorData),
			"clientDataJSON":    base64.RawURLEncoding.EncodeToString(clientData),
			"signature":         base64.RawURLEncoding.EncodeToString(signature),
			"userHandle":        base64.RawURLEncoding.EncodeToString(a.userHandle),
		},
	})
}

func beginRegistration(t *testing.T, router *gin.Engine, token string) passkeyBeginData {
	t.Helper()
	response := performPasskeyRequest(
		t,
		router,
		http.MethodGet,
		"/api/authn/webauthn_begin_registration",
		nil,
		map[string]string{"Authorization": token},
	)
	requirePasskeyResponse(t, response, 200, "")
	begin := decodePasskeyBegin(t, response)
	if begin.Session == "" ||
		begin.Options.PublicKey.Challenge == "" ||
		begin.Options.PublicKey.RP.ID != passkeyTestRPID ||
		begin.Options.PublicKey.AuthenticatorSelection.ResidentKey != protocol.ResidentKeyRequirementRequired ||
		begin.Options.PublicKey.AuthenticatorSelection.UserVerification != protocol.VerificationRequired {
		t.Fatalf("registration options = %#v", begin)
	}
	return begin
}

func beginLogin(t *testing.T, router *gin.Engine) passkeyBeginData {
	t.Helper()
	response := performPasskeyRequest(
		t,
		router,
		http.MethodGet,
		"/api/authn/webauthn_begin_login",
		nil,
		nil,
	)
	requirePasskeyResponse(t, response, 200, "")
	begin := decodePasskeyBegin(t, response)
	if begin.Session == "" ||
		begin.Options.PublicKey.Challenge == "" ||
		begin.Options.PublicKey.RPID != passkeyTestRPID ||
		begin.Options.PublicKey.UserVerification != protocol.VerificationRequired {
		t.Fatalf("login options = %#v", begin)
	}
	return begin
}

func decodePasskeyBegin(
	t *testing.T,
	response common.Resp[json.RawMessage],
) passkeyBeginData {
	t.Helper()
	var begin passkeyBeginData
	if err := json.Unmarshal(response.Data, &begin); err != nil {
		t.Fatal(err)
	}
	return begin
}

func performPasskeyRequest(
	t *testing.T,
	router *gin.Engine,
	method, target string,
	body []byte,
	headers map[string]string,
) common.Resp[json.RawMessage] {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	router.ServeHTTP(recorder, request)

	var response common.Resp[json.RawMessage]
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response %q: %v", recorder.Body.String(), err)
	}
	return response
}

func requirePasskeyResponse(
	t *testing.T,
	response common.Resp[json.RawMessage],
	code int,
	message string,
) {
	t.Helper()
	if response.Code != code {
		t.Fatalf("response code = %d, want %d: %s", response.Code, code, response.Message)
	}
	if message != "" && !strings.Contains(response.Message, message) {
		t.Fatalf("response message = %q, want it to contain %q", response.Message, message)
	}
}

func requireResponseToken(t *testing.T, response common.Resp[json.RawMessage]) {
	t.Helper()
	var data struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal(response.Data, &data); err != nil {
		t.Fatal(err)
	}
	if data.Token == "" {
		t.Fatal("successful login returned an empty token")
	}
}

func requirePasskeyCredentialCount(
	t *testing.T,
	userID uint,
	count int,
) []webauthn.Credential {
	t.Helper()
	user, err := db.GetUserById(userID)
	if err != nil {
		t.Fatal(err)
	}
	credentials := user.WebAuthnCredentials()
	if len(credentials) != count {
		t.Fatalf("credential count = %d, want %d", len(credentials), count)
	}
	return credentials
}

func requirePasskeyCounter(t *testing.T, userID uint, counter uint32) {
	t.Helper()
	credentials := requirePasskeyCredentialCount(t, userID, 1)
	if credentials[0].Authenticator.SignCount != counter {
		t.Fatalf("credential counter = %d, want %d", credentials[0].Authenticator.SignCount, counter)
	}
}

func paddedCoordinate(value *big.Int) []byte {
	coordinate := make([]byte, 32)
	value.FillBytes(coordinate)
	return coordinate
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}
