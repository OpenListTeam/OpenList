package doubao_new

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/adler32"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/cookie"
	"github.com/go-resty/resty/v2"
)

const (
	BaseURL         = "https://my.feishu.cn"
	DownloadBaseURL = "https://internal-api-drive-stream.feishu.cn"
)

var defaultObjTypes = []string{"124", "0", "12", "30", "123", "22"}

func (d *DoubaoNew) request(ctx context.Context, path string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	req := base.RestyClient.R()
	req.SetContext(ctx)
	req.SetHeader("accept", "*/*")
	req.SetHeader("origin", "https://www.doubao.com")
	req.SetHeader("referer", "https://www.doubao.com/")
	if auth := d.resolveAuthorization(); auth != "" {
		req.SetHeader("authorization", auth)
	}
	if dpop := d.resolveDpop(); dpop != "" {
		req.SetHeader("dpop", dpop)
	}

	if callback != nil {
		callback(req)
	}

	res, err := req.Execute(method, BaseURL+path)
	if err != nil {
		return nil, err
	}
	if res != nil {
		if v := res.Header().Get("X-Tt-Logid"); v != "" {
			d.TtLogid = v
		} else if v := res.Header().Get("x-tt-logid"); v != "" {
			d.TtLogid = v
		}
	}

	body := res.Body()
	var common BaseResp
	if err = json.Unmarshal(body, &common); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return body, fmt.Errorf("%s", msg)
	}
	if common.Code != 0 {
		errMsg := common.Msg
		if errMsg == "" {
			errMsg = common.Message
		}
		return body, fmt.Errorf("[doubao_new] API error (code: %d): %s", common.Code, errMsg)
	}
	if resp != nil {
		if err = json.Unmarshal(body, resp); err != nil {
			return body, err
		}
	}

	return body, nil
}

func adler32String(data []byte) string {
	sum := adler32.Checksum(data)
	return strconv.FormatUint(uint64(sum), 10)
}

func buildCommaHeader(items []string) string {
	return strings.Join(items, ",")
}

func joinIntComma(items []int) string {
	if len(items) == 0 {
		return ""
	}
	var sb strings.Builder
	for i, v := range items {
		if i > 0 {
			sb.WriteByte(',')
		}
		sb.WriteString(strconv.Itoa(v))
	}
	return sb.String()
}

func previewList(items []string, n int) string {
	if n <= 0 || len(items) == 0 {
		return ""
	}
	if len(items) < n {
		n = len(items)
	}
	return strings.Join(items[:n], ",")
}

func parseSize(size string) int64 {
	if size == "" {
		return 0
	}
	val, err := strconv.ParseInt(size, 10, 64)
	if err != nil {
		return 0
	}
	return val
}

func (d *DoubaoNew) resolveAuthorization() string {
	auth := strings.TrimSpace(d.Authorization)
	if auth == "" && d.Cookie != "" {
		if token := cookie.GetStr(d.Cookie, "LARK_SUITE_ACCESS_TOKEN"); token != "" {
			auth = token
		}
	}
	if auth == "" {
		return ""
	}
	if !strings.HasPrefix(auth, "DPoP ") && !strings.HasPrefix(auth, "dpop ") {
		auth = "DPoP " + auth
	}
	return auth
}

func (d *DoubaoNew) resolveDpop() string {
	dpop := strings.TrimSpace(d.Dpop)
	if dpop == "" && d.Cookie != "" {
		dpop = cookie.GetStr(d.Cookie, "LARK_SUITE_DPOP")
	}
	return dpop
}

func (d *DoubaoNew) listChildren(ctx context.Context, parentToken string, lastLabel string) (ListData, error) {
	var resp ListResp
	_, err := d.request(ctx, "/space/api/explorer/doubao/children/list/", http.MethodGet, func(req *resty.Request) {
		values := url.Values{}
		for _, t := range defaultObjTypes {
			values.Add("obj_type", t)
		}
		values.Set("length", "50")
		values.Set("rank", "0")
		values.Set("asc", "0")
		values.Set("min_length", "40")
		values.Set("thumbnail_width", "1028")
		values.Set("thumbnail_height", "1028")
		values.Set("thumbnail_policy", "4")
		if parentToken != "" {
			values.Set("token", parentToken)
		}
		if lastLabel != "" {
			values.Set("last_label", lastLabel)
		}
		req.SetQueryParamsFromValues(values)
	}, &resp)
	if err != nil {
		return ListData{}, err
	}

	return resp.Data, nil
}

