package pds

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func TestInitRequiresToken(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			DomainID: "domain",
			DriveID:  "drive",
		},
	}

	if err := driver.Init(context.Background()); err == nil {
		t.Fatal("expected missing token error")
	}
}

func TestInitAcceptsRefreshTokenOnly(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			DomainID:     "domain",
			DriveID:      "drive",
			RefreshToken: "refresh",
		},
	}

	if err := driver.Init(context.Background()); err != nil {
		t.Fatalf("expected refresh token to be enough, got %v", err)
	}
	if driver.RootFolderID != "root" {
		t.Fatalf("expected default root folder id, got %q", driver.RootFolderID)
	}
}

func TestEscapeQueryValue(t *testing.T) {
	got := escapeQueryValue(`a\b"c`)
	want := `a\\b\"c`
	if got != want {
		t.Fatalf("escapeQueryValue() = %q, want %q", got, want)
	}
}

func TestEnsureTokenSkipsRefreshWhenExpiresAtZeroAndAccessTokenExists(t *testing.T) {
	addition := &Addition{
		DomainID:     "domain",
		ClientID:     "client",
		AccessToken:  "access",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    0,
	}
	client := &client{
		addition: addition,
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("unexpected refresh request to %s", req.URL.String())
		})},
	}

	if err := client.ensureToken(context.Background()); err != nil {
		t.Fatalf("ensureToken() error = %v", err)
	}
	if addition.AccessToken != "access" {
		t.Fatalf("AccessToken = %q, want access", addition.AccessToken)
	}
	if addition.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0", addition.ExpiresAt)
	}
}

func TestEnsureTokenRefreshesMissingAccessTokenAndKeepsExpiresAtZero(t *testing.T) {
	addition := &Addition{
		DomainID:     "domain",
		ClientID:     "client",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
	}
	saveCalls := 0
	refreshCalls := 0
	client := &client{
		addition: addition,
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			refreshCalls++
			if req.URL.Host != "domain.auth.aliyunfile.com" || req.URL.Path != "/v2/oauth/token" {
				return nil, fmt.Errorf("unexpected request to %s", req.URL.String())
			}
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}
			if !strings.Contains(string(body), "grant_type=refresh_token") {
				return nil, fmt.Errorf("refresh request body = %q", string(body))
			}
			return testJSONResponse(req, http.StatusOK, `{"access_token":"fresh","token_type":"Bearer","expires_in":3600,"refresh_token":"refresh2"}`), nil
		})},
		onSave: func() {
			saveCalls++
		},
	}

	if err := client.ensureToken(context.Background()); err != nil {
		t.Fatalf("ensureToken() error = %v", err)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	if saveCalls != 1 {
		t.Fatalf("saveCalls = %d, want 1", saveCalls)
	}
	if addition.AccessToken != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", addition.AccessToken)
	}
	if addition.RefreshToken != "refresh2" {
		t.Fatalf("RefreshToken = %q, want refresh2", addition.RefreshToken)
	}
	if addition.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0", addition.ExpiresAt)
	}
}

func TestPostRefreshesAndRetriesOnAccessTokenExpired(t *testing.T) {
	addition := &Addition{
		DomainID:     "domain",
		ClientID:     "client",
		AccessToken:  "expired",
		RefreshToken: "refresh",
		TokenType:    "Bearer",
		ExpiresAt:    0,
	}
	apiCalls := 0
	refreshCalls := 0
	saveCalls := 0
	client := &client{
		addition: addition,
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.Host {
			case "domain.api.aliyunfile.com":
				apiCalls++
				switch apiCalls {
				case 1:
					if got := req.Header.Get("Authorization"); got != "Bearer expired" {
						return nil, fmt.Errorf("first Authorization = %q, want Bearer expired", got)
					}
					return testJSONResponse(req, http.StatusUnauthorized, `{"code":"AccessTokenExpired","message":"access token expired"}`), nil
				case 2:
					if got := req.Header.Get("Authorization"); got != "Bearer fresh" {
						return nil, fmt.Errorf("second Authorization = %q, want Bearer fresh", got)
					}
					return testJSONResponse(req, http.StatusOK, `{}`), nil
				default:
					return nil, fmt.Errorf("unexpected api call %d", apiCalls)
				}
			case "domain.auth.aliyunfile.com":
				refreshCalls++
				return testJSONResponse(req, http.StatusOK, `{"access_token":"fresh","token_type":"Bearer","expires_in":3600}`), nil
			default:
				return nil, fmt.Errorf("unexpected request to %s", req.URL.String())
			}
		})},
		onSave: func() {
			saveCalls++
		},
	}

	if err := client.post(context.Background(), "/v2/file/list", map[string]any{"drive_id": "drive"}, nil); err != nil {
		t.Fatalf("post() error = %v", err)
	}
	if apiCalls != 2 {
		t.Fatalf("apiCalls = %d, want 2", apiCalls)
	}
	if refreshCalls != 1 {
		t.Fatalf("refreshCalls = %d, want 1", refreshCalls)
	}
	if saveCalls != 1 {
		t.Fatalf("saveCalls = %d, want 1", saveCalls)
	}
	if addition.AccessToken != "fresh" {
		t.Fatalf("AccessToken = %q, want fresh", addition.AccessToken)
	}
	if addition.ExpiresAt != 0 {
		t.Fatalf("ExpiresAt = %d, want 0", addition.ExpiresAt)
	}
}

