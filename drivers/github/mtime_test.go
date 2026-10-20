package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	odriver "github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/go-resty/resty/v2"
)

func TestDriverInfoIncludesAccurateModifiedTimeDefault(t *testing.T) {
	info := op.GetDriverInfoMap()["GitHub API"]
	for _, item := range info.Additional {
		if item.Name != "accurate_modified_time" {
			continue
		}
		if item.Default != "false" {
			t.Fatalf("unexpected default: %q", item.Default)
		}
		return
	}
	t.Fatal("accurate_modified_time item not registered")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func newGithubTestDriver(rt roundTripFunc, token string, enabled bool) *Github {
	return &Github{
		Storage: model.Storage{MountPath: "/github-test", CacheExpiration: 10},
		Addition: Addition{
			RootPath:             odriver.RootPath{RootFolderPath: "/"},
			Token:                token,
			Owner:                "owner",
			Repo:                 "repo",
			Ref:                  "main",
			AccurateModifiedTime: enabled,
		},
		client: resty.New().SetTransport(rt),
	}
}

func newJSONResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal json: %v", err)
	}
	return string(data)
}

func graphQLQueryFromRequest(t *testing.T, r *http.Request) string {
	t.Helper()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatalf("read graphql request body: %v", err)
	}
	var payload map[string]string
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode graphql request body: %v", err)
	}
	query := payload["query"]
	if query == "" {
		t.Fatalf("graphql request missing query: %s", string(body))
	}
	return query
}

func newContentsPayload(t *testing.T, entries []Object) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"type":    "dir",
		"sha":     "tree-sha",
		"entries": entries,
	})
}

func newTreePayload(t *testing.T, sha string, trees []TreeObjResp) string {
	t.Helper()
	return mustJSON(t, map[string]any{
		"sha":       sha,
		"truncated": false,
		"tree":      trees,
	})
}

func newCommitGraphQLPayload(t *testing.T, histories map[string][]string) string {
	t.Helper()
	commit := make(map[string]any, len(histories))
	for alias, dates := range histories {
		nodes := make([]map[string]string, 0, len(dates))
		for _, date := range dates {
			nodes = append(nodes, map[string]string{"committedDate": date})
		}
		commit[alias] = map[string]any{"nodes": nodes}
	}
	return mustJSON(t, map[string]any{
		"data": map[string]any{
			"repository": map[string]any{"commit": commit},
		},
	})
}

func newSequentialEntries(count int) []Object {
	entries := make([]Object, 0, count)
	for i := range count {
		name := fmt.Sprintf("%03d.md", i)
		entries = append(entries, Object{Name: name, Path: "docs/" + name, Type: "file", Size: 1})
	}
	return entries
}

func mustObject(t *testing.T, obj model.Obj) *model.Object {
	t.Helper()
	raw, ok := model.UnwrapObjName(obj).(*model.Object)
	if !ok {
		t.Fatalf("unexpected obj type %T", obj)
	}
	return raw
}

func TestListAppliesAccurateModifiedTimeInOneRequest(t *testing.T) {
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	entries := []Object{
		{Name: "a.md", Path: "docs/a.md", Type: "file", Size: 1},
		{Name: ".gitkeep", Path: "docs/.gitkeep", Type: "file"},
		{Name: `quote " 文.md`, Path: `docs/quote " 文.md`, Type: "file", Size: 1},
		{Name: "control.md", Path: "docs/control\x01.md", Type: "file", Size: 1},
	}
	graphqlCalls := 0
	var query string
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			query = graphQLQueryFromRequest(t, r)
			if got := r.Header.Get("Authorization"); got != "Bearer token" {
				t.Fatalf("unexpected authorization header: %q", got)
			}
			return newJSONResponse(http.StatusOK, newCommitGraphQLPayload(t, map[string][]string{
				"p0": {stamp.Format(time.RFC3339)},
				"p1": {},
				"p2": {},
			})), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 1 {
		t.Fatalf("expected one GraphQL request, got %d", graphqlCalls)
	}
	if strings.Count(query, "history(first: 1") != 3 ||
		!strings.Contains(query, `object(expression: "main^{commit}")`) ||
		!strings.Contains(query, `p0: history(first: 1, path: "docs/a.md")`) ||
		!strings.Contains(query, `p1: history(first: 1, path: "docs/quote \" 文.md")`) ||
		!strings.Contains(query, `p2: history(first: 1, path: "docs/control\u0001.md")`) {
		t.Fatalf("query should peel the ref and contain all listed paths once:\n%s", query)
	}
	if len(objs) != 3 {
		t.Fatalf("expected three objects after .gitkeep filtering, got %d", len(objs))
	}
	if first := mustObject(t, objs[0]); !first.ModTime().Equal(stamp) || !first.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("unexpected first timestamps: mod=%v create=%v", first.ModTime(), first.CreateTime())
	}
	if second := mustObject(t, objs[1]); !second.ModTime().Equal(githubZeroTime) || !second.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("unmatched entry should retain legacy timestamps: mod=%v create=%v", second.ModTime(), second.CreateTime())
	}
}