func (d *DoubaoNew) listAllChildren(ctx context.Context, parentToken string) ([]Node, error) {
	nodes := make([]Node, 0, 50)
	lastLabel := ""
	for page := 0; page < 100; page++ {
		data, err := d.listChildren(ctx, parentToken, lastLabel)
		if err != nil {
			return nil, err
		}

		if len(data.NodeList) > 0 {
			for _, token := range data.NodeList {
				node, ok := data.Entities.Nodes[token]
				if !ok {
					continue
				}
				nodes = append(nodes, node)
			}
		} else {
			for _, node := range data.Entities.Nodes {
				nodes = append(nodes, node)
			}
		}

		if !data.HasMore || data.LastLabel == "" || data.LastLabel == lastLabel {
			break
		}
		lastLabel = data.LastLabel
	}

	if len(nodes) == 0 {
		return nil, nil
	}
	return nodes, nil
}

func (d *DoubaoNew) getFileInfo(ctx context.Context, fileToken string) (FileInfo, error) {
	var resp FileInfoResp
	_, err := d.request(ctx, "/space/api/box/file/info/", http.MethodPost, func(req *resty.Request) {
		req.SetHeader("Content-Type", "application/json")
		req.SetBody(base.Json{
			"caller":        "explorer",
			"file_token":    fileToken,
			"mount_point":   "explorer",
			"option_params": []string{"preview_meta", "check_cipher"},
		})
	}, &resp)
	if err != nil {
		return FileInfo{}, err
	}

	return resp.Data, nil
}

func (d *DoubaoNew) previewLink(ctx context.Context, obj *Object, args model.LinkArgs) (*model.Link, error) {
	auth := d.resolveAuthorization()
	dpop := d.resolveDpop()
	if auth == "" || dpop == "" {
		return nil, errors.New("missing authorization or dpop")
	}
	if obj.ObjToken == "" {
		return nil, errors.New("missing obj_token")
	}
	info, err := d.getFileInfo(ctx, obj.ObjToken)
	if err != nil {
		return nil, err
	}

	entry, ok := info.PreviewMeta.Data["22"]
	if !ok || entry.Status != 0 {
		return nil, errors.New("preview not available")
	}

	subID := ""
	pageIndex := 0

	if subID == "" {
		imgExt := ".webp"
		pageNums := 0
		if entry.Extra != "" {
			var extra PreviewImageExtra
			if err := json.Unmarshal([]byte(entry.Extra), &extra); err == nil {
				if extra.ImgExt != "" {
					imgExt = extra.ImgExt
				}
				pageNums = extra.PageNums
			}
		}
		if pageNums > 0 && pageIndex >= pageNums {
			pageIndex = pageNums - 1
		}
		subID = fmt.Sprintf("img_%d%s", pageIndex, imgExt)
	}

	query := url.Values{}
	query.Set("preview_type", "22")
	query.Set("sub_id", subID)
	if info.Version != "" {
		query.Set("version", info.Version)
	}
	previewURL := fmt.Sprintf("%s/space/api/box/stream/download/preview_sub/%s?%s", BaseURL, obj.ObjToken, query.Encode())

	headers := http.Header{
		"Referer":       []string{"https://www.doubao.com/"},
		"User-Agent":    []string{base.UserAgent},
		"Authorization": []string{auth},
		"Dpop":          []string{dpop},
	}

	return &model.Link{
		URL:    previewURL,
		Header: headers,
	}, nil
}

