package _139

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestInvalidAuthorizationDoesNotUseCookieFastLogin(t *testing.T) {
	d := Yun139{Addition: Addition{
		Authorization: "not-base64",
		MailCookies:   "Os_SSo_Sid=sid; RMKEY=rmkey",
	}}

	err := d.refreshToken()
	if err == nil || !strings.Contains(err.Error(), "password login failed") {
		t.Fatalf("refreshToken() error = %v, want password login fallback error", err)
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