func TestListSkipsAccurateModifiedTime(t *testing.T) {
	tests := []struct {
		name    string
		enabled bool
		token   string
		entries int
	}{
		{name: "disabled", enabled: false, token: "token", entries: 1},
		{name: "missing token", enabled: true, token: "", entries: 1},
		{name: "over entry limit", enabled: true, token: "token", entries: 201},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			graphqlCalls := 0
			payload := newContentsPayload(t, newSequentialEntries(tc.entries))
			drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				switch {
				case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
					return newJSONResponse(http.StatusOK, payload), nil
				case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
					graphqlCalls++
					return newJSONResponse(http.StatusOK, newCommitGraphQLPayload(t, nil)), nil
				default:
					return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
				}
			}), tc.token, tc.enabled)

			objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if graphqlCalls != 0 {
				t.Fatalf("expected zero GraphQL requests, got %d", graphqlCalls)
			}
			for _, obj := range objs {
				raw := mustObject(t, obj)
				if !raw.ModTime().Equal(githubZeroTime) || !raw.CreateTime().Equal(githubZeroTime) {
					t.Fatalf("legacy timestamps should be preserved: mod=%v create=%v", raw.ModTime(), raw.CreateTime())
				}
			}
		})
	}
}

func TestListFallsBackWhenGraphQLFails(t *testing.T) {
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, newContentsPayload(t, newSequentialEntries(1))), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, `{"errors":[{"message":"rate limited"}]}`), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("GraphQL failure should be best-effort: %v", err)
	}
	if graphqlCalls != 1 {
		t.Fatalf("expected one GraphQL request, got %d", graphqlCalls)
	}
	obj := mustObject(t, objs[0])
	if !obj.ModTime().Equal(githubZeroTime) || !obj.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("failed GraphQL request should retain legacy timestamps: mod=%v create=%v", obj.ModTime(), obj.CreateTime())
	}
}

func TestListUsesOneGraphQLRequestAtEntryLimit(t *testing.T) {
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	graphqlCalls := 0
	var query string
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, newContentsPayload(t, newSequentialEntries(200))), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			query = graphQLQueryFromRequest(t, r)
			return newJSONResponse(http.StatusOK, newCommitGraphQLPayload(t, map[string][]string{
				"p199": {stamp.Format(time.RFC3339)},
			})), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if graphqlCalls != 1 || strings.Count(query, "history(first: 1") != 200 {
		t.Fatalf("200 entries should share one request: calls=%d histories=%d", graphqlCalls, strings.Count(query, "history(first: 1"))
	}
	if !strings.Contains(query, `p199: history(first: 1, path: "docs/199.md")`) {
		t.Fatalf("query missing final entry:\n%s", query)
	}
	if len(objs) != 200 || !mustObject(t, objs[199]).ModTime().Equal(stamp) {
		t.Fatalf("unexpected final object timestamp")
	}
}

func TestListKeepsTreeFallbackOnLegacyPath(t *testing.T) {
	entries := make([]Object, 0, 1000)
	for i := range 1000 {
		name := fmt.Sprintf("dir-%d", i)
		entries = append(entries, Object{Name: name, Path: "docs/" + name, Type: "dir"})
	}
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, newContentsPayload(t, entries)), nil
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/git/trees/"):
			return newJSONResponse(http.StatusOK, newTreePayload(t, "tree-sha", []TreeObjResp{{TreeObjReq: TreeObjReq{Path: "child.md", Mode: "100644", Type: "blob", Sha: "blob-sha"}, Size: 1, URL: "https://example.invalid/blob"}})), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, newCommitGraphQLPayload(t, nil)), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	objs, err := drv.List(context.Background(), &model.Object{Path: "/docs", Name: "docs", IsFolder: true}, model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(objs) != 1 {
		t.Fatalf("unexpected tree fallback result length: %d", len(objs))
	}
	first := mustObject(t, objs[0])
	if first.GetPath() != "/child.md" {
		t.Fatalf("unexpected tree fallback path: %s", first.GetPath())
	}
	if !first.ModTime().Equal(githubZeroTime) || !first.CreateTime().Equal(githubZeroTime) {
		t.Fatalf("tree fallback should preserve legacy timestamps: mod=%v create=%v", first.ModTime(), first.CreateTime())
	}
	if graphqlCalls != 0 {
		t.Fatalf("tree fallback should skip GraphQL, got %d calls", graphqlCalls)
	}
}

func TestOpListCacheHitDoesNotRepeatGraphQL(t *testing.T) {
	op.Cache.ClearAll()
	defer op.Cache.ClearAll()
	stamp := time.Date(2025, 12, 22, 4, 52, 41, 0, time.UTC)
	graphqlCalls := 0
	drv := newGithubTestDriver(roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch {
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/contents/"):
			return newJSONResponse(http.StatusOK, newContentsPayload(t, []Object{{Name: "a.md", Path: "a.md", Type: "file", Size: 1}})), nil
		case r.Method == http.MethodPost && r.URL.String() == githubGraphQLEndpoint:
			graphqlCalls++
			return newJSONResponse(http.StatusOK, newCommitGraphQLPayload(t, map[string][]string{"p0": {stamp.Format(time.RFC3339)}})), nil
		default:
			return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}), "token", true)

	first, err := op.List(context.Background(), drv, "/", model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected first list error: %v", err)
	}
	second, err := op.List(context.Background(), drv, "/", model.ListArgs{})
	if err != nil {
		t.Fatalf("unexpected second list error: %v", err)
	}
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("unexpected cached results: first=%d second=%d", len(first), len(second))
	}
	if graphqlCalls != 1 {
		t.Fatalf("expected one GraphQL call across cached lists, got %d", graphqlCalls)
	}
	if !mustObject(t, first[0]).ModTime().Equal(stamp) {
		t.Fatalf("expected first list to include backfilled modified time, got %v", mustObject(t, first[0]).ModTime())
	}
	if !mustObject(t, second[0]).ModTime().Equal(stamp) {
		t.Fatalf("expected cached list to retain modified time, got %v", mustObject(t, second[0]).ModTime())
	}
}