func TestDirectUploadTools(t *testing.T) {
	driver := &PDS{}
	tools := driver.GetDirectUploadTools()
	if len(tools) != 1 || tools[0] != directUploadTool {
		t.Fatalf("GetDirectUploadTools() = %v, want [%s]", tools, directUploadTool)
	}
}

func TestMakeDirSetsReturnedPath(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			DomainID:    "domain",
			DriveID:     "drive",
			AccessToken: "access",
			TokenType:   "Bearer",
		},
	}
	driver.client = &client{
		addition: &driver.Addition,
		http: &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "domain.api.aliyunfile.com" || req.URL.Path != "/v2/file/create" {
				return nil, fmt.Errorf("unexpected request to %s", req.URL.String())
			}
			return testJSONResponse(req, http.StatusOK, `{"file_id":"child-id","name":"child"}`), nil
		})},
	}

	obj, err := driver.MakeDir(context.Background(), &model.Object{
		ID:       "parent-id",
		Path:     "/parent",
		Name:     "parent",
		IsFolder: true,
	}, "child")
	if err != nil {
		t.Fatalf("MakeDir() error = %v", err)
	}
	if obj.GetPath() != "/parent/child" {
		t.Fatalf("MakeDir() path = %q, want /parent/child", obj.GetPath())
	}
}

func TestDirectUploadTokenRoundTrip(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			RefreshToken: "refresh",
		},
	}
	token := directUploadToken{
		DomainID:     "domain",
		DriveID:      "drive",
		ParentFileID: "root",
		FileID:       "file",
		UploadID:     "upload",
		FileName:     "test.txt",
		FileSize:     4,
		ExpiresAt:    time.Now().Add(time.Minute).Unix(),
	}

	raw, err := driver.signDirectUploadToken(token)
	if err != nil {
		t.Fatalf("signDirectUploadToken() error = %v", err)
	}
	got, err := driver.verifyDirectUploadToken(raw)
	if err != nil {
		t.Fatalf("verifyDirectUploadToken() error = %v", err)
	}
	if *got != token {
		t.Fatalf("verifyDirectUploadToken() = %+v, want %+v", *got, token)
	}
}

func TestDirectUploadTokenRejectsTampering(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			RefreshToken: "refresh",
		},
	}
	raw, err := driver.signDirectUploadToken(directUploadToken{
		DomainID:  "domain",
		DriveID:   "drive",
		FileID:    "file",
		UploadID:  "upload",
		FileName:  "test.txt",
		ExpiresAt: time.Now().Add(time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("signDirectUploadToken() error = %v", err)
	}

	last := raw[len(raw)-1]
	replacement := byte('A')
	if last == replacement {
		replacement = 'B'
	}
	tampered := raw[:len(raw)-1] + string(replacement)
	if _, err := driver.verifyDirectUploadToken(tampered); err == nil {
		t.Fatal("expected tampered direct upload token to be rejected")
	}
}

func TestDirectUploadTokenRejectsExpired(t *testing.T) {
	driver := &PDS{
		Addition: Addition{
			RefreshToken: "refresh",
		},
	}
	raw, err := driver.signDirectUploadToken(directUploadToken{
		DomainID:  "domain",
		DriveID:   "drive",
		FileID:    "file",
		UploadID:  "upload",
		FileName:  "test.txt",
		ExpiresAt: time.Now().Add(-time.Minute).Unix(),
	})
	if err != nil {
		t.Fatalf("signDirectUploadToken() error = %v", err)
	}
	if _, err := driver.verifyDirectUploadToken(raw); err == nil {
		t.Fatal("expected expired direct upload token to be rejected")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func testJSONResponse(req *http.Request, statusCode int, body string) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     fmt.Sprintf("%d %s", statusCode, http.StatusText(statusCode)),
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
		Request:    req,
	}
}
