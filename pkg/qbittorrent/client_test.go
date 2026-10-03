package qbittorrent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"strings"
	"testing"
	"time"
)

// An adapter implementing the original four methods remains a valid Client.
type legacyClient struct{}

func (legacyClient) AddFromLink(string, string, string) error { return nil }
func (legacyClient) GetInfo(string) (TorrentInfo, error)      { return TorrentInfo{}, nil }
func (legacyClient) GetFiles(string) ([]FileInfo, error)      { return nil, nil }
func (legacyClient) Delete(string, bool) error                { return nil }

var _ Client = legacyClient{}

func TestDeleteContextStopsBeforeAuthorizationWhenCanceled(t *testing.T) {
	endpoint, err := url.Parse("http://127.0.0.1:1/")
	if err != nil {
		t.Fatalf("url.Parse() error = %v", err)
	}
	client := &client{
		url:    endpoint,
		client: http.Client{},
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := client.DeleteContext(ctx, "provider-task-id", false); !errors.Is(err, context.Canceled) {
		t.Fatalf("DeleteContext() error = %v, want context.Canceled", err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func response(code int, body string) *http.Response {
	return &http.Response{StatusCode: code, Status: fmt.Sprint(code), Body: io.NopCloser(strings.NewReader(body))}
}

func testCookieJar(t *testing.T) http.CookieJar {
	t.Helper()
	jar, err := cookiejar.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	return jar
}

func TestDeleteContextUsesOneDeadlineForEveryRequest(t *testing.T) {
	// Expire cleanup during each phase, including a session that needs login.
	paths := []string{"/api/v2/app/version", "/api/v2/auth/login", "/api/v2/app/version", "/api/v2/torrents/info", "/api/v2/torrents/delete", "/api/v2/torrents/deleteTags"}
	for stop := range paths {
		t.Run(fmt.Sprint(stop), func(t *testing.T) {
			type key struct{}
			parent, cancelParent := context.WithCancel(context.WithValue(context.Background(), key{}, "request-value"))
			cancelParent()
			ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), time.Minute)
			defer cancel()
			deadline, _ := ctx.Deadline()
			step := 0
			endpoint, _ := url.Parse("http://user:password@qbit.invalid/")
			c := &client{url: endpoint, client: http.Client{Jar: testCookieJar(t), Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if step > stop || req.URL.Path != paths[step] {
					t.Fatalf("request %d: %s", step, req.URL.Path)
				}
				gotDeadline, _ := req.Context().Deadline()
				if req.Context().Err() != nil || !gotDeadline.Equal(deadline) || req.Context().Value(key{}) != "request-value" {
					t.Fatalf("request %d lost cleanup context", step)
				}
				current := step
				step++
				if current == stop {
					cancel()
					return nil, req.Context().Err()
				}
				switch current {
				case 0:
					return response(http.StatusForbidden, ""), nil
				case 1:
					return response(http.StatusNoContent, ""), nil
				case 3:
					return response(http.StatusOK, `[{"hash":"abc"}]`), nil
				default:
					return response(http.StatusOK, ""), nil
				}
			})}}
			if err := c.DeleteContext(ctx, "job", false); !errors.Is(err, context.Canceled) {
				t.Fatalf("DeleteContext() = %v", err)
			}
			if step != stop+1 {
				t.Fatalf("requests = %d, want %d", step, stop+1)
			}
		})
	}
}

func TestDeleteContextKeepsFilesAndDeletesOnlyTaskTag(t *testing.T) {
	for _, code := range []int{http.StatusOK, http.StatusNoContent} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			endpoint, _ := url.Parse("http://qbit.invalid/")
			var mutations []string
			c := &client{url: endpoint, client: http.Client{Jar: testCookieJar(t), Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				switch req.URL.Path {
				case "/api/v2/app/version":
					return response(http.StatusOK, "v5"), nil
				case "/api/v2/torrents/info":
					if req.Form.Get("tag") != "openlist-job" {
						t.Fatal(req.Form)
					}
					return response(http.StatusOK, `[{"hash":"abc"}]`), nil
				case "/api/v2/torrents/delete":
					if req.Form.Get("hashes") != "abc" || req.Form.Get("deleteFiles") != "false" {
						t.Fatal(req.Form)
					}
				case "/api/v2/torrents/deleteTags":
					if req.Form.Get("tags") != "openlist-job" {
						t.Fatal(req.Form)
					}
				default:
					t.Fatalf("unexpected request: %s", req.URL.Path)
				}
				mutations = append(mutations, req.URL.Path)
				return response(code, ""), nil
			})}}
			if err := c.DeleteContext(context.Background(), "job", false); err != nil {
				t.Fatal(err)
			}
			if len(mutations) != 2 {
				t.Fatalf("mutations = %v", mutations)
			}
		})
	}
}
