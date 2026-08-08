package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

const (
	mtimeMaxEntries       = 200
	githubGraphQLEndpoint = "https://api.github.com/graphql"
)

var githubZeroTime = time.Unix(0, 0)

type graphQLHistory struct {
	Nodes []struct {
		CommittedDate time.Time `json:"committedDate"`
	} `json:"nodes"`
}

type graphQLMtimeResponse struct {
	Data struct {
		Repository struct {
			Commit map[string]graphQLHistory `json:"commit"`
		} `json:"repository"`
	} `json:"data"`
	Errors []struct{} `json:"errors"`
}

func quoteGraphQLString(value string) string {
	quoted, _ := json.Marshal(value)
	return string(quoted)
}

func buildMtimeQuery(owner, repo, ref string, objs []model.Obj) string {
	histories := make([]string, 0, len(objs))
	for i, obj := range objs {
		path := strings.TrimPrefix(obj.GetPath(), "/")
		histories = append(histories, fmt.Sprintf(`p%d: history(first: 1, path: %s) { nodes { committedDate } }`, i, quoteGraphQLString(path)))
	}

	return fmt.Sprintf(`query {
	repository(owner: %s, name: %s) {
		commit: object(expression: %s) {
			... on Commit {
				%s
			}
		}
	}
}`,
		quoteGraphQLString(owner),
		quoteGraphQLString(repo),
		quoteGraphQLString(ref+"^{commit}"),
		strings.Join(histories, "\n\t\t\t\t"),
	)
}

func (d *Github) fetchAccurateModifiedTimes(ctx context.Context, dirPath string, objs []model.Obj) {
	token := strings.TrimSpace(d.Token)
	if !d.AccurateModifiedTime || token == "" || len(objs) == 0 || len(objs) > mtimeMaxEntries {
		return
	}

	res, err := d.client.R().
		SetContext(ctx).
		SetHeader("Accept", "application/vnd.github+json").
		SetHeader("Authorization", "Bearer "+token).
		SetBody(map[string]string{"query": buildMtimeQuery(d.Owner, d.Repo, d.Ref, objs)}).
		Post(githubGraphQLEndpoint)
	if err != nil {
		log.WithError(err).Warnf("github accurate mtime failed for %s: transport", dirPath)
		return
	}
	if res.StatusCode() != http.StatusOK {
		log.Warnf("github accurate mtime failed for %s: http_%d", dirPath, res.StatusCode())
		return
	}

	var response graphQLMtimeResponse
	if err := utils.Json.Unmarshal(res.Body(), &response); err != nil {
		log.WithError(err).Warnf("github accurate mtime failed for %s: graphql", dirPath)
		return
	}
	if len(response.Errors) > 0 || response.Data.Repository.Commit == nil {
		log.Warnf("github accurate mtime failed for %s: graphql", dirPath)
		return
	}

	for i, obj := range objs {
		history := response.Data.Repository.Commit[fmt.Sprintf("p%d", i)]
		if len(history.Nodes) == 0 {
			continue
		}
		if raw, ok := obj.(*model.Object); ok {
			raw.Modified = history.Nodes[0].CommittedDate
		}
	}
}
