package template

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OpenListTeam/wopan-sdk-go"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// woPanMock mimics the WoPan dispatcher: it answers with the documented
// envelope and rejects requests whose User-Agent is not a browser UA, exactly
// like the edge did before the driver applied wopan.DefaultUA.
type woPanMock struct {
	calls      []string
	refreshed  bool
	badUA      int
	refreshURL string
}

func (m *woPanMock) transport(t *testing.T) http.RoundTripper {
	t.Helper()
	return roundTripFunc(func(r *http.Request) (*http.Response, error) {
		ua := r.Header.Get("User-Agent")
		if ua != wopan.DefaultUA {
			m.badUA++
			return &http.Response{
				StatusCode: http.StatusForbidden,
				Status:     "403 Forbidden",
				Body:       io.NopCloser(strings.NewReader("forbidden")),
				Header:     make(http.Header),
				Request:    r,
			}, nil
		}

		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var req struct {
			Header struct {
				Key string `json:"key"`
			} `json:"header"`
		}
		if err := json.Unmarshal(raw, &req); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		m.calls = append(m.calls, req.Header.Key)

		data := "{}"
		switch req.Header.Key {
		case wopan.KeyAppRefreshToken:
			m.refreshed = true
			data = `{"access_token":"fedcba9876543210","refresh_token":"new-refresh"}`
		case wopan.KeyAppQueryUser:
			data = `{"userId":"test-user"}`
		case wopan.KeyClassifyRule:
			data = `{"fileTypes":{}}`
		case wopan.KeyGetZoneInfo:
			data = `{"url":"https://example.invalid"}`
		case wopan.KeyFamilyUserCurrentEncode:
			data = `{"defaultHomeId":42}`
		}
		body := `{"STATUS":"200","RSP":{"RSP_CODE":"0000","RSP_DESC":"","DATA":` + data + `}}`
		return &http.Response{
			StatusCode: http.StatusOK,
			Status:     "200 OK",
			Body:       io.NopCloser(strings.NewReader(body)),
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Request:    r,
		}, nil
	})
}

// newTestClient mirrors Wopan.Init, but injects a mock transport and a token
// callback that does not touch the database.
func newTestClient(d *Wopan, rt http.RoundTripper) {
	d.client = wopan.DefaultWithRefreshToken(d.RefreshToken)
	d.client.SetAccessToken(d.AccessToken)
	d.client.SetHttpClient(&http.Client{Transport: rt})
	d.client.OnRefreshToken(d.setTokens)
}

func TestInitClientCredentials(t *testing.T) {
	const validAccessToken = "0123456789abcdef"

	tests := []struct {
		name            string
		accessToken     string
		refreshToken    string
		wantRefresh     bool
		wantErr         bool
		wantAccessToken string
		wantFamilyID    string
	}{
		{
			name:            "refresh only",
			refreshToken:    "refresh",
			wantRefresh:     true,
			wantAccessToken: "fedcba9876543210",
			wantFamilyID:    "42",
		},
		{
			name:            "short access token with refresh",
			accessToken:     "short",
			refreshToken:    "refresh",
			wantRefresh:     true,
			wantAccessToken: "fedcba9876543210",
			wantFamilyID:    "42",
		},
		{
			name:            "both tokens",
			accessToken:     validAccessToken,
			refreshToken:    "refresh",
			wantAccessToken: validAccessToken,
			wantFamilyID:    "42",
		},
		{
			name:            "access token only",
			accessToken:     validAccessToken,
			wantAccessToken: validAccessToken,
			wantFamilyID:    "42",
		},
		{
			name:    "no credentials",
			wantErr: true,
		},
		{
			name:        "short access token without refresh",
			accessToken: "short",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mock := &woPanMock{}
			d := &Wopan{Addition: Addition{
				AccessToken:  tt.accessToken,
				RefreshToken: tt.refreshToken,
			}}
			newTestClient(d, mock.transport(t))

			err := d.initClient(context.Background())
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				if len(mock.calls) != 0 {
					t.Fatalf("expected no dispatcher call on configuration error, got %v", mock.calls)
				}
				return
			}
			if err != nil {
				t.Fatalf("initClient returned error: %v", err)
			}
			if mock.badUA != 0 {
				t.Fatalf("%d request(s) were sent without the browser user agent", mock.badUA)
			}
			if mock.refreshed != tt.wantRefresh {
				t.Fatalf("refresh called = %v, want %v (calls: %v)", mock.refreshed, tt.wantRefresh, mock.calls)
			}
			if len(mock.calls) == 0 {
				t.Fatal("expected dispatcher calls, got none")
			}
			if tt.wantRefresh && mock.calls[0] != wopan.KeyAppRefreshToken {
				t.Fatalf("first call = %q, want %q", mock.calls[0], wopan.KeyAppRefreshToken)
			}
			if d.AccessToken != tt.wantAccessToken {
				t.Fatalf("AccessToken = %q, want %q", d.AccessToken, tt.wantAccessToken)
			}
			if tt.wantRefresh && d.RefreshToken != "new-refresh" {
				t.Fatalf("RefreshToken = %q, want new-refresh", d.RefreshToken)
			}
			if d.defaultFamilyID != tt.wantFamilyID {
				t.Fatalf("defaultFamilyID = %q, want %q", d.defaultFamilyID, tt.wantFamilyID)
			}
		})
	}
}
