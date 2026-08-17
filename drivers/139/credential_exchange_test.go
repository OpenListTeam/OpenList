package _139

import (
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
)

func TestShouldExchangeAuthorization(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tests := []struct {
		name string
		ttl  time.Duration
		want bool
	}{
		{name: "outside window", ttl: 3*24*time.Hour + time.Millisecond},
		{name: "at three day boundary", ttl: 3 * 24 * time.Hour, want: true},
		{name: "inside window", ttl: 12 * time.Hour, want: true},
		{name: "expired", ttl: -time.Millisecond, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldExchangeAuthorization(now, now.Add(tt.ttl)); got != tt.want {
				t.Fatalf("shouldExchangeAuthorization() = %t, want %t", got, tt.want)
			}
		})
	}
}

func TestInvalidAuthorizationDoesNotUseCookieFastLogin(t *testing.T) {
	d := Yun139{Addition: Addition{
		Authorization: "not-base64",
		MailCookies:   "Os_SSo_Sid=sid; RMKEY=rmkey",
	}}

	err := d.refreshTokenAt(time.Unix(1_700_000_000, 0))
	if err == nil || !strings.Contains(err.Error(), "password login failed") {
		t.Fatalf("refreshTokenAt() error = %v, want password login fallback error", err)
	}
	if d.Authorization != "not-base64" {
		t.Fatalf("Authorization = %q, want original invalid value retained", d.Authorization)
	}
}

func TestSMSSceneForRisk(t *testing.T) {
	tests := map[string]int{
		"S025":         1,
		"S035":         1,
		"PML401010062": 2,
		"MW0016":       4,
	}
	for riskCode, want := range tests {
		got, ok := smsSceneForRisk(riskCode)
		if !ok || got != want {
			t.Fatalf("smsSceneForRisk(%q) = %d, %t; want %d, true", riskCode, got, ok, want)
		}
	}
	if _, ok := smsSceneForRisk("PICTURE_ONLY"); ok {
		t.Fatal("smsSceneForRisk() accepted unsupported risk code")
	}
}

func TestSanitizeMailLoginCookiesKeepsDeviceContext(t *testing.T) {
	got := sanitizeMailLoginCookies(
		"behaviorid=device; Os_SSo_Sid=old-sid; RMKEY=old-rmkey; JSESSIONID=old-session; S_DEVICE_TOKEN=fingerprint",
		"new-session",
	)
	want := "behaviorid=device;JSESSIONID=new-session;S_DEVICE_TOKEN=fingerprint"
	if got != want {
		t.Fatalf("sanitizeMailLoginCookies() = %q, want %q", got, want)
	}
}

func TestSendSMSVerificationCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if r.URL.Query().Get("func") != "login:sendSmsCodeByScene" {
			t.Errorf("func = %q", r.URL.Query().Get("func"))
		}
		if !strings.Contains(string(body), `<string name="scene">1</string>`) {
			t.Errorf("request body = %s", body)
		}
		if !strings.Contains(r.Header.Get("Cookie"), "device=fingerprint") {
			t.Errorf("Cookie = %q", r.Header.Get("Cookie"))
		}
		http.SetCookie(w, &http.Cookie{Name: "challenge", Value: "sms"})
		_, _ = io.WriteString(w, `{"code":"S_OK"}`)
	}))
	defer server.Close()

	oldURL := mailSMSURL
	mailSMSURL = server.URL
	defer func() { mailSMSURL = oldURL }()

	d := Yun139{Addition: Addition{Username: "18800000000", MailCookies: "device=fingerprint"}}
	if err := d.sendSMSVerificationCode("S025"); err != nil {
		t.Fatalf("sendSMSVerificationCode() error = %v", err)
	}
	if !strings.Contains(d.MailCookies, "challenge=sms") {
		t.Fatalf("MailCookies = %q", d.MailCookies)
	}
}

func TestSendSMSVerificationCodeStopsAtPictureChallenge(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, `{"code":"PML401010021"}`)
	}))
	defer server.Close()

	oldURL := mailSMSURL
	mailSMSURL = server.URL
	defer func() { mailSMSURL = oldURL }()

	d := Yun139{Addition: Addition{Username: "18800000000"}}
	err := d.sendSMSVerificationCode("S025")
	if err == nil || !strings.Contains(err.Error(), "requires picture verification") {
		t.Fatalf("sendSMSVerificationCode() error = %v", err)
	}
}

func TestVerifySMSCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		if r.URL.Query().Get("func") != "/login/inlogin.action" {
			t.Errorf("func = %q", r.URL.Query().Get("func"))
		}
		wantHash := sha1Hash("fetion.com.cn:123456")
		if !strings.Contains(string(body), `<string name="loginPassword">`+wantHash+`</string>`) {
			t.Errorf("request body does not contain SMS code hash: %s", body)
		}
		http.SetCookie(w, &http.Cookie{Name: "RMKEY", Value: "new-rmkey"})
		_, _ = io.WriteString(w, `{"code":"S_OK","var":{"loginSuccessUrl":"https://mail.10086.cn/?sid=sms-sid"}}`)
	}))
	defer server.Close()

	oldURL := mailSMSURL
	mailSMSURL = server.URL
	defer func() { mailSMSURL = oldURL }()

	d := Yun139{Addition: Addition{
		Username:    "18800000000",
		SmsCode:     "123456",
		MailCookies: "challenge=sms",
	}}
	sid, err := d.verifySMSCode("S025")
	if err != nil {
		t.Fatalf("verifySMSCode() error = %v", err)
	}
	if sid != "sms-sid" {
		t.Fatalf("sid = %q", sid)
	}
	if d.SmsCode != "" {
		t.Fatalf("SmsCode = %q, want cleared", d.SmsCode)
	}
	if !strings.Contains(d.MailCookies, "RMKEY=new-rmkey") {
		t.Fatalf("MailCookies = %q", d.MailCookies)
	}
}

func TestDecodeDriveAuthorization(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("mobile:18800000000:token:with:colons"))
	account, token, err := decodeDriveAuthorization("Basic " + encoded)
	if err != nil {
		t.Fatalf("decodeDriveAuthorization() error = %v", err)
	}
	if account != "18800000000" || token != "token:with:colons" {
		t.Fatalf("decodeDriveAuthorization() = %q, %q", account, token)
	}
}

func TestRecoverMailSessionFromDrive(t *testing.T) {
	oldClient := base.RestyClient
	base.RestyClient = resty.New().SetRetryCount(0)
	defer func() { base.RestyClient = oldClient }()

	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read request body: %v", err)
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.URL.Path {
		case "/tellin/querySpecToken.do":
			decoded, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(r.Header.Get("Authorization"), "Basic "))
			if err != nil || string(decoded) != "pc:18800000000:drive-auth-token" {
				t.Errorf("Authorization = %q, decode error = %v", decoded, err)
			}
			if !strings.Contains(string(body), "<toSourceId>001003</toSourceId>") || strings.Contains(string(body), "001005") {
				t.Errorf("ticket body = %s", body)
			}
			w.Header().Set("Content-Type", "application/xml")
			_, _ = io.WriteString(w, "<root><return>0</return><token>mail-ticket</token></root>")
		case "/login/inlogin.action":
			if !strings.Contains(string(body), `<string name="token">mail-ticket</string>`) {
				t.Errorf("mail login body = %s", body)
			}
			http.SetCookie(w, &http.Cookie{Name: "Os_SSo_Sid", Value: "session-cookie"})
			http.SetCookie(w, &http.Cookie{Name: "mailTheme", Value: "blue"})
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"code":"S_OK","var":{"sid":"sid-from-body","rmkey":"rmkey-from-body"}}`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	oldTicketURL, oldLoginURL := driveMailTicketURL, driveMailLoginURL
	driveMailTicketURL = server.URL + "/tellin/querySpecToken.do"
	driveMailLoginURL = server.URL + "/login/inlogin.action"
	defer func() {
		driveMailTicketURL = oldTicketURL
		driveMailLoginURL = oldLoginURL
	}()

	d := Yun139{Addition: Addition{
		Authorization: base64.StdEncoding.EncodeToString([]byte("mobile:18800000000:drive-auth-token")),
	}}
	sid, err := d.recoverMailSessionFromDrive()
	if err != nil {
		t.Fatalf("recoverMailSessionFromDrive() error = %v", err)
	}
	if requests != 2 {
		t.Fatalf("requests = %d, want 2", requests)
	}
	if sid != "sid-from-body" {
		t.Fatalf("sid = %q", sid)
	}
	for _, cookie := range []string{
		"Os_SSo_Sid=session-cookie",
		"mailTheme=blue",
		"sid=sid-from-body",
		"RMKEY=rmkey-from-body",
	} {
		if !strings.Contains(d.MailCookies, cookie) {
			t.Fatalf("MailCookies = %q, missing %q", d.MailCookies, cookie)
		}
	}
	if d.Username != "18800000000" || d.Account != "18800000000" {
		t.Fatalf("account fields = %q, %q", d.Username, d.Account)
	}
}
