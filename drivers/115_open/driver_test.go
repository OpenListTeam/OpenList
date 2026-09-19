package _115_open

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	sdk "github.com/OpenListTeam/115-sdk-go"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

func TestPutRapidUploadOverwritesExistingFileByID(t *testing.T) {
	var calls []string
	var deleteForm url.Values
	d, closeServer := newTestOpen115(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/open/upload/init":
			writeJSON(t, w, `{"state":true,"data":{"status":2,"file_id":"new-id","pick_code":"new-pick"}}`)
		case "/open/ufile/delete":
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse delete form: %v", err)
			}
			deleteForm = r.Form
			writeJSON(t, w, `{"state":true,"data":[]}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	file := newTestUploadStream()
	defer file.Close()
	file.SetExist(&Obj{Fid: "old-id", Pid: "stale-parent", Fn: file.GetName(), Fc: "1"})
	dstDir := &Obj{Fid: "parent-id", Fn: "parent", Fc: "0"}
	progress := 0.0

	err := d.Put(context.Background(), dstDir, file, func(p float64) { progress = p })
	if err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if got, want := calls, []string{"/open/upload/init", "/open/ufile/delete"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected request order: got %v, want %v", got, want)
	}
	if got := deleteForm.Get("file_ids"); got != "old-id" {
		t.Errorf("deleted file id = %q, want old-id", got)
	}
	if got := deleteForm.Get("parent_id"); got != "parent-id" {
		t.Errorf("delete parent id = %q, want parent-id", got)
	}
	if progress != 100 {
		t.Errorf("progress = %v, want 100", progress)
	}
}

func TestPutRapidUploadWithoutExistingFileDoesNotDelete(t *testing.T) {
	var calls []string
	d, closeServer := newTestOpen115(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		if r.URL.Path != "/open/upload/init" {
			http.NotFound(w, r)
			return
		}
		writeJSON(t, w, `{"state":true,"data":{"status":2,"file_id":"new-id","pick_code":"new-pick"}}`)
	})
	defer closeServer()

	file := newTestUploadStream()
	defer file.Close()
	dstDir := &Obj{Fid: "parent-id", Fn: "parent", Fc: "0"}

	if err := d.Put(context.Background(), dstDir, file, func(float64) {}); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	if got, want := calls, []string{"/open/upload/init"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected requests: got %v, want %v", got, want)
	}
}

func TestPutSecondVerificationOverwritesExistingFile(t *testing.T) {
	var calls []string
	initCalls := 0
	d, closeServer := newTestOpen115(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/open/upload/init":
			initCalls++
			if initCalls == 1 {
				writeJSON(t, w, `{"state":true,"data":{"status":7,"sign_key":"challenge","sign_check":"0-2"}}`)
				return
			}
			if err := r.ParseForm(); err != nil {
				t.Errorf("parse verification form: %v", err)
			}
			if got := r.Form.Get("sign_key"); got != "challenge" {
				t.Errorf("sign_key = %q, want challenge", got)
			}
			wantSignVal := strings.ToUpper(utils.HashData(utils.SHA1, []byte("rep")))
			if got := r.Form.Get("sign_val"); got != wantSignVal {
				t.Errorf("sign_val = %q, want %q", got, wantSignVal)
			}
			writeJSON(t, w, `{"state":true,"data":{"status":2,"file_id":"new-id","pick_code":"new-pick"}}`)
		case "/open/ufile/delete":
			writeJSON(t, w, `{"state":true,"data":[]}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	file := newTestUploadStream()
	defer file.Close()
	file.SetExist(&Obj{Fid: "old-id", Pid: "parent-id", Fn: file.GetName(), Fc: "1"})
	dstDir := &Obj{Fid: "parent-id", Fn: "parent", Fc: "0"}

	if err := d.Put(context.Background(), dstDir, file, func(float64) {}); err != nil {
		t.Fatalf("Put failed: %v", err)
	}
	want := []string{"/open/upload/init", "/open/upload/init", "/open/ufile/delete"}
	if !slices.Equal(calls, want) {
		t.Fatalf("unexpected requests: got %v, want %v", calls, want)
	}
}

func TestPutDoesNotRemoveExistingFileWhenUploadFails(t *testing.T) {
	var calls []string
	d, closeServer := newTestOpen115(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		writeJSON(t, w, `{"state":false,"code":500,"message":"upload failed"}`)
	})
	defer closeServer()

	file := newTestUploadStream()
	defer file.Close()
	file.SetExist(&Obj{Fid: "old-id", Pid: "parent-id", Fn: file.GetName(), Fc: "1"})
	dstDir := &Obj{Fid: "parent-id", Fn: "parent", Fc: "0"}

	if err := d.Put(context.Background(), dstDir, file, func(float64) {}); err == nil {
		t.Fatal("Put succeeded, want upload error")
	}
	if got, want := calls, []string{"/open/upload/init"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected requests: got %v, want %v", got, want)
	}
}

