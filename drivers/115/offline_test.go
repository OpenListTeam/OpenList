package _115

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
)

type offlineRoundTripFunc func(*http.Request) (*http.Response, error)

func (f offlineRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestDeleteOfflineTasksUsesCleanupContext(t *testing.T) {
	type contextKey struct{}
	parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), contextKey{}, "cleanup"))
	cancelParent()
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 50*time.Millisecond)
	defer cancel()
	deadline, _ := ctx.Deadline()
	called := false
	d := &Pan115{client: driver115.New(driver115.WithClient(&http.Client{
		Transport: offlineRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			called = true
			gotDeadline, ok := req.Context().Deadline()
			if !ok || !gotDeadline.Equal(deadline) || req.Context().Value(contextKey{}) != "cleanup" {
				t.Error("HTTP request lost the cleanup deadline or values")
				return nil, errors.New("missing cleanup context")
			}
			// A stalled endpoint must release the worker when cleanup expires.
			<-req.Context().Done()
			return nil, req.Context().Err()
		}),
	}))}
	if err := d.DeleteOfflineTasks(ctx, []string{"task"}, false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DeleteOfflineTasks() = %v", err)
	}
	if !called {
		t.Fatal("cleanup request was skipped")
	}
	if parent.Err() != context.Canceled {
		t.Fatal("original task context changed")
	}
}

func TestDeleteOfflineTasksPreservesProtocol(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deleteFiles bool
		flag        string
		body        string
		wantErr     bool
	}{
		{"keep files", false, "0", `{"state":true}`, false},
		{"delete files", true, "1", `{"state":true}`, false},
		{"provider error", false, "0", `{"state":false,"errno":99,"error":"denied"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			d := &Pan115{client: driver115.New(driver115.WithClient(&http.Client{
				Transport: offlineRoundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != http.MethodPost || req.URL.String() != driver115.ApiDelOfflineUrl {
						t.Errorf("request = %s %s", req.Method, req.URL)
					}
					if err := req.ParseForm(); err != nil {
						t.Fatal(err)
					}
					if !reflect.DeepEqual(req.PostForm["hash"], []string{"first", "second"}) || req.PostForm.Get("flag") != tc.flag {
						t.Errorf("form = %v", req.PostForm)
					}
					cookie, err := req.Cookie("UID")
					if err != nil || cookie.Value != "test-user" {
						t.Fatal("SDK authentication cookie missing")
					}
					return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
				}),
			}))}
			d.client.SetCookies(&http.Cookie{Name: "UID", Value: "test-user"})
			if err := d.DeleteOfflineTasks(context.Background(), []string{"first", "second"}, tc.deleteFiles); (err != nil) != tc.wantErr {
				t.Fatalf("DeleteOfflineTasks() = %v", err)
			}
			if calls != 1 {
				t.Fatalf("requests = %d", calls)
			}
		})
	}
}
