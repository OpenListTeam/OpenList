package degoo

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// Thanks to https://github.com/bernd-wechner/Degoo for API research.

const (
	loginURL       = "https://rest-api.degoo.com/login"
	accessTokenURL = "https://rest-api.degoo.com/access-token/v2"

	// Degoo GraphQL API endpoint.
	apiURL = "https://production-appsync.degoo.com/graphql"
	// Fixed API key.
	apiKey = "da2-vs6twz5vnjdavpqndtbzg3prra"
	// Checksum for new folder.
	folderChecksum = "CgAQAg"
)

// login performs the login process and retrieves the access token.
func (d *Degoo) login(ctx context.Context) error {
	creds := DegooLoginRequest{
		GenerateToken: true,
		Username:      d.Addition.Username,
		Password:      d.Addition.Password,
	}

	jsonCreds, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to serialize login credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", loginURL, bytes.NewBuffer(jsonCreds))
	if err != nil {
		return fmt.Errorf("failed to create login request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", base.UserAgent)
	req.Header.Set("Origin", "https://app.degoo.com")

	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("login request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed: %s", resp.Status)
	}

	var loginResp DegooLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return fmt.Errorf("failed to parse login response: %w", err)
	}

	if loginResp.RefreshToken != "" {
		tokenReq := DegooAccessTokenRequest{RefreshToken: loginResp.RefreshToken}
		jsonTokenReq, _ := json.Marshal(tokenReq)

		tokenReqHTTP, _ := http.NewRequestWithContext(ctx, "POST", accessTokenURL, bytes.NewBuffer(jsonTokenReq))
		tokenReqHTTP.Header.Set("Content-Type", "application/json")
		tokenReqHTTP.Header.Set("User-Agent", base.UserAgent)

		tokenResp, err := d.client.Do(tokenReqHTTP)
		if err != nil {
			return fmt.Errorf("failed to get access token: %w", err)
		}
		defer tokenResp.Body.Close()

		var accessTokenResp DegooAccessTokenResponse
		if err := json.NewDecoder(tokenResp.Body).Decode(&accessTokenResp); err != nil {
			return fmt.Errorf("failed to parse access token response: %w", err)
		}
		d.Token = accessTokenResp.AccessToken
	} else if loginResp.Token != "" {
		d.Token = loginResp.Token
	} else {
		return fmt.Errorf("login failed, no valid token returned")
	}
	return nil
}

// apiCall performs a Degoo GraphQL API request.
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
	req.Header.Set("User-Agent", base.UserAgent)

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
		if degooResp.Errors[0].ErrorType == "Unauthorized" {
			err = d.login(ctx)
			if err != nil {
				return nil, fmt.Errorf("unauthorized access, login failed: %w", err)
			}
			// Retry the API call after re-login
			return d.apiCall(ctx, operationName, query, variables)
		}
		return nil, fmt.Errorf("degoo API returned an error: %v", degooResp.Errors[0].Message)
	}

	return degooResp.Data, nil
}

// humanReadableTimes converts Degoo timestamps to Go time.Time.
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

// getDevices fetches and caches top-level devices and folders.
func (d *Degoo) getDevices(ctx context.Context) error {
	const query = `query GetFileChildren5($Token: String! $ParentID: String $AllParentIDs: [String] $Limit: Int! $Order: Int! $NextToken: String ) { getFileChildren5(Token: $Token ParentID: $ParentID AllParentIDs: $AllParentIDs Limit: $Limit Order: $Order NextToken: $NextToken) { Items { ParentID } NextToken } }`
	variables := map[string]interface{}{
		"Token":    d.Token,
		"ParentID": "0",
		"Limit":    10,
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
	if d.RootFolderID == "0" {
		if len(resp.GetFileChildren5.Items) > 0 {
			d.RootFolderID = resp.GetFileChildren5.Items[0].ParentID
		}
		op.MustSaveDriverStorage(d)
	}
	return nil
}

// getAllFileChildren5 fetches all children of a directory with pagination.
func (d *Degoo) getAllFileChildren5(ctx context.Context, parentID string) ([]DegooFileItem, error) {
	const query = `query GetFileChildren5($Token: String! $ParentID: String $AllParentIDs: [String] $Limit: Int! $Order: Int! $NextToken: String ) { getFileChildren5(Token: $Token ParentID: $ParentID AllParentIDs: $AllParentIDs Limit: $Limit Order: $Order NextToken: $NextToken) { Items { ID ParentID Name Category Size CreationTime LastModificationTime LastUploadTime FilePath IsInRecycleBin DeviceID MetadataID } NextToken } }`
	var allItems []DegooFileItem
	nextToken := ""
	for {
		variables := map[string]interface{}{
			"Token":    d.Token,
			"ParentID": parentID,
			"Limit":    1000,
			"Order":    3,
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

// getOverlay4 fetches metadata for a single item by ID.
func (d *Degoo) getOverlay4(ctx context.Context, id string) (DegooFileItem, error) {
	const query = `query GetOverlay4($Token: String!, $ID: IDType!) { getOverlay4(Token: $Token, ID: $ID) { ID ParentID Name Category Size CreationTime LastModificationTime LastUploadTime URL FilePath IsInRecycleBin DeviceID MetadataID } }`
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