func (d *DoubaoNew) createFolder(ctx context.Context, parentToken, name string) (Node, error) {
	data := url.Values{}
	data.Set("name", name)
	data.Set("source", "0")
	if parentToken != "" {
		data.Set("parent_token", parentToken)
	}

	doRequest := func(csrfToken string) (*resty.Response, []byte, error) {
		req := base.RestyClient.R()
		req.SetContext(ctx)
		req.SetHeader("accept", "*/*")
		req.SetHeader("origin", "https://www.doubao.com")
		req.SetHeader("referer", "https://www.doubao.com/")
		if auth := d.resolveAuthorization(); auth != "" {
			req.SetHeader("authorization", auth)
		}
		if dpop := d.resolveDpop(); dpop != "" {
			req.SetHeader("dpop", dpop)
		}
		if csrfToken != "" {
			req.SetHeader("x-csrftoken", csrfToken)
		}
		req.SetHeader("Content-Type", "application/x-www-form-urlencoded")
		req.SetBody(data.Encode())
		res, err := req.Execute(http.MethodPost, BaseURL+"/space/api/explorer/v2/create/folder/")
		if err != nil {
			return nil, nil, err
		}
		return res, res.Body(), nil
	}

	res, body, err := doRequestWithCsrf(doRequest)
	if err != nil {
		return Node{}, err
	}
	if err := decodeBaseResp(body, res); err != nil {
		return Node{}, err
	}

	var resp CreateFolderResp
	if err := json.Unmarshal(body, &resp); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return Node{}, fmt.Errorf("%s", msg)
	}

	var node Node
	if len(resp.Data.NodeList) > 0 {
		if n, ok := resp.Data.Entities.Nodes[resp.Data.NodeList[0]]; ok {
			node = n
		}
	}
	if node.Token == "" {
		for _, n := range resp.Data.Entities.Nodes {
			node = n
			break
		}
	}
	if node.Token == "" && node.ObjToken == "" && node.NodeToken == "" {
		return Node{}, fmt.Errorf("[doubao_new] create folder failed: empty response")
	}
	if node.NodeToken == "" {
		if node.Token != "" {
			node.NodeToken = node.Token
		} else if node.ObjToken != "" {
			node.NodeToken = node.ObjToken
		}
	}
	if node.ObjToken == "" && node.Token != "" {
		node.ObjToken = node.Token
	}
	return node, nil
}

func (d *DoubaoNew) renameFolder(ctx context.Context, token, name string) error {
	if token == "" {
		return fmt.Errorf("[doubao_new] rename folder missing token")
	}
	data := url.Values{}
	data.Set("token", token)
	data.Set("name", name)

	doRequest := func(csrfToken string) (*resty.Response, []byte, error) {
		req := base.RestyClient.R()
		req.SetContext(ctx)
		req.SetHeader("accept", "*/*")
		req.SetHeader("origin", "https://www.doubao.com")
		req.SetHeader("referer", "https://www.doubao.com/")
		if auth := d.resolveAuthorization(); auth != "" {
			req.SetHeader("authorization", auth)
		}
		if dpop := d.resolveDpop(); dpop != "" {
			req.SetHeader("dpop", dpop)
		}
		if csrfToken != "" {
			req.SetHeader("x-csrftoken", csrfToken)
		}
		req.SetHeader("Content-Type", "application/x-www-form-urlencoded")
		req.SetBody(data.Encode())
		res, err := req.Execute(http.MethodPost, BaseURL+"/space/api/explorer/v2/rename/")
		if err != nil {
			return nil, nil, err
		}
		return res, res.Body(), nil
	}

	res, body, err := doRequestWithCsrf(doRequest)
	if err != nil {
		return err
	}
	return decodeBaseResp(body, res)
}

func isCsrfTokenError(body []byte, res *resty.Response) bool {
	if len(body) == 0 {
		return false
	}
	if strings.Contains(strings.ToLower(string(body)), "csrf token error") {
		return true
	}
	if res != nil && res.StatusCode() == http.StatusForbidden {
		return true
	}
	return false
}

func doRequestWithCsrf(doRequest func(csrfToken string) (*resty.Response, []byte, error)) (*resty.Response, []byte, error) {
	res, body, err := doRequest("")
	if err != nil {
		return res, body, err
	}
	if isCsrfTokenError(body, res) {
		csrfToken := extractCsrfTokenFromResponse(res)
		if csrfToken != "" {
			return doRequest(csrfToken)
		}
	}
	return res, body, err
}