func TestPutReturnsErrorWhenOverwrittenFileCannotBeRemoved(t *testing.T) {
	var calls []string
	d, closeServer := newTestOpen115(t, func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.URL.Path)
		switch r.URL.Path {
		case "/open/upload/init":
			writeJSON(t, w, `{"state":true,"data":{"status":2,"file_id":"new-id","pick_code":"new-pick"}}`)
		case "/open/ufile/delete":
			writeJSON(t, w, `{"state":false,"code":500,"message":"delete failed"}`)
		default:
			http.NotFound(w, r)
		}
	})
	defer closeServer()

	file := newTestUploadStream()
	defer file.Close()
	file.SetExist(&Obj{Fid: "old-id", Pid: "parent-id", Fn: file.GetName(), Fc: "1"})
	dstDir := &Obj{Fid: "parent-id", Fn: "parent", Fc: "0"}

	err := d.Put(context.Background(), dstDir, file, func(float64) {})
	if err == nil || !strings.Contains(err.Error(), "failed to remove overwritten file") {
		t.Fatalf("Put error = %v, want overwritten-file removal error", err)
	}
	if got, want := calls, []string{"/open/upload/init", "/open/ufile/delete"}; !slices.Equal(got, want) {
		t.Fatalf("unexpected requests: got %v, want %v", got, want)
	}
}

func TestParseCallbackResult(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		result, err := parseCallbackResult([]byte(`{"state":true,"data":{"file_id":"new-id","file_name":"file.bin"}}`))
		if err != nil {
			t.Fatalf("parseCallbackResult failed: %v", err)
		}
		if result.Data.FileID != "new-id" {
			t.Fatalf("file id = %q, want new-id", result.Data.FileID)
		}
	})

	for _, tc := range []struct {
		name string
		body string
	}{
		{name: "callback error", body: `{"state":false,"code":500,"message":"failed"}`},
		{name: "missing file id", body: `{"state":true,"data":{}}`},
		{name: "invalid json", body: `{`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := parseCallbackResult([]byte(tc.body)); err == nil {
				t.Fatal("parseCallbackResult succeeded, want error")
			}
		})
	}
}

func newTestUploadStream() *stream.FileStream {
	data := []byte("replacement content")
	return &stream.FileStream{
		Obj: &model.Object{
			Name:     "same.bin",
			Size:     int64(len(data)),
			HashInfo: utils.NewHashInfo(utils.SHA1, utils.HashData(utils.SHA1, data)),
		},
		Reader: bytes.NewReader(data),
	}
}

func newTestOpen115(t *testing.T, handler http.HandlerFunc) (*Open115, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	target, err := url.Parse(server.URL)
	if err != nil {
		server.Close()
		t.Fatalf("parse test server URL: %v", err)
	}
	client := sdk.New().SetAccessToken("test-token")
	client.SetHttpClient(&http.Client{Transport: &rewriteTransport{
		target: target,
		base:   server.Client().Transport,
	}})
	return &Open115{client: client}, server.Close
}

type rewriteTransport struct {
	target *url.URL
	base   http.RoundTripper
}

func (t *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	clone.URL.Scheme = t.target.Scheme
	clone.URL.Host = t.target.Host
	clone.Host = t.target.Host
	return t.base.RoundTrip(clone)
}

func writeJSON(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if _, err := io.WriteString(w, body); err != nil {
		t.Errorf("write response: %v", err)
	}
}
