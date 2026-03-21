package doubao_new

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/cookie"
	"github.com/go-resty/resty/v2"
	"github.com/google/uuid"
	"golang.org/x/crypto/pbkdf2"
)

type Clock interface {
	Now() (int64, error)
}

type SystemClock struct{}

func (SystemClock) Now() (int64, error) { return time.Now().Unix(), nil }

type DPoPTokenInput struct {
	KeyPair   *ecdsa.PrivateKey
	ExpiresIn int64 // 默认 15

	JTI   string
	HTM   string
	HTU   string
	IAT   int64
	Nonce string
	Clock Clock
}

type DPoPTokenOutput struct {
	DPoPToken   string `json:"dpopToken"`
	ExpiredTime int64  `json:"expiredTime"`
	ExpiresIn   int64  `json:"expiresIn"`
}

type JWTPayload struct {
	Exp   int64  `json:"exp,omitempty"`
	Iat   int64  `json:"iat,omitempty"`
	Nbf   int64  `json:"nbf,omitempty"`
	Jti   string `json:"jti,omitempty"`
	Htm   string `json:"htm,omitempty"`
	Htu   string `json:"htu,omitempty"`
	Nonce string `json:"nonce,omitempty"`
	Sub   string `json:"sub,omitempty"`
}

type jwkECPrivateKey struct {
	Kty string `json:"kty"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
	D   string `json:"d"`
}

type dpopKeyPairEnvelope struct {
	PrivateKey *jwkECPrivateKey `json:"privateKey"`
	KeyPair    *jwkECPrivateKey `json:"keyPair"`
	JWK        *jwkECPrivateKey `json:"jwk"`
}

const defaultDPoPKeySecret = "passport-dpop-token-generator"

type encryptedDpopKeyPair struct {
	Data       string `json:"data"`
	Ciphertext string `json:"ciphertext"`
	Encrypted  string `json:"encrypted"`
	Secret     string `json:"secret"`
	Password   string `json:"password"`
	Passphrase string `json:"passphrase"`
}

func GenerateDPoPToken(in DPoPTokenInput) (*DPoPTokenOutput, error) {
	if in.KeyPair == nil {
		return nil, errors.New("keyPair required")
	}
	if in.KeyPair.Curve != elliptic.P256() {
		return nil, errors.New("ES256 requires P-256 key")
	}
	if in.Clock == nil {
		in.Clock = SystemClock{}
	}
	if in.ExpiresIn <= 0 {
		in.ExpiresIn = 15
	}

	now, err := in.Clock.Now()
	if err != nil {
		return nil, err
	}

	payload := map[string]any{
		"jti":   pickStr(in.JTI, uuid.NewString()),
		"htm":   pickStr(in.HTM, ""),
		"htu":   pickStr(in.HTU, ""),
		"iat":   pickI64(in.IAT, now),
		"nonce": pickStr(in.Nonce, uuid.NewString()),
	}
	if in.ExpiresIn > 0 {
		payload["exp"] = payload["iat"].(int64) + in.ExpiresIn
	}

	pub := in.KeyPair.PublicKey
	header := map[string]any{
		"typ": "dpop+jwt",
		"alg": "ES256",
		"jwk": map[string]string{
			"kty": "EC",
			"crv": "P-256",
			"x":   b64url(pad32(pub.X.Bytes())),
			"y":   b64url(pad32(pub.Y.Bytes())),
		},
	}

	hb, err := json.Marshal(header)
	if err != nil {
		return nil, err
	}
	pb, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}

	hEnc := b64url(hb)
	pEnc := b64url(pb)
	signingInput := hEnc + "." + pEnc

	sum := sha256.Sum256([]byte(signingInput))
	r, s, err := ecdsa.Sign(rand.Reader, in.KeyPair, sum[:])
	if err != nil {
		return nil, err
	}

	sig := append(pad32(r.Bytes()), pad32(s.Bytes())...)
	token := signingInput + "." + b64url(sig)

	iat := payload["iat"].(int64)
	return &DPoPTokenOutput{
		DPoPToken:   token,
		ExpiredTime: iat + in.ExpiresIn,
		ExpiresIn:   in.ExpiresIn,
	}, nil
}

func ParseJWTPayload(token string, out any) error {
	token = strings.TrimSpace(trimTokenScheme(token))
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return fmt.Errorf("invalid JWT format")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return fmt.Errorf("failed to decode JWT payload: %w", err)
	}
	if err := json.Unmarshal(payload, out); err != nil {
		return fmt.Errorf("failed to parse JWT payload: %w", err)
	}
	return nil
}

func parseECPrivateKeyJWK(raw string) (*ecdsa.PrivateKey, error) {
	var jwk jwkECPrivateKey
	if err := json.Unmarshal([]byte(raw), &jwk); err != nil {
		return nil, err
	}
	if jwk.D == "" || jwk.X == "" || jwk.Y == "" {
		var env dpopKeyPairEnvelope
		if err := json.Unmarshal([]byte(raw), &env); err != nil {
			return nil, err
		}
		switch {
		case env.PrivateKey != nil:
			jwk = *env.PrivateKey
		case env.KeyPair != nil:
			jwk = *env.KeyPair
		case env.JWK != nil:
			jwk = *env.JWK
		default:
			return nil, errors.New("missing private key JWK")
		}
	}

	if jwk.Kty != "" && jwk.Kty != "EC" {
		return nil, errors.New("unsupported JWK kty")
	}
	if jwk.Crv != "" && jwk.Crv != "P-256" {
		return nil, errors.New("unsupported JWK curve")
	}
	if jwk.D == "" || jwk.X == "" || jwk.Y == "" {
		return nil, errors.New("incomplete JWK")
	}

	xBytes, err := base64.RawURLEncoding.DecodeString(jwk.X)
	if err != nil {
		return nil, fmt.Errorf("invalid jwk x: %w", err)
	}
	yBytes, err := base64.RawURLEncoding.DecodeString(jwk.Y)
	if err != nil {
		return nil, fmt.Errorf("invalid jwk y: %w", err)
	}
	dBytes, err := base64.RawURLEncoding.DecodeString(jwk.D)
	if err != nil {
		return nil, fmt.Errorf("invalid jwk d: %w", err)
	}

	key := &ecdsa.PrivateKey{
		PublicKey: ecdsa.PublicKey{
			Curve: elliptic.P256(),
			X:     new(big.Int).SetBytes(xBytes),
			Y:     new(big.Int).SetBytes(yBytes),
		},
		D: new(big.Int).SetBytes(dBytes),
	}
	return validateP256Key(key)
}

func parseEncryptedDPoPKeyPair(raw string) (*ecdsa.PrivateKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("empty encrypted key pair")
	}

	var payload encryptedDpopKeyPair
	ciphertext := raw
	if strings.HasPrefix(raw, "{") {
		if err := json.Unmarshal([]byte(raw), &payload); err != nil {
			return nil, err
		}
		switch {
		case strings.TrimSpace(payload.Data) != "":
			ciphertext = strings.TrimSpace(payload.Data)
		case strings.TrimSpace(payload.Ciphertext) != "":
			ciphertext = strings.TrimSpace(payload.Ciphertext)
		case strings.TrimSpace(payload.Encrypted) != "":
			ciphertext = strings.TrimSpace(payload.Encrypted)
		default:
			return nil, errors.New("missing encrypted dpop payload")
		}
	}

	decoded, err := decodeBase64Loose(ciphertext)
	if err != nil {
		return nil, err
	}
	if len(decoded) <= 12 {
		return nil, errors.New("encrypted dpop payload too short")
	}

	plain, err := decryptDoubaoKeyPair(decoded, defaultDPoPKeySecret)
	if err != nil {
		return nil, fmt.Errorf("failed to decrypt with default secret: %w", err)
	}
	return parseECPrivateKeyJWK(string(plain))
}

func decryptDoubaoKeyPair(ciphertext []byte, secret string) ([]byte, error) {
	key := pbkdf2.Key([]byte(secret), []byte("fixed-salt"), 100000, 32, sha256.New)
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonceSize := aead.NonceSize()
	if len(ciphertext) <= nonceSize {
		return nil, errors.New("ciphertext too short")
	}
	nonce := ciphertext[:nonceSize]
	enc := ciphertext[nonceSize:]
	return aead.Open(nil, nonce, enc, nil)
}

func decodeBase64Loose(raw string) ([]byte, error) {
	raw = strings.TrimSpace(raw)
	raw = strings.ReplaceAll(raw, "\n", "")
	raw = strings.ReplaceAll(raw, "\r", "")
	raw = strings.ReplaceAll(raw, "\t", "")
	raw = strings.ReplaceAll(raw, " ", "")

	encodings := []*base64.Encoding{
		base64.StdEncoding,
		base64.RawStdEncoding,
		base64.URLEncoding,
		base64.RawURLEncoding,
	}
	var lastErr error
	for _, enc := range encodings {
		decoded, err := enc.DecodeString(raw)
		if err == nil {
			return decoded, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("invalid base64")
	}
	return nil, lastErr
}

func validateP256Key(key *ecdsa.PrivateKey) (*ecdsa.PrivateKey, error) {
	if key == nil {
		return nil, errors.New("nil private key")
	}
	if key.Curve != elliptic.P256() {
		return nil, errors.New("ES256 requires P-256 key")
	}
	if key.PublicKey.X == nil || key.PublicKey.Y == nil || key.D == nil {
		return nil, errors.New("invalid private key")
	}
	if !key.Curve.IsOnCurve(key.PublicKey.X, key.PublicKey.Y) {
		return nil, errors.New("public key is not on P-256 curve")
	}
	return key, nil
}

func trimTokenScheme(token string) string {
	token = strings.TrimSpace(token)
	if i := strings.IndexByte(token, ' '); i > 0 {
		scheme := strings.ToLower(strings.TrimSpace(token[:i]))
		if scheme == "bearer" || scheme == "dpop" {
			return strings.TrimSpace(token[i+1:])
		}
	}
	return token
}

func b64url(b []byte) string {
	return base64.RawURLEncoding.EncodeToString(b)
}

func pad32(b []byte) []byte {
	if len(b) >= 32 {
		return b[len(b)-32:]
	}
	out := make([]byte, 32)
	copy(out[32-len(b):], b)
	return out
}

func pickStr(v, def string) string {
	if v != "" {
		return v
	}
	return def
}

func pickI64(v, def int64) int64 {
	if v != 0 {
		return v
	}
	return def
}

func (d *DoubaoNew) resolveAuthorizationToken() string {
	auth := strings.TrimSpace(d.Authorization)
	if auth == "" && d.Cookie != "" {
		auth = cookie.GetStr(d.Cookie, "LARK_SUITE_ACCESS_TOKEN")
	}
	return trimTokenScheme(auth)
}

func (d *DoubaoNew) resolveAuthorization() string {
	auth := d.resolveAuthorizationToken()
	if auth == "" {
		return ""
	}
	return "DPoP " + auth
}

func (d *DoubaoNew) resolveDpop() string {
	dpop := strings.TrimSpace(d.Dpop)
	if dpop == "" && d.Cookie != "" {
		dpop = cookie.GetStr(d.Cookie, "LARK_SUITE_DPOP")
	}
	return dpop
}

func (d *DoubaoNew) resolveDPoPKeyPair() (*ecdsa.PrivateKey, error) {
	raw := strings.TrimSpace(d.DpopKeyPair)
	if raw == "" {
		return nil, nil
	}
	if cached, ok := d.dpopKeyPairCache.Load(raw); ok {
		key, _ := cached.(*ecdsa.PrivateKey)
		return key, nil
	}
	key, err := parseEncryptedDPoPKeyPair(raw)
	if err != nil {
		return nil, err
	}
	d.dpopKeyPairCache.Store(raw, key)
	return key, nil
}

func (d *DoubaoNew) resolveDpopForRequest(method, rawURL string) (string, error) {
	if key, err := d.resolveDPoPKeyPair(); err != nil {
		return "", err
	} else if key != nil {
		proof, err := GenerateDPoPToken(DPoPTokenInput{
			KeyPair: key,
			HTM:     strings.ToUpper(strings.TrimSpace(method)),
			HTU:     normalizeDPoPURL(rawURL),
		})
		if err != nil {
			return "", err
		}
		return proof.DPoPToken, nil
	}

	static := d.resolveDpop()
	if static == "" {
		return "", nil
	}
	if payload, err := parseDPoPPayload(static); err == nil && payload.Exp > 0 {
		now := time.Now().Unix()
		if payload.Exp <= now+5 {
			return "", errors.New("static dpop token expired or near expiry; configure dpop_key_pair for automatic refresh")
		}
	}
	return static, nil
}

func (d *DoubaoNew) applyAuthHeaders(req *resty.Request, method, rawURL string) error {
	if auth := d.resolveAuthorization(); auth != "" {
		req.SetHeader("authorization", auth)
	}
	dpop, err := d.resolveDpopForRequest(method, rawURL)
	if err != nil {
		return err
	}
	if dpop != "" {
		req.SetHeader("dpop", dpop)
	}
	return nil
}

func normalizeDPoPURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Fragment = ""
	return u.String()
}

func parseDPoPPayload(token string) (*JWTPayload, error) {
	var payload JWTPayload
	if err := ParseJWTPayload(token, &payload); err != nil {
		return nil, err
	}
	return &payload, nil
}
