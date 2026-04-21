package github

import (
	"context"
	stdjson "encoding/json"
	"fmt"
	"net/http"
	stdpath "path"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

const (
	mtimeBatchSize        = 50
	mtimeMaxEntries       = 200
	githubGraphQLEndpoint = "https://api.github.com/graphql"
)

var githubZeroTime = time.Unix(0, 0)

type graphQLHistoryNode struct {
	CommittedDate time.Time `json:"committedDate"`
}

type graphQLHistory struct {
	Nodes []graphQLHistoryNode `json:"nodes"`
}

type graphQLBatchNode struct {
	TypeName string                    `json:"__typename"`
	OID      string                    `json:"oid"`
	Target   *graphQLBatchNode         `json:"target"`
	Aliases  map[string]graphQLHistory `json:"-"`
}

func (n *graphQLBatchNode) UnmarshalJSON(data []byte) error {
	type rawNode graphQLBatchNode

	var decoded rawNode
	if err := utils.Json.Unmarshal(data, &decoded); err != nil {
		return err
	}

	var rawFields map[string]stdjson.RawMessage
	if err := utils.Json.Unmarshal(data, &rawFields); err != nil {
		return err
	}

	aliases := make(map[string]graphQLHistory)
	for key, value := range rawFields {
		switch key {
		case "__typename", "oid", "target":
			continue
		}

		var history graphQLHistory
		if err := utils.Json.Unmarshal(value, &history); err != nil {
			return err
		}
		aliases[key] = history
	}

	*n = graphQLBatchNode(decoded)
	n.Aliases = aliases
	return nil
}

type graphQLBatchResponse struct {
	Data struct {
		Repository struct {
			RefTarget *graphQLBatchNode `json:"refTarget"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct {
		Message string `json:"message"`
	} `json:"errors"`
}

func shouldUseAccurateMtime(enabled bool, token string, entryCount int) (bool, string) {
	switch {
	case !enabled:
		return false, "disabled"
	case strings.TrimSpace(token) == "":
		return false, "missing_token"
	case entryCount > mtimeMaxEntries:
		return false, "entry_limit"
	default:
		return true, ""
	}
}

func collectMtimePaths(dirPath string, contents []Object, tree []TreeObjResp) []string {
	if len(contents) > 0 {
		paths := make([]string, 0, len(contents))
		for _, entry := range contents {
			paths = append(paths, utils.FixAndCleanPath(entry.Path))
		}
		return paths
	}

	paths := make([]string, 0, len(tree))
	for _, entry := range tree {
		paths = append(paths, utils.FixAndCleanPath(stdpath.Join(dirPath, entry.Path)))
	}
	return paths
}

func buildMtimeBatchQuery(owner, repo, expression string, paths []string) (string, map[string]string) {
	aliasToPath := make(map[string]string, len(paths))
	historyFields := make([]string, 0, len(paths))
	for i, rawPath := range paths {
		alias := fmt.Sprintf("p%d", i)
		normalized := utils.FixAndCleanPath(rawPath)
		aliasToPath[alias] = normalized
		historyFields = append(historyFields, fmt.Sprintf(`%s: history(first: 1, path: %q) { nodes { committedDate } }`, alias, strings.TrimPrefix(normalized, "/")))
	}

	commitFields := strings.Join(historyFields, "\n")
	query := fmt.Sprintf(`query {
	repository(owner: %q, name: %q) {
		refTarget: object(expression: %q) {
			__typename
			... on Commit {
				oid
				%s
			}
			... on Tag {
				target {
					__typename
					... on Commit {
						oid
						%s
					}
				}
			}
		}
	}
}`,
		owner,
		repo,
		expression,
		commitFields,
		commitFields,
	)
	return query, aliasToPath
}

func parseMtimeBatchResult(body []byte, aliasToPath map[string]string) (string, map[string]time.Time, error) {
	var resp graphQLBatchResponse
	if err := utils.Json.Unmarshal(body, &resp); err != nil {
		return "", nil, err
	}
	if len(resp.Errors) > 0 {
		return "", nil, fmt.Errorf("graphql returned %d top-level errors", len(resp.Errors))
	}

	commit := resp.Data.Repository.RefTarget
	if commit == nil {
		return "", nil, fmt.Errorf("graphql returned empty ref target")
	}
	if commit.TypeName == "Tag" {
		if commit.Target == nil || commit.Target.TypeName != "Commit" {
			return "", nil, fmt.Errorf("graphql tag did not resolve to commit")
		}
		commit = commit.Target
	} else if commit.TypeName != "Commit" {
		return "", nil, fmt.Errorf("graphql did not resolve commit target")
	}
	if commit.OID == "" {
		return "", nil, fmt.Errorf("graphql did not resolve a commit oid")
	}

	modified := make(map[string]time.Time, len(aliasToPath))
	for alias, path := range aliasToPath {
		history, ok := commit.Aliases[alias]
		if !ok || len(history.Nodes) == 0 {
			continue
		}
		modified[utils.FixAndCleanPath(path)] = history.Nodes[0].CommittedDate
	}
	return commit.OID, modified, nil
}

func applyModifiedTimes(objs []model.Obj, modified map[string]time.Time) {
	for _, obj := range objs {
		raw, ok := obj.(*model.Object)
		if !ok {
			continue
		}
		if stamp, exists := modified[raw.GetPath()]; exists {
			raw.Modified = stamp
		}
		if raw.Ctime.IsZero() {
			raw.Ctime = githubZeroTime
		}
	}
}

func (d *Github) fetchAccurateModifiedTimes(ctx context.Context, dirPath string, objs []model.Obj, contents []Object) {
	ok, reason := shouldUseAccurateMtime(d.AccurateModifiedTime, d.Token, len(objs))
	if !ok {
		if reason != "" {
			log.Debugf("github accurate mtime skipped for %s: %s", dirPath, reason)
		}
		return
	}

	paths := collectMtimePaths(dirPath, contents, nil)
	if len(paths) == 0 {
		return
	}

	commitExpr := d.Ref
	totalBatches := (len(paths) + mtimeBatchSize - 1) / mtimeBatchSize
	for start := 0; start < len(paths); start += mtimeBatchSize {
		end := start + mtimeBatchSize
		if end > len(paths) {
			end = len(paths)
		}

		query, aliasToPath := buildMtimeBatchQuery(d.Owner, d.Repo, commitExpr, paths[start:end])
		request := d.client.R().
			SetContext(ctx).
			SetHeader("Accept", "application/vnd.github+json").
			SetBody(map[string]string{"query": query})
		if token := strings.TrimSpace(d.Token); token != "" {
			request.SetHeader("Authorization", "Bearer "+token)
		}

		res, err := request.Post(githubGraphQLEndpoint)
		if err != nil {
			log.WithError(err).Warnf("github accurate mtime stopped for %s after %d/%d batches: transport", dirPath, start/mtimeBatchSize+1, totalBatches)
			return
		}
		if res.StatusCode() != http.StatusOK {
			log.Warnf("github accurate mtime stopped for %s after %d/%d batches: http_%d", dirPath, start/mtimeBatchSize+1, totalBatches, res.StatusCode())
			return
		}

		resolvedCommitExpr, modified, err := parseMtimeBatchResult(res.Body(), aliasToPath)
		if err != nil {
			log.WithError(err).Warnf("github accurate mtime stopped for %s after %d/%d batches: graphql", dirPath, start/mtimeBatchSize+1, totalBatches)
			return
		}
		commitExpr = resolvedCommitExpr
		applyModifiedTimes(objs, modified)

		if res.Header().Get("X-Ratelimit-Remaining") == "0" {
			log.Warnf("github accurate mtime stopped for %s after %d/%d batches: rate_limit", dirPath, start/mtimeBatchSize+1, totalBatches)
			return
		}
	}
}