func extractCsrfTokenFromResponse(res *resty.Response) string {
	if res == nil || res.Request == nil {
		return ""
	}
	if res.Request.RawRequest != nil {
		if csrf := cookie.GetStr(res.Request.RawRequest.Header.Get("Cookie"), "_csrf_token"); csrf != "" {
			return csrf
		}
	}
	if csrf := cookie.GetStr(res.Request.Header.Get("Cookie"), "_csrf_token"); csrf != "" {
		return csrf
	}
	for _, c := range res.Cookies() {
		if c.Name == "_csrf_token" {
			return c.Value
		}
	}
	return ""
}

func decodeBaseResp(body []byte, res *resty.Response) error {
	var common BaseResp
	if err := json.Unmarshal(body, &common); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return fmt.Errorf("%s", msg)
	}
	if common.Code != 0 {
		errMsg := common.Msg
		if errMsg == "" {
			errMsg = common.Message
		}
		return fmt.Errorf("[doubao_new] API error (code: %d): %s", common.Code, errMsg)
	}
	return nil
}

func (d *DoubaoNew) renameFile(ctx context.Context, fileToken, name string) error {
	if fileToken == "" {
		return fmt.Errorf("[doubao_new] rename file missing file token")
	}
	_, err := d.request(ctx, "/space/api/box/file/update_info/", http.MethodPost, func(req *resty.Request) {
		req.SetHeader("Content-Type", "application/json")
		req.SetBody(base.Json{
			"file_token": fileToken,
			"name":       name,
		})
	}, nil)
	return err
}

func (d *DoubaoNew) moveObj(ctx context.Context, srcToken, destToken string) error {
	if srcToken == "" {
		return fmt.Errorf("[doubao_new] move missing src token")
	}
	data := url.Values{}
	data.Set("src_token", srcToken)
	if destToken != "" {
		data.Set("dest_token", destToken)
	}
	doRequest := func(csrfToken string) (*resty.Response, []byte, error) {
		req := base.RestyClient.R()
		req.SetContext(ctx)
		req.SetHeader("accept", "*/*")
		req.SetHeader("origin", "https://www.doubao.com")
		req.SetHeader("referer", "https://www.doubao.com/")
		if auth := d.resolveAuthorization(); auth != "" {
			req.SetHeader("authorization", auth)
		}
		if dpop := d.resolveDpop(); dpop != "" {
			req.SetHeader("dpop", dpop)
		}
		if csrfToken != "" {
			req.SetHeader("x-csrftoken", csrfToken)
		}
		req.SetHeader("Content-Type", "application/x-www-form-urlencoded")
		req.SetBody(data.Encode())
		res, err := req.Execute(http.MethodPost, BaseURL+"/space/api/explorer/v2/move/")
		if err != nil {
			return nil, nil, err
		}
		return res, res.Body(), nil
	}

	res, body, err := doRequestWithCsrf(doRequest)
	if err != nil {
		return err
	}
	return decodeBaseResp(body, res)
}

func (d *DoubaoNew) removeObj(ctx context.Context, tokens []string) error {
	if len(tokens) == 0 {
		return fmt.Errorf("[doubao_new] remove missing tokens")
	}
	doRequest := func(csrfToken string) (*resty.Response, []byte, error) {
		req := base.RestyClient.R()
		req.SetContext(ctx)
		req.SetHeader("accept", "application/json, text/plain, */*")
		req.SetHeader("origin", "https://www.doubao.com")
		req.SetHeader("referer", "https://www.doubao.com/")
		if auth := d.resolveAuthorization(); auth != "" {
			req.SetHeader("authorization", auth)
		}
		if dpop := d.resolveDpop(); dpop != "" {
			req.SetHeader("dpop", dpop)
		}
		if csrfToken != "" {
			req.SetHeader("x-csrftoken", csrfToken)
		}
		req.SetHeader("Content-Type", "application/json")
		req.SetBody(base.Json{
			"tokens": tokens,
			"apply":  1,
		})
		res, err := req.Execute(http.MethodPost, BaseURL+"/space/api/explorer/v3/remove/")
		if err != nil {
			return nil, nil, err
		}
		return res, res.Body(), nil
	}

	res, body, err := doRequestWithCsrf(doRequest)
	if err != nil {
		return err
	}
	var resp RemoveResp
	if err := json.Unmarshal(body, &resp); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return fmt.Errorf("%s", msg)
	}
	if resp.Code != 0 {
		errMsg := resp.Msg
		if errMsg == "" {
			errMsg = resp.Message
		}
		return fmt.Errorf("[doubao_new] API error (code: %d): %s", resp.Code, errMsg)
	}
	if resp.Data.TaskID == "" {
		return nil
	}
	return d.waitTask(ctx, resp.Data.TaskID)
}

