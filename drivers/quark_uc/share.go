package quark

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
	"github.com/pkg/errors"
)

var (
	quarkShareURLRe = regexp.MustCompile(`(?i)https?://(?:(?:www|pan)\.)?quark\.cn/s/([A-Za-z0-9]+)`)
	ucShareURLRe    = regexp.MustCompile(`(?i)https?://(?:drive\.)?uc\.cn/s/([A-Za-z0-9]+)`)
	sharePassRe     = regexp.MustCompile(`(?i)(?:pwd|passcode|pass|提取码|密码)\s*[：:=\s]\s*([A-Za-z0-9]{4,8})`)
)

func ParseShareLink(raw string) (pwdID, passcode string, err error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", "", errors.New("empty share link")
	}

	if m := quarkShareURLRe.FindStringSubmatch(raw); len(m) > 1 {
		pwdID = m[1]
	} else if m := ucShareURLRe.FindStringSubmatch(raw); len(m) > 1 {
		pwdID = m[1]
	}
	if pwdID == "" {
		return "", "", errors.New("unsupported share link")
	}

	if u, parseErr := url.Parse(firstURL(raw)); parseErr == nil {
		passcode = u.Query().Get("pwd")
	}
	if passcode == "" {
		if m := sharePassRe.FindStringSubmatch(raw); len(m) > 1 {
			passcode = m[1]
		}
	}
	return pwdID, passcode, nil
}

func firstURL(raw string) string {
	for _, field := range strings.Fields(raw) {
		if strings.Contains(strings.ToLower(field), "://") {
			return field
		}
	}
	return raw
}

func (d *QuarkOrUC) SaveFromShare(ctx context.Context, dstDir model.Obj, args model.SaveFromShareArgs) error {
	pwdID, passcode, err := ParseShareLink(args.URL)
	if err != nil {
		return err
	}
	if args.Password != "" {
		passcode = args.Password
	}

	stoken, err := d.getShareToken(ctx, pwdID, passcode)
	if err != nil {
		return err
	}
	files, err := d.getShareRootFiles(ctx, pwdID, stoken)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("share is empty")
	}

	fidList := make([]string, 0, len(files))
	tokenList := make([]string, 0, len(files))
	for _, f := range files {
		if f.Fid == "" {
			continue
		}
		fidList = append(fidList, f.Fid)
		tokenList = append(tokenList, f.ShareFidToken)
	}
	if len(fidList) == 0 {
		return errors.New("share is empty")
	}

	taskID, tqGap, err := d.saveShare(ctx, pwdID, stoken, fidList, tokenList, dstDir.GetID())
	if err != nil {
		return err
	}
	return d.waitShareTask(ctx, taskID, tqGap)
}

func (d *QuarkOrUC) getShareToken(ctx context.Context, pwdID, passcode string) (string, error) {
	var resp ShareTokenResp
	_, err := d.request("/share/sharepage/token", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(base.Json{
			"pwd_id":                            pwdID,
			"passcode":                          passcode,
			"support_visit_limit_private_share": true,
		})
	}, &resp)
	if err != nil {
		return "", err
	}
	if resp.Data.Stoken == "" {
		return "", errors.New("failed to get share token")
	}
	return resp.Data.Stoken, nil
}

func (d *QuarkOrUC) getShareRootFiles(ctx context.Context, pwdID, stoken string) ([]ShareFile, error) {
	files := make([]ShareFile, 0)
	page := 1
	size := 100
	for {
		query := map[string]string{
			"pwd_id":       pwdID,
			"stoken":       stoken,
			"pdir_fid":     "0",
			"force":        "0",
			"_page":        fmt.Sprintf("%d", page),
			"_size":        fmt.Sprintf("%d", size),
			"_fetch_total": "1",
		}
		var resp ShareDetailResp
		_, err := d.request("/share/sharepage/detail", http.MethodGet, func(req *resty.Request) {
			req.SetContext(ctx).SetQueryParams(query)
		}, &resp)
		if err != nil {
			return nil, err
		}
		files = append(files, resp.Data.List...)
		if page*size >= resp.Metadata.Total || len(resp.Data.List) == 0 {
			break
		}
		page++
	}
	return files, nil
}

func (d *QuarkOrUC) saveShare(ctx context.Context, pwdID, stoken string, fidList, fidTokenList []string, toPdirFid string) (string, time.Duration, error) {
	if toPdirFid == "" {
		toPdirFid = "0"
	}
	data := base.Json{
		"fid_list":       fidList,
		"fid_token_list": fidTokenList,
		"to_pdir_fid":    toPdirFid,
		"pwd_id":         pwdID,
		"stoken":         stoken,
		"pdir_fid":       "0",
		"scene":          "link",
	}
	var resp ShareSaveResp
	_, err := d.request("/share/sharepage/save", http.MethodPost, func(req *resty.Request) {
		req.SetContext(ctx).SetBody(data)
	}, &resp)
	if err != nil {
		return "", 0, err
	}
	if resp.Data.TaskId == "" {
		return "", 0, errors.New("empty save task id")
	}
	return resp.Data.TaskId, tqGapDuration(resp.Metadata.TqGap), nil
}

func (d *QuarkOrUC) waitShareTask(ctx context.Context, taskID string, gap time.Duration) error {
	if gap <= 0 {
		gap = 500 * time.Millisecond
	}
	const maxWait = 2 * time.Minute
	deadline := time.Now().Add(maxWait)
	retryIndex := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if time.Now().After(deadline) {
			return errors.New("save from share timed out")
		}
		var resp ShareTaskResp
		_, err := d.request("/task", http.MethodGet, func(req *resty.Request) {
			req.SetContext(ctx).SetQueryParams(map[string]string{
				"task_id":     taskID,
				"retry_index": fmt.Sprintf("%d", retryIndex),
			})
		}, &resp)
		if err != nil {
			return err
		}
		switch resp.Data.Status {
		case 2:
			return nil
		case 3:
			msg := resp.Message
			if msg == "" || msg == "ok" {
				msg = "save from share failed"
			}
			return errors.New(msg)
		}
		if g := tqGapDuration(resp.Metadata.TqGap); g > 0 {
			gap = g
		}
		retryIndex++
		timer := time.NewTimer(gap)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func tqGapDuration(ms int) time.Duration {
	if ms <= 0 {
		return 0
	}
	return time.Duration(ms) * time.Millisecond
}
