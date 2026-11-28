package _139

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
)

type testUploadSections struct {
	*strings.Reader
}

func (s testUploadSections) GetSectionReader(offset, length int64) (io.ReadSeeker, error) {
	return io.NewSectionReader(s.Reader, offset, length), nil
}

func (testUploadSections) FreeSectionReader(io.ReadSeeker) {}

func (testUploadSections) DiscardSection(int64, int64) error { return nil }

func TestUploadPersonalPartsRenewsExpiredBatch(t *testing.T) {
	var mu sync.Mutex
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		mu.Lock()
		requests = append(requests, fmt.Sprintf("%s %s %s", r.Method, r.URL.Path, body))
		mu.Unlock()
		if r.URL.Path == "/expired" {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>Request has expired</Message></Error>")
			return
		}
		if r.URL.Path == "/stale-third" {
			t.Error("the third part must use the renewed URL")
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	useUploadTestClient(t, server.Client())

	parts := []PartInfo{
		{PartNumber: 101, PartSize: 2, ParallelHashCtx: ParallelHashCtx{PartOffset: 0}},
		{PartNumber: 102, PartSize: 2, ParallelHashCtx: ParallelHashCtx{PartOffset: 2}},
		{PartNumber: 103, PartSize: 1, ParallelHashCtx: ParallelHashCtx{PartOffset: 4}},
	}
	urls := []PersonalPartInfo{
		{PartNumber: 102, UploadUrl: server.URL + "/expired"},
		{PartNumber: 103, UploadUrl: server.URL + "/stale-third"},
		{PartNumber: 101, UploadUrl: server.URL + "/first"},
	}
	refreshes := 0
	fetchURLs := func(pending []PartInfo) ([]PersonalPartInfo, error) {
		refreshes++
		if !reflect.DeepEqual(pending, parts[1:]) {
			t.Fatalf("renewal requested the wrong parts: %+v", pending)
		}
		return []PersonalPartInfo{
			{PartNumber: 103, UploadUrl: server.URL + "/fresh-third"},
			{PartNumber: 102, UploadUrl: server.URL + "/fresh-second"},
		}, nil
	}
	progress := driver.NewProgress(5, func(float64) {})
	sections := testUploadSections{strings.NewReader("abcde")}
	if err := (&Yun139{}).uploadPersonalParts(context.Background(), parts, urls, fetchURLs, sections, progress); err != nil {
		t.Fatal(err)
	}
	want := []string{"PUT /first ab", "PUT /expired cd", "PUT /fresh-second cd", "PUT /fresh-third e"}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(requests, want) || refreshes != 1 || progress.Done != 5 {
		t.Fatalf("requests = %v, refreshes = %d, uploaded bytes = %d", requests, refreshes, progress.Done)
	}
}

func TestPutPersonalPartDistinguishesExpiredAndDenied(t *testing.T) {
	for _, tt := range []struct {
		name, body string
		expired    bool
	}{
		{"expired", "<Error><Code>RequestExpired</Code></Error>", true},
		{"denied", "<Error><Code>AccessDenied</Code><Message>Access denied</Message></Error>", false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				fmt.Fprint(w, tt.body)
			}))
			defer server.Close()
			useUploadTestClient(t, server.Client())
			err := putPersonalPart(context.Background(), server.URL, 1, strings.NewReader("x"))
			if err == nil || (err == errUploadURLExpired) != tt.expired {
				t.Fatalf("got %v, want expired = %t", err, tt.expired)
			}
		})
	}
}

func useUploadTestClient(t *testing.T, client *http.Client) {
	t.Helper()
	previous := base.HttpClient
	base.HttpClient = client
	t.Cleanup(func() { base.HttpClient = previous })
}
