package transmission

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/offline_download/tool"
	"github.com/hekmon/transmissionrpc/v3"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestRemovePreservesFilesAcrossSessionIDRetry(t *testing.T) {
	parent, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Minute)
	defer cancel()
	deadline, _ := ctx.Deadline()
	calls := 0
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		gotDeadline, _ := req.Context().Deadline()
		if req.Context().Err() != nil || !gotDeadline.Equal(deadline) {
			t.Fatal("cleanup context lost during RPC retry")
		}
		var payload struct {
			Method    string `json:"method"`
			Tag       int    `json:"tag"`
			Arguments struct {
				IDs             []int64 `json:"ids"`
				DeleteLocalData *bool   `json:"delete-local-data"`
			} `json:"arguments"`
		}
		if err := json.NewDecoder(req.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload.Method != "torrent-remove" || len(payload.Arguments.IDs) != 1 || payload.Arguments.IDs[0] != 7 || payload.Arguments.DeleteLocalData == nil || *payload.Arguments.DeleteLocalData {
			t.Fatalf("unexpected remove payload: %+v", payload)
		}
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusConflict, Header: http.Header{"X-Transmission-Session-Id": []string{"session"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if calls != 2 || req.Header.Get("X-Transmission-Session-Id") != "session" {
			t.Fatal("unexpected session retry")
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(fmt.Sprintf(`{"result":"success","arguments":{},"tag":%d}`, payload.Tag)))}, nil
	})}
	endpoint, _ := url.Parse("http://transmission.invalid/rpc")
	client, err := transmissionrpc.New(endpoint, &transmissionrpc.Config{CustomClient: httpClient})
	if err != nil {
		t.Fatal(err)
	}
	task := &tool.DownloadTask{GID: "7"}
	task.SetCtx(parent)
	if err := (&Transmission{client: client}).Remove(ctx, task); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("RPC calls = %d", calls)
	}
}

func TestRemoveUsesCallerCleanupContextAndStopsAtDeadline(t *testing.T) {
	requestStarted := false
	httpClient := &http.Client{
		Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			requestStarted = true
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}
	endpoint, err := url.Parse("http://transmission.invalid/rpc")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	client, err := transmissionrpc.New(endpoint, &transmissionrpc.Config{CustomClient: httpClient})
	if err != nil {
		t.Fatalf("transmissionrpc.New() error = %v", err)
	}

	parentCtx, cancelParent := context.WithCancel(context.Background())
	cancelParent()
	transmission := &Transmission{client: client}

	cleanupCtx, cleanupCancel := context.WithTimeout(context.WithoutCancel(parentCtx), 25*time.Millisecond)
	defer cleanupCancel()
	err = transmission.Remove(cleanupCtx, &tool.DownloadTask{GID: "1"})
	if !requestStarted {
		t.Fatal("Remove() did not run with the detached cleanup context")
	}
	if !stderrors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Remove() error = %v, want context.DeadlineExceeded", err)
	}
}
