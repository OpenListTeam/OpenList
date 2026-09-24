package sjtu_netdisk

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

func setTestConfig(t *testing.T) {
	t.Helper()

	oldConfig := conf.Conf
	conf.Conf = conf.DefaultConfig(t.TempDir())
	t.Cleanup(func() { conf.Conf = oldConfig })
}

func TestRefreshTokenRejectsEmptyAccessToken(t *testing.T) {
	setTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"libraryId":"library","spaceId":"space"}`))
	}))
	defer server.Close()

	oldTokenURL := tokenURL
	tokenURL = server.URL
	t.Cleanup(func() { tokenURL = oldTokenURL })

	d := &SJTUNetdisk{Addition: Addition{UserToken: "user-token", KeepAlive: "keep-alive"}}
	d.client = d.newClient()

	err := d.refreshToken(context.Background())
	if err == nil || err.Error() != "sjtu_netdisk: empty access_token in response" {
		t.Fatalf("refreshToken() error = %v, want empty access_token error", err)
	}
}

func TestListUsesQueryAccessTokenAndReusesClient(t *testing.T) {
	setTestConfig(t)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch r.URL.Path {
		case "/token":
			_, _ = w.Write([]byte(`{"libraryId":"library","spaceId":"space","accessToken":"access-token","expiresIn":1800}`))
		case "/api/v1/directory/library/space/":
			if got := r.URL.Query().Get("access_token"); got != "access-token" {
				t.Errorf("access_token query parameter = %q, want %q", got, "access-token")
			}
			_, _ = w.Write([]byte(`{"totalNum":0,"contents":[]}`))
		default:
			t.Errorf("unexpected request path: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	oldTokenURL, oldAPIURL := tokenURL, apiURL
	tokenURL = server.URL + "/token"
	apiURL = server.URL + "/api/v1"
	t.Cleanup(func() {
		tokenURL = oldTokenURL
		apiURL = oldAPIURL
	})

	d := &SJTUNetdisk{Addition: Addition{
		UserToken:   "user-token",
		UserId:      "user-id",
		KeepAlive:   "keep-alive",
		OrderBy:     "name",
		OrderByType: "asc",
	}}
	if err := d.Init(context.Background()); err != nil {
		t.Fatalf("Init() error = %v", err)
	}
	client := d.client

	root, err := d.GetRoot(context.Background())
	if err != nil {
		t.Fatalf("GetRoot() error = %v", err)
	}
	if _, err := d.List(context.Background(), root, model.ListArgs{}); err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if d.client != client {
		t.Fatal("List() replaced the shared API client")
	}
}

func TestEncodePathEscapesPathSeparators(t *testing.T) {
	d := &SJTUNetdisk{}

	if got, want := d.encodePath("directory/file name?.txt"), "directory%2Ffile%20name%3F.txt"; got != want {
		t.Errorf("encodePath() = %q, want %q", got, want)
	}
}
