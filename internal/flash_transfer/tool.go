package flash_transfer

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
)

var ErrIDNotFound = errors.New("fileset_id not found in html")

// GetFileSetID 很不优雅的实现 但是目前没别的好思路了
func GetFileSetID(htmlContent string) string {
	reScript := regexp.MustCompile(`<script[^>]*id="__NUXT_DATA__"[^>]*>([^<]+)</script>`)
	matches := reScript.FindStringSubmatch(htmlContent)

	if len(matches) < 2 {
		return ""
	}

	jsonContent := matches[1]

	var nuxtData []interface{}
	if err := json.Unmarshal([]byte(jsonContent), &nuxtData); err != nil {
		return ""
	}

	for _, item := range nuxtData {
		if obj, ok := item.(map[string]interface{}); ok {
			if val, exists := obj["fileset_id"]; exists {
				return resolveNuxtValue(nuxtData, val)
			}
			if val, exists := obj["filesetId"]; exists {
				return resolveNuxtValue(nuxtData, val)
			}
		}
	}

	return ""
}

func GetFileSetName(htmlContent string) string {
	re := regexp.MustCompile(`(?i)<title>(.*?)</title>`)
	match := re.FindStringSubmatch(htmlContent)

	if len(match) > 1 {
		rawTitle := match[1]

		parts := strings.Split(rawTitle, "｜") // QQ flash transfer's shit '｜' ？？？！！！！

		if len(parts) > 0 {
			result := strings.TrimSpace(parts[0])
			return result
		}
	} else {
		return ""
	}
	return ""
}

func resolveNuxtValue(rootData []interface{}, val interface{}) string {
	switch v := val.(type) {
	case string:
		return v
	case float64:
		idx := int(v)
		if idx >= 0 && idx < len(rootData) {
			if realValue, ok := rootData[idx].(string); ok {
				return realValue
			}
		}
	}
	return ""
}

func (c *FlashClient) GetFileSetIdByCode(shareCode string) (error, string, string) {
	validateCode := regexp.MustCompile(`^[a-zA-Z0-9]+$`)
	if !validateCode.MatchString(shareCode) {
		return errors.New("invalid share code format"), "", ""
	}

	url := c.BaseUrl + "/q/" + shareCode

	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return err, "", ""
	}

	// 加UA 防风控
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/120.0.0.0 Safari/537.36")

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return err, "", ""
	}

	defer func(Body io.ReadCloser) {
		_ = Body.Close()
	}(resp.Body)

	htmlBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return err, "", ""
	}
	htmlContent := string(htmlBytes)

	filesetID := GetFileSetID(htmlContent)
	filesetName := GetFileSetName(htmlContent)

	if filesetID != "" {
		return nil, filesetID, filesetName
	}

	return ErrIDNotFound, "", filesetName
}
