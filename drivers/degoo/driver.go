package template

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Degoo struct {
	model.Storage
	Addition
	Token   string
	devices map[string]string
	client  *http.Client // The HTTP client provided by the framework.
}

// Config returns the driver's configuration settings.
func (d *Degoo) Config() driver.Config {
	return config
}

// GetAddition returns the driver's custom configuration.
func (d *Degoo) GetAddition() driver.Additional {
	return &d.Addition
}

// Init handles the driver's initialization, including authentication.
// It directly translates the Python login logic to Go.
func (d *Degoo) Init(ctx context.Context) error {
	loginURL := "https://rest-api.degoo.com/login"
	accessTokenURL := "https://rest-api.degoo.com/access-token/v2"

	// Get a client from the framework for better integration.
	d.client = base.HttpClient()

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
	req.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
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
		tokenReqHTTP.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")

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

	return d.getDevices(ctx)
}

// Drop handles cleanup on driver removal.
func (d *Degoo) Drop(ctx context.Context) error {
	return nil
}

// List fetches and returns the list of files and folders in a directory.
// It uses the Degoo API's getFileChildren5 to retrieve all items, handling pagination.
func (d *Degoo) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	items, err := d.getAllFileChildren5(ctx, dir.ID)
	if err != nil {
		return nil, err
	}

	var objs []model.Obj
	for _, item := range items {
		obj := d.toModelObj(item)
		objs = append(objs, obj)
	}

	return objs, nil
}

// Link returns a direct download link for a file.
func (d *Degoo) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	item, err := d.getOverlay4(ctx, file.ID)
	if err != nil {
		return nil, err
	}
	
	link := &model.Link{URL: item.URL}
	if item.OptimizedURL != "" {
		link.URL = item.OptimizedURL
	}
	
	return link, nil
}

// MakeDir creates a new folder.
// This is done by calling the setUploadFile3 API with a special checksum and size.
func (d *Degoo) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	const query = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) { setUploadFile3(Token: $Token, FileInfos: $FileInfos) }`
	
	variables := map[string]interface{}{
		"Token": d.Token,
		"FileInfos": []map[string]interface{}{
			{
				"Checksum": folderChecksum,
				"Name":     dirName,
				"CreationTime": time.Now().UnixNano() / int64(time.Millisecond),
				"ParentID": parentDir.ID,
				"Size":     0,
			},
		},
	}
	
	_, err := d.apiCall(ctx, "SetUploadFile3", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	// A new folder is created. We need to fetch its ID.
	items, err := d.getAllFileChildren5(ctx, parentDir.ID)
	if err != nil {
		return model.Obj{}, err
	}

	for _, item := range items {
		if item.Name == dirName && item.Category == 2 {
			return d.toModelObj(item), nil
		}
	}
	return model.Obj{}, fmt.Errorf("failed to locate newly created directory: %s", dirName)
}

// Move moves a file or folder to a new parent directory.
func (d *Degoo) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	const query = `mutation SetMoveFile($Token: String!, $Copy: Boolean, $NewParentID: String!, $FileIDs: [String]!) { setMoveFile(Token: $Token, Copy: $Copy, NewParentID: $NewParentID, FileIDs: $FileIDs) }`

	variables := map[string]interface{}{
		"Token":       d.Token,
		"Copy":        false,
		"NewParentID": dstDir.ID,
		"FileIDs":     []string{srcObj.ID},
	}
	
	_, err := d.apiCall(ctx, "SetMoveFile", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	return srcObj, nil
}

// Rename renames a file or folder.
func (d *Degoo) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	const query = `mutation SetRenameFile($Token: String!, $FileRenames: [FileRenameInfo]!) { setRenameFile(Token: $Token, FileRenames: $FileRenames) }`

	variables := map[string]interface{}{
		"Token": d.Token,
		"FileRenames": []DegooFileRenameInfo{
			{
				ID:      srcObj.ID,
				NewName: newName,
			},
		},
	}
	
	_, err := d.apiCall(ctx, "SetRenameFile", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	srcObj.Name = newName
	return srcObj, nil
}

// Copy copies a file or folder. The Degoo API doesn't appear to have a direct
// copy function, so we return NotImplement.
func (d *Degoo) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	return model.Obj{}, errs.NotImplement
}

// Remove deletes a file or folder by moving it to the trash.
func (d *Degoo) Remove(ctx context.Context, obj model.Obj) error {
	const query = `mutation SetDeleteFile5($Token: String!, $IsInRecycleBin: Boolean!, $IDs: [IDType]!) { setDeleteFile5(Token: $Token, IsInRecycleBin: $IsInRecycleBin, IDs: $IDs) }`

	variables := map[string]interface{}{
		"Token":          d.Token,
		"IsInRecycleBin": false,
		"IDs":            []map[string]string{{"FileID": obj.ID}},
	}
	
	_, err := d.apiCall(ctx, "SetDeleteFile5", query, variables)
	return err
}

// Put uploads a file to the Degoo storage.
func (d *Degoo) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// The Python script outlines a multi-step upload process that we need to follow:
	// 1. Get upload authorization via getBucketWriteAuth4.
	// 2. Perform the actual file upload (not implemented here).
	// 3. Register the uploaded file's metadata with setUploadFile3.
	return nil, errs.NotImplement
}

// Ensure Degoo struct implements the driver.Driver interface.
var _ driver.Driver = (*Degoo)(nil)
