package _139

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
)

func TestSanitizeLoginCookiesDropsStaleJSessionIDWhenFreshOneMissing(t *testing.T) {
	got := sanitizeMailLoginCookies("JSESSIONID=old; behaviorid=b", "")
	want := "behaviorid=b"
	if got != want {
		t.Fatalf("sanitizeMailLoginCookies() = %q, want %q", got, want)
	}
}

func TestMergeMailCookiesPreservesExistingOrderAndAppendsNewNames(t *testing.T) {
	got := mergeMailCookieHeader("z=zv; behaviorid=b; Os_SSo_Sid=old", []*http.Cookie{
		{Name: "RMKEY", Value: "rm"},
		{Name: "Os_SSo_Sid", Value: "sid"},
		{Name: "a", Value: "av"},
	})
	want := "z=zv;behaviorid=b;Os_SSo_Sid=sid;RMKEY=rm;a=av"
	if got != want {
		t.Fatalf("mergeMailCookieHeader() = %q, want %q", got, want)
	}
}

func TestExtractFastLoginCookies(t *testing.T) {
	sid, rmkey := extractFastLoginCookies("RMKEY=rm; Os_SSo_Sid=sid")
	if sid != "sid" || rmkey != "rm" {
		t.Fatalf("extractFastLoginCookies() = %q, %q; want sid, rm", sid, rmkey)
	}
}

