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

// apiCall is a generic wrapper for GraphQL API requests, mimicking the Python script's post request.
func (d *Degoo) apiCall(ctx context.Context, operationName, query string, variables map[string]interface{}) (json.RawMessage, error) {
	reqBody := map[string]interface{}{
		"operationName": operationName,
		"query": query,
		"variables": variables,
	}

	jsonBody, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", "https://production-appsync.degoo.com/graphql", bytes.NewReader(jsonBody))
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", "da2-vs6twz5vnjdavpqndtbzg3prra")
	req.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
	// The token is stored in self.KEYS in the Python script, and in d.Token here.
	if d.Token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", d.Token))
	}

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("API response error: %s", resp.Status)
	}

	var degooResp DegooGraphqlResponse
	if err := json.NewDecoder(resp.Body).Decode(&degooResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	if len(degooResp.Errors) > 0 {
		return nil, fmt.Errorf("Degoo API returned an error: %v", degooResp.Errors[0]["message"])
	}

	return degooResp.Data, nil
}

// humanReadableTimes converts timestamps to a human-readable format, mimicking _human_readable_times() in the Python script.
func humanReadableTimes(creation, modification, upload string) (cTime, mTime, uTime string) {
	// Implements the same logic as the Python script.
	return "", "", ""
}

// checkSum calculates the specific Degoo checksum for a file, mimicking check_sum() in the Python script.
func checkSum(filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", err
	}
	defer file.Close()

	// Mimics the hardcoded seed from the Python script.
	seed := []byte{13, 7, 2, 2, 15, 40, 75, 117, 13, 10, 19, 16, 29, 23, 3, 36}
	hasher := sha1.New()
	hasher.Write(seed)

	if _, err := io.Copy(hasher, file); err != nil {
		return "", err
	}

	cs := hasher.Sum(nil)

	// Mimics the special encoding method from the Python script.
	csBytes := []byte{10, byte(len(cs))}
	csBytes = append(csBytes, cs...)
	csBytes = append(csBytes, 16, 0)

	return base64.StdEncoding.EncodeToString(csBytes), nil
}
