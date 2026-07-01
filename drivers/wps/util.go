package wps

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

const ENDPOINT_BUSINESS = "https://365.kdocs.cn"
const ENDPOINT_PERSONAL = "https://drive.wps.cn"

func (d *Wps) isPersonal() bool {
	// prefer d.login if available, as it may be set by islogin API
	// which can determine account type more reliably
	// one login session only support one type
	// can not use personal and company account at the same time
	if d.login != nil {
		return !d.login.IsCompanyAccount
	}
	return strings.TrimSpace(d.Mode) == "Personal"
}

func (d *Wps) driveHost() string {
	if d.isPersonal() {
		return ENDPOINT_PERSONAL
	}
	return ENDPOINT_BUSINESS
}

func (d *Wps) drivePrefix() string {
	if d.isPersonal() {
		return ""
	}
	return "/3rd/drive"
}

func (d *Wps) driveURL(path string) string {
	return d.driveHost() + d.drivePrefix() + path
}

func (d *Wps) origin() string {
	return d.driveHost()
}

func (d *Wps) getUA() string {
	if d.CustomUA != "" {
		return d.CustomUA
	}
	return base.UserAgent
}

func (d *Wps) request(ctx context.Context) *resty.Request {
	return d.client.R().
		SetHeader("Cookie", d.Cookie).
		SetHeader("Accept", "application/json").
		SetHeader("User-Agent", d.getUA()).
		SetContext(ctx)
}

func (d *Wps) jsonRequest(ctx context.Context) *resty.Request {
	return d.request(ctx).
		SetHeader("Content-Type", "application/json").
		SetHeader("Origin", d.origin())
}

func statusOK(code int, expect []int) bool {
	if len(expect) == 0 {
		return code >= 200 && code < 300
	}
	for _, v := range expect {
		if v == code {
			return true
		}
	}
	return false
}

func respArg(arg string, resp *http.Response, body []byte) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ""
	}
	l := strings.ToLower(arg)
	if strings.HasPrefix(l, "header.") {
		h := strings.TrimSpace(arg[len("header."):])
		if h == "" {
			return ""
		}
		return strings.TrimSpace(resp.Header.Get(h))
	}
	if strings.HasPrefix(l, "body.") {
		k := strings.TrimSpace(arg[len("body."):])
		if k == "" {
			return ""
		}
		var m map[string]interface{}
		if err := json.Unmarshal(body, &m); err != nil {
			return ""
		}
		if v, ok := m[k]; ok {
			if s, ok := v.(string); ok {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractXMLTag(v, tag string) string {
	s := strings.TrimSpace(v)
	if s == "" {
		return ""
	}
	lt := strings.ToLower(tag)
	open := "<" + lt + ">"
	clos := "</" + lt + ">"
	ls := strings.ToLower(s)
	i := strings.Index(ls, open)
	if i < 0 {
		return ""
	}
	i += len(open)
	j := strings.Index(ls[i:], clos)
	if j < 0 {
		return ""
	}
	r := strings.TrimSpace(s[i : i+j])
	r = strings.ReplaceAll(r, "&quot;", "")
	return strings.Trim(r, `"'`)
}

func checkAPI(resp *resty.Response, result apiResult) error {
	if result.Result != "" && result.Result != "ok" {
		if result.Msg == "" {
			result.Msg = "unknown error"
		}
		return fmt.Errorf("%s: %s", result.Result, result.Msg)
	}
	if resp != nil && resp.IsError() {
		if result.Msg != "" {
			return fmt.Errorf("%s", result.Msg)
		}
		return fmt.Errorf("http error: %d", resp.StatusCode())
	}
	return nil
}

func (d *Wps) getGroups(ctx context.Context) ([]Group, error) {
	// different APIs
	switch d.Mode {
	case "Personal":
		var resp personalGroupsResp
		r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(d.driveURL("/api/v3/groups"))
		if err != nil {
			return nil, err
		}
		if err := checkAPI(r, resp.apiResult); err != nil {
			return nil, err
		}
		res := make([]Group, 0, len(resp.Groups))
		for _, g := range resp.Groups {
			res = append(res, Group{GroupID: g.ID, Name: g.Name})
		}
		return res, nil
	case "Business":
		var resp groupsResp
		url := fmt.Sprintf("%s/3rd/plus/groups/v1/companies/%d/users/self/groups/private", ENDPOINT_BUSINESS, d.login.CompanyID)
		r, err := d.request(ctx).SetResult(&resp).SetError(&resp).Get(url)
		if err != nil {
			return nil, err
		}
		if r != nil && r.IsError() {
			return nil, fmt.Errorf("http error: %d", r.StatusCode())
		}
		return resp.Groups, nil
	}
	return nil, fmt.Errorf("unsupported mode: %s", d.Mode)
}

func (d *Wps) getFiles(ctx context.Context, groupID, parentID int64) ([]FileInfo, error) {
	var resp filesResp
	var files []FileInfo
	next_offset := 0
	for range 50 {
		url := fmt.Sprintf("%s/api/v5/groups/%d/files", d.driveHost()+d.drivePrefix(), groupID)
		r, err := d.request(ctx).
			SetQueryParam("parentid", strconv.FormatInt(parentID, 10)).
			SetQueryParam("offset", fmt.Sprint(next_offset)).
			SetResult(&resp).
			SetError(&resp).
			Get(url)
		if err != nil {
			return nil, err
		}
		if r != nil && r.IsError() {
			return nil, fmt.Errorf("http error: %d", r.StatusCode())
		}
		files = append(files, resp.Files...)
		if resp.NextOffset == -1 {
			break
		}
		next_offset = resp.NextOffset
	}
	return files, nil
}

func parseTime(v int64) time.Time {
	if v <= 0 {
		return time.Time{}
	}
	return time.Unix(v, 0)
}

func joinPath(basePath, name string) string {
	if basePath == "" || basePath == "/" {
		return "/" + name
	}
	return strings.TrimRight(basePath, "/") + "/" + name
}

func (d *Wps) doJSON(ctx context.Context, method, url string, body interface{}) error {
	var result apiResult
	req := d.jsonRequest(ctx).SetBody(body).SetResult(&result).SetError(&result)
	var (
		resp *resty.Response
		err  error
	)
	switch method {
	case http.MethodPost:
		resp, err = req.Post(url)
	case http.MethodPut:
		resp, err = req.Put(url)
	default:
		return errs.NotSupport
	}
	if err != nil {
		return err
	}
	return checkAPI(resp, result)
}

func unwrapWpsObj(obj model.Obj) (*Obj, error) {
	for obj != nil {
		if node, ok := obj.(*Obj); ok {
			return node, nil
		}
		unwrap, ok := obj.(model.ObjUnwrap)
		if !ok {
			break
		}
		obj = unwrap.Unwrap()
	}
	return nil, fmt.Errorf("invalid object type")
}