func TestCredentialState(t *testing.T) {
	tests := []struct {
		name string
		d    Yun139
		want credentialState
		err  bool
	}{
		{
			name: "authorization",
			d:    Yun139{Addition: Addition{Authorization: " auth "}},
			want: credentialStateAuthorization,
		},
		{
			name: "full login",
			d: Yun139{Addition: Addition{
				MailCookies: "RMKEY=rm; Os_SSo_Sid=sid",
				Username:    "user",
				Password:    "password",
			}},
			want: credentialStateFullLogin,
		},
		{
			name: "cookies only",
			d:    Yun139{Addition: Addition{MailCookies: "RMKEY=rm; Os_SSo_Sid=sid"}},
			want: credentialStateCookiesOnly,
		},
		{
			name: "partial password login",
			d:    Yun139{Addition: Addition{Username: "user"}},
			err:  true,
		},
		{
			name: "missing credentials",
			d:    Yun139{},
			err:  true,
		},
		{
			name: "invalid cookie",
			d:    Yun139{Addition: Addition{MailCookies: "invalid-cookie"}},
			err:  true,
		},
		{
			name: "authorization with basic prefix",
			d:    Yun139{Addition: Addition{Authorization: "Basic abc"}},
			err:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.d.credentialState()
			if tt.err {
				if err == nil {
					t.Fatal("credentialState() expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("credentialState() unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("credentialState() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIntegrationLoginObtainsAuthorization(t *testing.T) {
	if os.Getenv("OPENLIST_139_INTEGRATION") != "1" {
		t.Skip("set OPENLIST_139_INTEGRATION=1 to run live 139Yun login checks")
	}
	base.RestyClient = resty.New().
		SetHeader("user-agent", base.UserAgent).
		SetRetryCount(3).
		SetRetryResetReaders(true).
		SetTimeout(30 * time.Second)

	username := os.Getenv("OPENLIST_139_USERNAME")
	password := os.Getenv("OPENLIST_139_PASSWORD")
	mailCookies := os.Getenv("OPENLIST_139_MAIL_COOKIES")
	authorization := strings.TrimSpace(os.Getenv("OPENLIST_139_AUTHORIZATION"))

	if authorization != "" {
		t.Run("authorization", func(t *testing.T) {
			d := Yun139{Addition: Addition{Authorization: authorization}}
			state, err := d.credentialState()
			if err != nil {
				t.Fatalf("credentialState() unexpected error: %v", err)
			}
			if state != credentialStateAuthorization {
				t.Fatalf("credentialState() = %v, want authorization", state)
			}
			if d.Authorization == "" || strings.HasPrefix(strings.ToLower(d.Authorization), "basic ") {
				t.Fatal("authorization should be present without Basic prefix")
			}
		})
	}

	if mailCookies == "" {
		t.Fatal("OPENLIST_139_MAIL_COOKIES is required")
	}

	runFastLogin := func(t *testing.T, mailCookies string) {
		t.Helper()
		d := Yun139{Addition: Addition{MailCookies: mailCookies}}
		state, err := d.credentialState()
		if err != nil {
			t.Fatalf("credentialState() unexpected error: %v", err)
		}
		if state != credentialStateCookiesOnly {
			t.Fatalf("credentialState() = %v, want cookies only", state)
		}
		sid, rmkey := extractFastLoginCookies(d.MailCookies)
		if sid == "" || rmkey == "" {
			t.Fatal("mail cookies are missing Os_SSo_Sid or RMKEY")
		}
		token, err := d.step2_get_single_token(sid)
		if err != nil {
			t.Fatalf("step2_get_single_token() error: %v", err)
		}
		auth, err := d.step3_third_party_login(token)
		if err != nil {
			t.Fatalf("step3_third_party_login() error: %v", err)
		}
		d.Authorization = auth
		if d.Authorization == "" {
			t.Fatal("authorization is empty after fast login")
		}
	}

	if username == "" || password == "" {
		t.Fatal("OPENLIST_139_USERNAME and OPENLIST_139_PASSWORD are required for password fallback")
	}

	var refreshedMailCookies string
	var generatedAuthorization string
	t.Run("password login fallback", func(t *testing.T) {
		d := Yun139{Addition: Addition{
			MailCookies: mailCookies,
			Username:    username,
			Password:    password,
		}}
		state, err := d.credentialState()
		if err != nil {
			t.Fatalf("credentialState() unexpected error: %v", err)
		}
		if state != credentialStateFullLogin {
			t.Fatalf("credentialState() = %v, want full login", state)
		}
		passId, err := d.step1_password_login()
		if err != nil {
			t.Fatalf("step1_password_login() error: %v", err)
		}
		token, err := d.step2_get_single_token(passId)
		if err != nil {
			t.Fatalf("step2_get_single_token() error: %v", err)
		}
		auth, err := d.step3_third_party_login(token)
		if err != nil {
			t.Fatalf("step3_third_party_login() error: %v", err)
		}
		d.Authorization = auth
		if auth == "" || d.Authorization == "" {
			t.Fatal("authorization is empty after password login")
		}
		generatedAuthorization = auth
		refreshedMailCookies = d.MailCookies
	})

	t.Run("authorization generated by password login", func(t *testing.T) {
		if generatedAuthorization == "" {
			t.Fatal("password login did not generate authorization")
		}
		d := Yun139{Addition: Addition{Authorization: generatedAuthorization}}
		state, err := d.credentialState()
		if err != nil {
			t.Fatalf("credentialState() unexpected error: %v", err)
		}
		if state != credentialStateAuthorization {
			t.Fatalf("credentialState() = %v, want authorization", state)
		}
		if strings.HasPrefix(strings.ToLower(d.Authorization), "basic ") {
			t.Fatal("authorization should not include Basic prefix")
		}
	})

	t.Run("mail cookies fast login from input", func(t *testing.T) {
		sid, rmkey := extractFastLoginCookies(mailCookies)
		if sid == "" || rmkey == "" {
			t.Skip("input mail cookies are missing Os_SSo_Sid or RMKEY")
		}
		runFastLogin(t, mailCookies)
	})

	t.Run("mail cookies fast login after password login", func(t *testing.T) {
		if refreshedMailCookies == "" {
			t.Fatal("password login did not refresh mail cookies")
		}
		runFastLogin(t, refreshedMailCookies)
	})
}

func TestIsRedirectStatus(t *testing.T) {
	for _, status := range []int{300, 301, 302, 307, 399} {
		if !isRedirectStatus(status) {
			t.Fatalf("isRedirectStatus(%d) = false, want true", status)
		}
	}
	for _, status := range []int{200, 299, 400, 500} {
		if isRedirectStatus(status) {
			t.Fatalf("isRedirectStatus(%d) = true, want false", status)
		}
	}
}

func TestFetchMailJSessionIDAcceptsRedirectResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.SetCookie(w, &http.Cookie{Name: "JSESSIONID", Value: "fresh"})
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer server.Close()

	got, err := fetchMailJSessionID(server.URL)
	if err != nil {
		t.Fatalf("fetchMailJSessionID() error: %v", err)
	}
	if got != "fresh" {
		t.Fatalf("fetchMailJSessionID() = %q, want fresh", got)
	}
}

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
