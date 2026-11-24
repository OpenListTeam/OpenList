package guangyapan

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

// newTestDriver returns a logged-in driver whose API requests are all answered
// with the given HTTP status and JSON body.
func newTestDriver(status int, body string) *GuangYaPan {
	d := &GuangYaPan{}
	d.AccessToken = "test-access-token"
	d.PageSize = 100
	d.apiClient = resty.New().
		SetBaseURL(apiBaseURL).
		SetTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: status,
				Status:     http.StatusText(status),
				Body:       io.NopCloser(strings.NewReader(body)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Request:    r,
			}, nil
		}))
	return d
}

// The API reports business errors with HTTP 200 and a non-zero code in the
// body. Listing must return them instead of an empty folder.
func TestListReportsAPIError(t *testing.T) {
	const body = `{"code":42,"msg":"mock failure"}`
	ctx := context.Background()

	t.Run("List", func(t *testing.T) {
		d := newTestDriver(http.StatusOK, body)
		objs, err := d.List(ctx, &model.Object{IsFolder: true}, model.ListArgs{})
		checkAPIError(t, err)
		if len(objs) != 0 {
			t.Errorf("got %d objects, want none", len(objs))
		}
	})
	t.Run("findChildFolderID", func(t *testing.T) {
		d := newTestDriver(http.StatusOK, body)
		_, err := d.findChildFolderID(ctx, "", "restic")
		checkAPIError(t, err)
	})
}

func checkAPIError(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("got nil error, want the API error")
	}
	if msg := err.Error(); !strings.Contains(msg, "code=42") || !strings.Contains(msg, "mock failure") {
		t.Fatalf("got error %q, want it to carry code=42 and the msg", msg)
	}
}

func TestListParsesFiles(t *testing.T) {
	ctx := context.Background()
	d := newTestDriver(http.StatusOK, `{"code":0,"msg":"success","data":{"total":2,"list":[
		{"fileId":"d1","fileName":"restic","resType":2,"ctime":1700000000,"utime":1700000100},
		{"fileId":"f1","fileName":"a.txt","fileSize":3,"resType":1,"ctime":1700000000,"utime":1700000200}
	]}}`)

	objs, err := d.List(ctx, &model.Object{IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(objs) != 2 {
		t.Fatalf("List returned %d objects, want 2", len(objs))
	}
	if got := objs[0]; got.GetID() != "d1" || got.GetName() != "restic" || !got.IsDir() {
		t.Errorf("first object = %+v, want folder d1 named restic", got)
	}
	if got := objs[1]; got.GetID() != "f1" || got.GetName() != "a.txt" || got.IsDir() || got.GetSize() != 3 {
		t.Errorf("second object = %+v, want 3-byte file f1 named a.txt", got)
	}

	id, err := d.findChildFolderID(ctx, "", "restic")
	if err != nil || id != "d1" {
		t.Fatalf("findChildFolderID = %q, %v; want d1", id, err)
	}
}
