package _139

import (
	"net/http"
	"testing"
)

func TestSanitizeLoginCookiesReplacesJSessionIDAndOrdersAllowlist(t *testing.T) {
	got := sanitizeLoginCookies("unknown=x; RMKEY=rm; JSESSIONID=old; Os_SSo_Sid=sid; behaviorid=b", "fresh")
	want := "behaviorid=b; Os_SSo_Sid=sid; JSESSIONID=fresh"
	if got != want {
		t.Fatalf("sanitizeLoginCookies() = %q, want %q", got, want)
	}
}

func TestSanitizeLoginCookiesDropsStaleJSessionIDWhenFreshOneMissing(t *testing.T) {
	got := sanitizeLoginCookies("JSESSIONID=old; Os_SSo_Sid=sid", "")
	want := "Os_SSo_Sid=sid"
	if got != want {
		t.Fatalf("sanitizeLoginCookies() = %q, want %q", got, want)
	}
}

func TestMergeMailCookiesIsDeterministicAndKeepsExtrasSorted(t *testing.T) {
	got := mergeMailCookies("z=zv; behaviorid=b; Os_SSo_Sid=old", []*http.Cookie{
		{Name: "RMKEY", Value: "rm"},
		{Name: "Os_SSo_Sid", Value: "sid"},
		{Name: "a", Value: "av"},
	})
	want := "behaviorid=b; Os_SSo_Sid=sid; RMKEY=rm; a=av; z=zv"
	if got != want {
		t.Fatalf("mergeMailCookies() = %q, want %q", got, want)
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
