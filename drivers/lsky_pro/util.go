package lsky_pro

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-resty/resty/v2"
)

const apiPrefix = "/api/v1"

func itoa(i int) string { return strconv.Itoa(i) }

// request calls the Lsky Pro API, checks the response envelope and
// unmarshals its data field into out (when out is not nil).
func (d *LskyPro) request(ctx context.Context, method, path string, callback func(*resty.Request), out any) error {
	req := d.client.R().SetContext(ctx)
	if callback != nil {
		callback(req)
	}
	res, err := req.Execute(method, apiPrefix+path)
	if err != nil {
		return err
	}
	var env Resp[json.RawMessage]
	if err := json.Unmarshal(res.Body(), &env); err != nil {
		return fmt.Errorf("lsky pro: unexpected response (HTTP %d)", res.StatusCode())
	}
	if res.StatusCode() == http.StatusUnauthorized {
		return fmt.Errorf("lsky pro: unauthorized, please check the token")
	}
	if !env.Status {
		if env.Message == "" {
			return fmt.Errorf("lsky pro: request failed (HTTP %d)", res.StatusCode())
		}
		return fmt.Errorf("lsky pro: %s", env.Message)
	}
	if out == nil || len(env.Data) == 0 {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// listAll walks every page of a paginated endpoint.
func listAll[T any](ctx context.Context, d *LskyPro, path string, params map[string]string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		var p Page[T]
		err := d.request(ctx, http.MethodGet, path, func(req *resty.Request) {
			req.SetQueryParams(params).SetQueryParam("page", strconv.Itoa(page))
		}, &p)
		if err != nil {
			return nil, err
		}
		all = append(all, p.Data...)
		if page >= p.LastPage || len(p.Data) == 0 {
			return all, nil
		}
	}
}
