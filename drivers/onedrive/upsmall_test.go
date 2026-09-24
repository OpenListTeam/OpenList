package onedrive

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/go-resty/resty/v2"
)

const testRegion = "upsmall-unittest"

// newUpSmallTestDriver spins up a fake Graph API whose PUT /content reports
// the given storedSize in the returned driveItem.
func newUpSmallTestDriver(t *testing.T, storedSize int64) *Onedrive {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.Method {
		case http.MethodPut: // PUT .../content returns the created driveItem
			_ = json.NewEncoder(w).Encode(File{Id: "test-id", Name: "file.bin", Size: storedSize})
		case http.MethodPatch: // updateMetadata
			_ = json.NewEncoder(w).Encode(File{Id: "test-id", Name: "file.bin", Size: storedSize})
		default:
			http.Error(w, "unexpected "+r.Method, http.StatusBadRequest)
		}
	}))
	t.Cleanup(srv.Close)

	onedriveHostMap[testRegion] = Host{Api: srv.URL}

	oldClient := base.RestyClient
	base.RestyClient = resty.New()
	t.Cleanup(func() { base.RestyClient = oldClient })

	d := &Onedrive{}
	d.Region = testRegion
	d.AccessToken = "test-token"
	return d
}

func TestUpSmallVerifyStoredSize(t *testing.T) {
	testcases := []struct {
		name      string
		streamSz  int64 // size advertised by the stream (-1 = unknown, e.g. chunked PUT)
		storedSz  int64 // size reported by OneDrive in the driveItem response
		wantError bool
	}{
		{"stored size matches", 169800, 169800, false},
		{"stalled upload stored zero", 169800, 0, true},
		{"unknown stream size skipped", -1, 0, false},
		{"empty file", 0, 0, false},
	}

	payload := bytes.Repeat([]byte("x"), 169800)
	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			d := newUpSmallTestDriver(t, tc.storedSz)
			ss, err := stream.NewSeekableStream(&stream.FileStream{
				Obj:    &model.Object{Path: "/dir/file.bin", Name: "file.bin", Size: tc.streamSz},
				Reader: bytes.NewReader(payload),
			}, &model.Link{})
			if err != nil {
				t.Fatalf("NewSeekableStream: %v", err)
			}

			err = d.upSmall(context.Background(), &model.Object{Path: "/dir"}, ss)
			if tc.wantError {
				if err == nil {
					t.Error("expected size mismatch error, got nil")
				} else if !strings.Contains(err.Error(), "size mismatch") {
					t.Errorf("error should mention size mismatch, got: %v", err)
				}
			} else if err != nil {
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
}
