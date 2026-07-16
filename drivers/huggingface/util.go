package huggingface

import (
	"fmt"
	"strings"

	"github.com/go-resty/resty/v2"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

const hfAPIBase = "https://huggingface.co"

func apiRepoType(t string) string {
	switch t {
	case "model":
		return "models"
	case "dataset":
		return "datasets"
	case "space":
		return "spaces"
	default:
		return "models"
	}
}

func (d *HuggingFace) apiURL(path string) string {
	return fmt.Sprintf("%s/api/%s/%s%s", hfAPIBase, apiRepoType(d.RepoType), d.RepoID, path)
}

func (d *HuggingFace) resolveURL(ref, path string) string {
	base := hfAPIBase
	if d.RepoType != "model" {
		base = hfAPIBase + "/" + apiRepoType(d.RepoType)
	}
	return fmt.Sprintf("%s/%s/resolve/%s/%s", base, d.RepoID, ref, strings.TrimPrefix(path, "/"))
}

func (d *HuggingFace) request() *resty.Request {
	req := d.client.R()
	if d.ApiToken != "" {
		req.SetHeader("Authorization", "Bearer "+d.ApiToken)
	}
	return req
}

func relativePath(path string) string {
	return strings.TrimPrefix(path, "/")
}

func toHFError(res *resty.Response) error {
	var errResp ErrorResponse
	if err := utils.Json.Unmarshal(res.Body(), &errResp); err != nil {
		return fmt.Errorf("%s", res.Status())
	}
	if errResp.Error != "" {
		return fmt.Errorf("%s: %s", res.Status(), errResp.Error)
	}
	return fmt.Errorf("%s", res.Status())
}
