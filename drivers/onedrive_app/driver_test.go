package onedrive_app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

const testUser = "user@example.com"

// graphCall is one request the driver sent to the mocked Microsoft Graph API.
type graphCall struct {
	method string
	path   string
}

// graphMock serves the driver's Graph requests from a local test server and
// records them. The driver builds absolute URLs from a fixed host per region,
// so requests are rewritten onto the test server before they are sent.
type graphMock struct {
	mu    sync.Mutex
	calls []graphCall
}

func newGraphMock(t *testing.T, handler http.HandlerFunc) *graphMock {
	t.Helper()
	m := &graphMock{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		m.calls = append(m.calls, graphCall{method: r.Method, path: r.URL.Path})
		m.mu.Unlock()
		handler(w, r)
	}))
	t.Cleanup(srv.Close)

	previous := base.RestyClient
	client := resty.New().SetTimeout(5 * time.Second)
	client.OnBeforeRequest(func(_ *resty.Client, req *resty.Request) error {
		for _, host := range onedriveHostMap {
			if strings.HasPrefix(req.URL, host.Api) {
				req.URL = strings.Replace(req.URL, host.Api, srv.URL, 1)
				return nil
			}
		}
		return nil
	})
	base.RestyClient = client
	t.Cleanup(func() { base.RestyClient = previous })
	return m
}

func (m *graphMock) assertRequests(t *testing.T, want ...graphCall) {
	t.Helper()
	m.mu.Lock()
	got := append([]graphCall(nil), m.calls...)
	m.mu.Unlock()
	if len(got) != len(want) {
		t.Fatalf("Graph requests = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Graph request %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func newRemoveDriver(removeWay string) *OnedriveAPP {
	return &OnedriveAPP{
		Addition:    Addition{Region: "global", Email: testUser, RemoveWay: removeWay},
		AccessToken: "test-token",
	}
}

func fileObject(id string) *Object {
	return &Object{ObjThumb: model.ObjThumb{Object: model.Object{
		ID:   id,
		Path: "/dir/file.txt",
		Name: "file.txt",
	}}}
}

func writeJSON(t *testing.T, w http.ResponseWriter, body string, status ...int) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if len(status) > 0 {
		w.WriteHeader(status[0])
	}
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("failed to write response body: %v", err)
	}
}

func TestRemoveMovesItemToRecycleBin(t *testing.T) {
	// An empty remove_way is what storages configured before this option existed
	// deserialize to, so it must keep moving items to the recycle bin.
	for _, removeWay := range []string{"", "trash"} {
		t.Run("remove_way="+removeWay, func(t *testing.T) {
			mock := newGraphMock(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})

			if err := newRemoveDriver(removeWay).Remove(t.Context(), fileObject("item-1")); err != nil {
				t.Fatalf("Remove() error = %v", err)
			}
			mock.assertRequests(t, graphCall{
				method: http.MethodDelete,
				path:   "/v1.0/users/" + testUser + "/drive/root:/dir/file.txt:",
			})
		})
	}
}

func TestRemovePermanentlyDeletesKnownItem(t *testing.T) {
	mock := newGraphMock(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/users/" + testUser + "/drive":
			writeJSON(t, w, `{"id":"drive-1"}`)
		case "/v1.0/drives/drive-1/items/item-1/permanentDelete":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Graph request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := newRemoveDriver("delete").Remove(t.Context(), fileObject("item-1")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	mock.assertRequests(t,
		graphCall{method: http.MethodGet, path: "/v1.0/users/" + testUser + "/drive"},
		graphCall{method: http.MethodPost, path: "/v1.0/drives/drive-1/items/item-1/permanentDelete"},
	)
}

func TestRemovePermanentlyDeletesResolvingItemFromPath(t *testing.T) {
	mock := newGraphMock(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/users/" + testUser + "/drive/root:/dir/file.txt:":
			writeJSON(t, w, `{"id":"item-9","parentReference":{"driveId":"drive-9"}}`)
		case "/v1.0/drives/drive-9/items/item-9/permanentDelete":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Graph request: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	})

	if err := newRemoveDriver("delete").Remove(t.Context(), fileObject("")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	// The item metadata already carries the drive id, so the drive is not fetched again.
	mock.assertRequests(t,
		graphCall{method: http.MethodGet, path: "/v1.0/users/" + testUser + "/drive/root:/dir/file.txt:"},
		graphCall{method: http.MethodPost, path: "/v1.0/drives/drive-9/items/item-9/permanentDelete"},
	)
}

func TestRemovePermanentlyDeletesReturnsGraphError(t *testing.T) {
	mock := newGraphMock(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1.0/users/"+testUser+"/drive" {
			writeJSON(t, w, `{"id":"drive-1"}`)
			return
		}
		writeJSON(t, w, `{"error":{"code":"itemNotFound","message":"The resource could not be found."}}`, http.StatusNotFound)
	})

	err := newRemoveDriver("delete").Remove(t.Context(), fileObject("item-1"))
	if err == nil {
		t.Fatal("Remove() error = nil, want the Graph error")
	}
	if !strings.Contains(err.Error(), "The resource could not be found.") {
		t.Errorf("Remove() error = %v, want it to carry the Graph message", err)
	}
	mock.assertRequests(t,
		graphCall{method: http.MethodGet, path: "/v1.0/users/" + testUser + "/drive"},
		graphCall{method: http.MethodPost, path: "/v1.0/drives/drive-1/items/item-1/permanentDelete"},
	)
}

func TestRemovePermanentlyDeletesFailsWithoutItemID(t *testing.T) {
	newGraphMock(t, func(w http.ResponseWriter, r *http.Request) {
		writeJSON(t, w, `{}`)
	})

	err := newRemoveDriver("delete").Remove(t.Context(), fileObject(""))
	if err == nil {
		t.Fatal("Remove() error = nil, want an error when the item id cannot be resolved")
	}
}
