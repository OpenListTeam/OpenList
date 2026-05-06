package cloudflare_imgbed

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

const (
	listApi                 = "/api/manage/list"
	deleteApi               = "/api/manage/delete"
	uploadApi               = "/upload"
	hfGetUrlApi             = "/upload/huggingface/getUploadUrl"
	hfCommitApi             = "/upload/huggingface/commitUpload"
	hfDirectThreshold int64 = 20 * 1024 * 1024
	fileSampleSize          = 512 // HF 申请上传地址时需提供文件前 512 字节的 Sample
)

// doRequest 通用请求封装，包含重试和 API 错误解析
func (d *CFImgBed) doRequest(method, urlPath string, callback func(*resty.Request), resp interface{}) ([]byte, error) {
	maxRetries := 3
	for i := 0; i < maxRetries; i++ {
		req := d.client.R()
		if callback != nil {
			callback(req)
		}
		if resp != nil {
			req.SetResult(resp)
		}

		res, err := req.Execute(method, urlPath)
		if err != nil {
			log.WithError(err).Warnf("request %s %s failed, attempt %d/%d", method, urlPath, i+1, maxRetries)
			if i < maxRetries-1 {
				time.Sleep(time.Duration(i+1) * time.Second)
				continue
			}
			return nil, err
		}

		body := res.Body()
		var apiErr apiError
		if err := json.Unmarshal(body, &apiErr); err == nil {
			if apiErr.Error != "" || apiErr.Message != "" {
				msg := apiErr.Error
				if msg == "" {
					msg = apiErr.Message
				}
				return nil, fmt.Errorf("API error: %s", msg)
			}
		}

		if res.StatusCode() == 429 {
			time.Sleep(time.Duration(i+1) * 2 * time.Second)
			continue
		}

		if res.IsError() {
			return nil, fmt.Errorf("HTTP %d", res.StatusCode())
		}
		return body, nil
	}
	return nil, fmt.Errorf("max retries exceeded")
}
