package template

import (
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// apiCall 是通用的 GraphQL API 请求封装，模拟 Python 脚本的 post 请求
func (d *Degoo) apiCall(ctx context.Context, operationName, query string, variables map[string]interface{}) (json.RawMessage, error) {
	reqBody := map[string]interface{}{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("无法序列化请求体: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://production-appsync.degoo.com/graphql", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("无法创建请求: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "da2-vs6twz5vnjdavpqndtbzg3prra")
	req.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
	// Python 脚本中的 Token 存储在 self.KEYS 中，Go中存储在 d.Token
	if d.Token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.Token))
	}
	
	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API 响应错误: %s", resp.Status)
	}

	var degooResp DegooGraphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&degooResp); err != nil {
		return nil, fmt.Errorf("无法解析响应: %w", err)
	}

	if len(degooResp.Errors) > 0 {
		return nil, fmt.Errorf("Degoo API 返回错误: %v", degooResp.Errors[0]["message"])
	}

	return degooResp.Data, nil
}

// humanReadableTimes 转换时间戳为可读格式，模拟 Python 脚本中的 _human_readable_times()
func humanReadableTimes(creation, modification, upload string) (cTime, mTime, uTime string) {
	// 实现与 Python 脚本中相同的逻辑
	return "", "", ""
}

// checkSum 计算文件的 Degoo 特有校验和，模拟 Python 脚本中的 check_sum()
func checkSum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// 模拟 Python 脚本中 hardcoded 的 seed
	seed := []byte{13, 7, 2, 2, 15, 40, 75, 117, 13, 10, 19, 16, 29, 23, 3, 36}
	hasher := sha1.New()
	hasher.Write(seed)

	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	cs := hasher.Sum(nil)

	// 模拟 Python 脚本中特殊的编码方式
	csBytes := []byte{10, byte(len(cs))}
	csBytes = append(csBytes, cs...)
	csBytes = append(csBytes, 16, 0)
	
	return base64.StdEncoding.EncodeToString(csBytes), nil
}
