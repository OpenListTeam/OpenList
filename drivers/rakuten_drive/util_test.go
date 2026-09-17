package rakuten_drive

import (
	"encoding/base64"
	"strconv"
	"testing"
	"time"
)

func makeJWT(payload string, pad bool) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	var body string
	if pad {
		body = base64.URLEncoding.EncodeToString([]byte(payload))
	} else {
		body = base64.RawURLEncoding.EncodeToString([]byte(payload))
	}
	return header + "." + body + ".signature"
}

func TestParseJWTExp(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	cases := []struct {
		name    string
		token   string
		wantErr bool
	}{
		{"valid", makeJWT(`{"exp":`+strconv.FormatInt(exp, 10)+`}`, false), false},
		{"padded payload", makeJWT(`{"exp":`+strconv.FormatInt(exp, 10)+`}`, true), false},
		{"missing exp", makeJWT(`{"sub":"user"}`, false), true},
		{"no dot", "not-a-jwt", true},
		{"invalid base64", "header.!!!.signature", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseJWTExp(c.token)
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected error, got %v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(time.Unix(exp, 0)) {
				t.Fatalf("expected %v, got %v", time.Unix(exp, 0), got)
			}
		})
	}
}