func (d *DoubaoNew) getUserStorage(ctx context.Context) (UserStorageData, error) {
	req := base.RestyClient.R()
	req.SetContext(ctx)
	req.SetHeader("accept", "*/*")
	req.SetHeader("origin", "https://www.doubao.com")
	req.SetHeader("referer", "https://www.doubao.com/")
	req.SetHeader("agw-js-conv", "str")
	req.SetHeader("content-type", "application/json")
	if auth := d.resolveAuthorization(); auth != "" {
		req.SetHeader("authorization", auth)
	}
	if dpop := d.resolveDpop(); dpop != "" {
		req.SetHeader("dpop", dpop)
	}
	if d.Cookie != "" {
		req.SetHeader("cookie", d.Cookie)
	}
	req.SetBody(base.Json{})

	res, err := req.Execute(http.MethodPost, "https://www.doubao.com/alice/aispace/facade/get_user_storage")
	if err != nil {
		return UserStorageData{}, err
	}

	body := res.Body()
	var resp UserStorageResp
	if err := json.Unmarshal(body, &resp); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return UserStorageData{}, fmt.Errorf("%s", msg)
	}
	if resp.Code != 0 {
		errMsg := resp.Msg
		if errMsg == "" {
			errMsg = resp.Message
		}
		return UserStorageData{}, fmt.Errorf("[doubao_new] API error (code: %d): %s", resp.Code, errMsg)
	}

	return resp.Data, nil
}

func (d *DoubaoNew) waitTask(ctx context.Context, taskID string) error {
	const (
		taskPollInterval    = time.Second
		taskPollMaxAttempts = 120
	)
	var lastErr error
	for attempt := 0; attempt < taskPollMaxAttempts; attempt++ {
		if attempt > 0 {
			if err := waitWithContext(ctx, taskPollInterval); err != nil {
				return err
			}
		}
		status, err := d.getTaskStatus(ctx, taskID)
		if err != nil {
			lastErr = err
			continue
		}
		if status.IsFail {
			return fmt.Errorf("[doubao_new] remove task failed: %s", taskID)
		}
		if status.IsFinish {
			return nil
		}
	}
	if lastErr != nil {
		return lastErr
	}
	return fmt.Errorf("[doubao_new] remove task timed out: %s", taskID)
}

func (d *DoubaoNew) getTaskStatus(ctx context.Context, taskID string) (TaskStatusData, error) {
	if taskID == "" {
		return TaskStatusData{}, fmt.Errorf("[doubao_new] task status missing task_id")
	}
	req := base.RestyClient.R()
	req.SetContext(ctx)
	req.SetHeader("accept", "application/json, text/plain, */*")
	req.SetHeader("origin", "https://www.doubao.com")
	req.SetHeader("referer", "https://www.doubao.com/")
	if auth := d.resolveAuthorization(); auth != "" {
		req.SetHeader("authorization", auth)
	}
	if dpop := d.resolveDpop(); dpop != "" {
		req.SetHeader("dpop", dpop)
	}
	req.SetQueryParam("task_id", taskID)
	res, err := req.Execute(http.MethodGet, BaseURL+"/space/api/explorer/v2/task/")
	if err != nil {
		return TaskStatusData{}, err
	}
	body := res.Body()
	var resp TaskStatusResp
	if err := json.Unmarshal(body, &resp); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return TaskStatusData{}, fmt.Errorf("%s", msg)
	}
	if resp.Code != 0 {
		errMsg := resp.Msg
		if errMsg == "" {
			errMsg = resp.Message
		}
		return TaskStatusData{}, fmt.Errorf("[doubao_new] API error (code: %d): %s", resp.Code, errMsg)
	}
	return resp.Data, nil
}

func waitWithContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
