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
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

const (
	// Degoo's GraphQL API endpoint.
	apiURL = "https://production-appsync.degoo.com/graphql"
	// The fixed API key found in the Python script.
	apiKey = "da2-vs6twz5vnjdavpqndtbzg3prra"
	// User-Agent string to impersonate a legitimate client.
	userAgent = "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50"
	// Checksum for a new folder, as determined in the Python script.
	folderChecksum = "CgAQAg"
)

// apiCall is a generic helper to perform GraphQL API requests to Degoo.
func (d *Degoo) apiCall(ctx context.Context, operationName, query string, variables map[string]interface{}) (json.RawMessage, error) {
	reqBody := map[string]interface{}{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to serialize request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", apiURL, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("User-Agent", userAgent)
	
	if d.Token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.Token))
	}

	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("API request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API response error: %s", resp.Status)
	}

	var degooResp DegooGraphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&degooResp); err != nil {
		return nil, fmt.Errorf("failed to decode API response: %w", err)
	}

	if len(degooResp.Errors) > 0 {
		errMsg := degooResp.Errors[0]["message"]
		return nil, fmt.Errorf("degoo API returned an error: %v", errMsg)
	}

	return degooResp.Data, nil
}

// humanReadableTimes converts Degoo's raw timestamps into standard Go time.Time objects.
func humanReadableTimes(creation, modification, upload string) (cTime, mTime, uTime time.Time) {
	cTime, _ = time.Parse(time.RFC3339, creation)
	if modification != "" {
		modMillis, _ := strconv.ParseInt(modification, 10, 64)
		mTime = time.Unix(0, modMillis*int64(time.Millisecond))
	}
	if upload != "" {
		upMillis, _ := strconv.ParseInt(upload, 10, 64)
		uTime = time.Unix(0, upMillis*int64(time.Millisecond))
	}
	return cTime, mTime, uTime
}
	// 将字符串时间戳转换为可读格式 "2006-01-02 15:04:05"
	format := "2006-01-02 15:04:05"
	cTime = parseTimestampToReadable(creation, format)
	mTime = parseTimestampToReadable(modification, format)
	uTime = parseTimestampToReadable(upload, format)
	return cTime, mTime, uTime
}

// parseTimestampToReadable 辅助函数，将字符串时间戳转换为可读格式
func parseTimestampToReadable(ts string, format string) string {
	if ts == "" {
		return ""
	}
	// 支持秒和毫秒时间戳
	var tInt int64
	_, err := fmt.Sscanf(ts, "%d", &tInt)
	if err != nil {
		return ""
	}
	// 判断是否为毫秒级时间戳
	if len(ts) > 10 {
		tInt = tInt / 1000
	}
	t := time.Unix(tInt, 0)
	return t.Format(format)
}
// checkSum calculates the specific SHA1-based checksum required by the Degoo upload API.
func checkSum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	seed := []byte{13, 7, 2, 2, 15, 40, 75, 117, 13, 10, 19, 16, 29, 23, 3, 36}
	hasher := sha1.New()
	hasher.Write(seed)

	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	cs := hasher.Sum(nil)

	csBytes := []byte{10, byte(len(cs))}
	csBytes = append(csBytes, cs...)
	csBytes = append(csBytes, 16, 0)

	return base64.StdEncoding.EncodeToString(csBytes), nil
}

// toModelObj converts a DegooFileItem API object into an OpenList model.Obj.
func (d *Degoo) toModelObj(item DegooFileItem) model.Obj {
	isFolder := item.Category == 2 || item.Category == 1 || item.Category == 10
	_, modTime, _ := humanReadableTimes(item.CreationTime, item.LastModificationTime, item.LastUploadTime)
	return model.Obj{
		ID:        item.ID,
		ParentID:  item.ParentID,
		Name:      item.Name,
		Size:      item.Size,
		IsFolder:  isFolder,
		UpdatedAt: modTime,
	}
}

// getDevices gets and caches the list of top-level devices and folders.
func (d *Degoo) getDevices(ctx context.Context) error {
	const query = `query GetFileChildren5($Token: String! $ParentID: String $AllParentIDs: [String] $Limit: Int! $Order: Int! $NextToken: String ) { getFileChildren5(Token: $Token ParentID: $ParentID AllParentIDs: $AllParentIDs Limit: $Limit Order: $Order NextToken: $NextToken) { Items { ID Name Category } NextToken } }`
	variables := map[string]interface{}{
		"Token":    d.Token,
		"ParentID": "0",
		"Limit":    1000,
		"Order":    3,
	}
	data, err := d.apiCall(ctx, "GetFileChildren5", query, variables)
	if err != nil {
		return err
	}
	var resp DegooGetChildren5Data
	if err := json.Unmarshal(data, &resp); err != nil {
		return fmt.Errorf("failed to parse device list: %w", err)
	}
	d.devices = make(map[string]string)
	for _, item := range resp.GetFileChildren5.Items {
		if item.Category == 1 {
			d.devices[item.ID] = item.Name
		}
	}
	return nil
}

// getAllFileChildren5 handles pagination to fetch all children of a directory.
func (d *Degoo) getAllFileChildren5(ctx context.Context, parentID string) ([]DegooFileItem, error) {
	const query = `query GetFileChildren5($Token: String! $ParentID: String $AllParentIDs: [String] $Limit: Int! $Order: Int! $NextToken: String ) { getFileChildren5(Token: $Token ParentID: $ParentID AllParentIDs: $AllParentIDs Limit: $Limit Order: $Order NextToken: $NextToken) { Items { ID ParentID Name Category Size CreationTime LastModificationTime LastUploadTime FilePath IsInRecycleBin DeviceID MetadataID } NextToken } }`
	var allItems []DegooFileItem
	nextToken := ""
	for {
		variables := map[string]interface{}{
			"Token": d.Token,
			"ParentID": parentID,
			"Limit": 1000,
			"Order": 3,
		}
		if nextToken != "" {
			variables["NextToken"] = nextToken
		}
		data, err := d.apiCall(ctx, "GetFileChildren5", query, variables)
		if err != nil {
			return nil, err
		}
		var resp DegooGetChildren5Data
		if err := json.Unmarshal(data, &resp); err != nil {
			return nil, err
		}
		allItems = append(allItems, resp.GetFileChildren5.Items...)
		if resp.GetFileChildren5.NextToken == "" {
			break
		}
		nextToken = resp.GetFileChildren5.NextToken
	}
	return allItems, nil
}

// getOverlay4 fetches metadata for a single item by its ID.
func (d *Degoo) getOverlay4(ctx context.Context, id string) (DegooFileItem, error) {
	const query = `query GetOverlay4($Token: String!, $ID: IDType!) { getOverlay4(Token: $Token, ID: $ID) { ID ParentID Name Category Size CreationTime LastModificationTime LastUploadTime URL OptimizedURL FilePath IsInRecycleBin DeviceID MetadataID } }`
	variables := map[string]interface{}{
		"Token": d.Token,
		"ID": map[string]string{
			"FileID": id,
		},
	}
	data, err := d.apiCall(ctx, "GetOverlay4", query, variables)
	if err != nil {
		return DegooFileItem{}, err
	}
	var resp DegooGetOverlay4Data
	if err := json.Unmarshal(data, &resp); err != nil {
		return DegooFileItem{}, fmt.Errorf("failed to parse item metadata: %w", err)
	}
	return resp.GetOverlay4, nil
}
