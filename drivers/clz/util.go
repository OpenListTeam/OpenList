package clz

import (
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/go-resty/resty/v2"
)

func (d *CLZ) request(reqType string, data map[string]interface{}, result interface{}) error {
	client := resty.New()
	data["timestamp"] = time.Now().Unix()
	data["token"] = d.Token
	data["os_type"] = "PC"
	data["app_name"] = "cilizhai"
	data["app_version"] = "1.2"
	data["channel"] = "webstore"
	data["request_type"] = reqType

	resp, err := client.R().
		SetHeader("Content-Type", "application/x-www-form-urlencoded").
		SetHeader("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/116.0.0.0 Safari/537.36").
		SetBody(data).
		SetResult(result).
		Post("https://web.miao2021.cn/web_api/")
	
	if err != nil {
		return err
	}
	if !resp.IsSuccess() {
		return fmt.Errorf("http error: %d", resp.StatusCode())
	}
	return nil
}

// DecryptReader 实现了字节解密：偶数+1，奇数-1 
type DecryptReader struct {
	rc io.ReadCloser
}

func (r *DecryptReader) Read(p []byte) (n int, err error) {
	n, err = r.rc.Read(p)
	for i := 0; i < n; i++ {
		if p[i]%2 == 0 {
			p[i] = p[i] + 1
		} else {
			p[i] = p[i] - 1
		}
	}
	return n, err
}

func (r *DecryptReader) Close() error {
	return r.rc.Close()
}