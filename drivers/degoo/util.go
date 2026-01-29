package degoo

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
)

// Thanks to https://github.com/bernd-wechner/Degoo for API research.

const (
	// API endpoints
	loginURL       = "https://rest-api.degoo.com/login"
	accessTokenURL = "https://rest-api.degoo.com/access-token/v2"
	apiURL         = "https://production-appsync.degoo.com/graphql"

	// API configuration
	apiKey         = "da2-vs6twz5vnjdavpqndtbzg3prra"
	folderChecksum = "CgAQAg"

	// Token management
	tokenRefreshThreshold = 5 * time.Minute

	// Rate limiting
	minRequestInterval = 1 * time.Second

	// Error messages
	errRateLimited  = "rate limited (429), please try again later"
	errUnauthorized = "unauthorized access"
)

var (
	// Global rate limiting - protects against concurrent API calls
	lastRequestTime time.Time
	requestMutex    sync.Mutex
)

// JWT payload structure for token expiration checking
type JWTPayload struct {
	UserID string `json:"userID"`
	Exp    int64  `json:"exp"`
	Iat    int64  `json:"iat"`
}

// Rate limiting helper functions

// applyRateLimit ensures minimum interval between API requests
func applyRateLimit() {
	requestMutex.Lock()
	defer requestMutex.Unlock()

	if !lastRequestTime.IsZero() {
		if elapsed := time.Since(lastRequestTime); elapsed < minRequestInterval {
			time.Sleep(minRequestInterval - elapsed)
		}
	}
	lastRequestTime = time.Now()
}

// HTTP request helper functions

// createJSONRequest creates a new HTTP request with JSON body
func createJSONRequest(ctx context.Context, method, url string, body interface{}) (*http.Request, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewBuffer(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", base.UserAgent)
	return req, nil
}

// checkHTTPResponse checks for common HTTP error conditions
func checkHTTPResponse(resp *http.Response, operation string) error {
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("%s %s", operation, errRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s failed: %s", operation, resp.Status)
	}
	return nil
}

// isTokenExpired checks if the JWT token is expired or will expire soon
func (d *Degoo) isTokenExpired() bool {
	if d.AccessToken == "" {
		return true
	}

	payload, err := extractJWTPayload(d.AccessToken)
	if err != nil {
		return true // Invalid token format
	}

	// Check if token expires within the threshold
	expireTime := time.Unix(payload.Exp, 0)
	return time.Now().Add(tokenRefreshThreshold).After(expireTime)
}

// extractJWTPayload extracts and parses JWT payload
func extractJWTPayload(token string) (*JWTPayload, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, fmt.Errorf("invalid JWT format")
	}

	// Decode the payload (second part)
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("failed to decode JWT payload: %w", err)
	}

	var jwtPayload JWTPayload
	if err := json.Unmarshal(payload, &jwtPayload); err != nil {
		return nil, fmt.Errorf("failed to parse JWT payload: %w", err)
	}

	return &jwtPayload, nil
}

// refreshToken attempts to refresh the access token using the refresh token
func (d *Degoo) refreshToken(ctx context.Context) error {
	if d.RefreshToken == "" {
		return fmt.Errorf("no refresh token available")
	}

	// Create request
	tokenReq := DegooAccessTokenRequest{RefreshToken: d.RefreshToken}
	req, err := createJSONRequest(ctx, "POST", accessTokenURL, tokenReq)
	if err != nil {
		return fmt.Errorf("failed to create refresh token request: %w", err)
	}

	// Execute request
	resp, err := d.client.Do(req)
	if err != nil {
		return fmt.Errorf("refresh token request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check response
	if err := checkHTTPResponse(resp, "refresh token"); err != nil {
		return err
	}

	var accessTokenResp DegooAccessTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&accessTokenResp); err != nil {
		return fmt.Errorf("failed to parse access token response: %w", err)
	}

	if accessTokenResp.AccessToken == "" {
		return fmt.Errorf("empty access token received")
	}

	d.AccessToken = accessTokenResp.AccessToken
	// Save the updated token to storage
	op.MustSaveDriverStorage(d)

	return nil
}

// ensureValidToken ensures we have a valid, non-expired token
func (d *Degoo) ensureValidToken(ctx context.Context) error {
	// Check if token is expired or will expire soon
	if d.isTokenExpired() {
		// Try to refresh token first if we have a refresh token
		if d.RefreshToken != "" {
			if refreshErr := d.refreshToken(ctx); refreshErr == nil {
				return nil // Successfully refreshed
			} else {
				// If refresh failed, fall back to full login
				fmt.Printf("Token refresh failed, falling back to full login: %v\n", refreshErr)
			}
		}

		// Perform full login
		if d.Username != "" && d.Password != "" {
			return d.login(ctx)
		}
	}

	return nil
}

// login performs the login process and retrieves the access token.
func (d *Degoo) login(ctx context.Context) error {
	if d.Username == "" || d.Password == "" {
		return fmt.Errorf("username or password not provided")
	}

	creds := DegooLoginRequest{
		GenerateToken: true,
		Username:      d.Username,
		Password:      d.Password,
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

	// Handle rate limiting (429 Too Many Requests)
	if resp.StatusCode == http.StatusTooManyRequests {
		return fmt.Errorf("login rate limited (429), please try again later")
	}

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("login failed: %s", resp.Status)
	}

	var loginResp DegooLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return fmt.Errorf("failed to parse login response: %w", err)
	}

	if loginResp.RefreshToken != "" {
		tokenReq := DegooAccessTokenRequest{RefreshToken: loginResp.RefreshToken}
		jsonTokenReq, err := json.Marshal(tokenReq)
		if err != nil {
			return fmt.Errorf("failed to serialize access token request: %w", err)
		}

		tokenReqHTTP, err := http.NewRequestWithContext(ctx, "POST", accessTokenURL, bytes.NewBuffer(jsonTokenReq))
		if err != nil {
			return fmt.Errorf("failed to create access token request: %w", err)
		}

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
		d.AccessToken = accessTokenResp.AccessToken
		d.RefreshToken = loginResp.RefreshToken // Save refresh token
	} else if loginResp.Token != "" {
		d.AccessToken = loginResp.Token
		d.RefreshToken = "" // Direct token, no refresh token available
	} else {
		return fmt.Errorf("login failed, no valid token returned")
	}

	// Save the updated tokens to storage
	op.MustSaveDriverStorage(d)

	return nil
}

// apiCall performs a Degoo GraphQL API request.
func (d *Degoo) apiCall(ctx context.Context, operationName, query string, variables map[string]interface{}) (json.RawMessage, error) {
	// Apply rate limiting
	applyRateLimit()

	// Ensure we have a valid token before making the API call
	if err := d.ensureValidToken(ctx); err != nil {
		return nil, fmt.Errorf("failed to ensure valid token: %w", err)
	}

	// Update the Token in variables if it exists (after potential refresh)
	d.updateTokenInVariables(variables)

	return d.executeGraphQLRequest(ctx, operationName, query, variables)
}

// updateTokenInVariables updates the Token field in GraphQL variables
func (d *Degoo) updateTokenInVariables(variables map[string]interface{}) {
	if variables != nil {
		if _, hasToken := variables["Token"]; hasToken {
			variables["Token"] = d.AccessToken
		}
	}
}

// executeGraphQLRequest executes a GraphQL request with retry logic
func (d *Degoo) executeGraphQLRequest(ctx context.Context, operationName, query string, variables map[string]interface{}) (json.RawMessage, error) {
	reqBody := map[string]interface{}{
		"operationName": operationName,
		"query":         query,
		"variables":     variables,
	}

	// Create and configure request
	req, err := createJSONRequest(ctx, "POST", apiURL, reqBody)
	if err != nil {
		return nil, err
	}

	// Set Degoo-specific headers
	req.Header.Set("x-api-key", apiKey)
	if d.AccessToken != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.AccessToken))
	}

	// Execute request
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GraphQL API request failed: %w", err)
	}
	defer resp.Body.Close()

	// Check for HTTP errors
	if err := checkHTTPResponse(resp, "GraphQL API"); err != nil {
		return nil, err
	}

	// Parse GraphQL response
	var degooResp DegooGraphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&degooResp); err != nil {
		return nil, fmt.Errorf("failed to decode GraphQL response: %w", err)
	}

	// Handle GraphQL errors
	if len(degooResp.Errors) > 0 {
		return d.handleGraphQLError(ctx, degooResp.Errors[0], operationName, query, variables)
	}

	return degooResp.Data, nil
}

// handleGraphQLError handles GraphQL-level errors with retry logic
func (d *Degoo) handleGraphQLError(ctx context.Context, gqlError DegooErrors, operationName, query string, variables map[string]interface{}) (json.RawMessage, error) {
	if gqlError.ErrorType == "Unauthorized" {
		// Re-login and retry
		if err := d.login(ctx); err != nil {
			return nil, fmt.Errorf("%s, login failed: %w", errUnauthorized, err)
		}

		// Update token in variables and retry
		d.updateTokenInVariables(variables)
		return d.apiCall(ctx, operationName, query, variables)
	}

	return nil, fmt.Errorf("GraphQL API error: %s", gqlError.Message)
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
		"Token":    d.AccessToken,
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
			"Token":    d.AccessToken,
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
		"Token": d.AccessToken,
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

func (d *Degoo) getUserInfo(ctx context.Context) (DegooGetUserInfo3Data, error) {
	const query = "query GetUserInfo3($Token: String!) { getUserInfo3(Token: $Token) { UsedQuota TotalQuota } }"
	variables := map[string]interface{}{
		"Token": d.AccessToken,
	}
	data, err := d.apiCall(ctx, "GetUserInfo3", query, variables)
	var resp DegooGetUserInfo3Data
	if err != nil {
		return resp, err
	}
	if err = json.Unmarshal(data, &resp); err != nil {
		return resp, fmt.Errorf("failed to parse user info: %w", err)
	}
	return resp, nil
}
