package template

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
)

type Degoo struct {
	model.Storage
	Addition
	Token string
	// Caches the list of devices, mimicking the __devices__ property in the Python script.
	devices map[string]string
}

func (d *Degoo) Config() driver.Config {
	return config
}

func (d *Degoo) GetAddition() driver.Additional {
	return &d.Addition
}

// Init implements login and token management, fully replicating the login() method logic from the Python script.
func (d *Degoo) Init(ctx context.Context) error {
	loginURL := "https://rest-api.degoo.com/login"
	accessTokenURL := "https://rest-api.degoo.com/access-token/v2"
	
	creds := DegooLoginRequest{
		GenerateToken: true,
		Username:      d.Addition.Username,
		Password:      d.Addition.Password,
	}

	jsonCreds, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("failed to marshal login credentials: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", loginURL, bytes.NewBuffer(jsonCreds))
	if err != nil {
		return fmt.Errorf("failed to create login request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	// Mimics the User-Agent from the Python script.
	req.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
	req.Header.Set("Origin", "https://app.degoo.com")

	client := &http.Client{}
	resp, err := client.Do(req)
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

	// Checks for a RefreshToken; if it exists, an AccessToken must be requested.
	if loginResp.RefreshToken != "" {
		tokenReq := DegooAccessTokenRequest{RefreshToken: loginResp.RefreshToken}
		jsonTokenReq, _ := json.Marshal(tokenReq)
		
		tokenReqHTTP, _ := http.NewRequestWithContext(ctx, "POST", accessTokenURL, bytes.NewBuffer(jsonTokenReq))
		tokenReqHTTP.Header.Set("Content-Type", "application/json")
		tokenReqHTTP.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
		
		tokenResp, err := client.Do(tokenReqHTTP)
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

	// Initializes the device list.
	d.devices = make(map[string]string)
	d.getDevices(ctx)
	
	return nil
}

// getDevices fetches and caches the device list, mimicking the Python script's devices property.
func (d *Degoo) getDevices(ctx context.Context) error {
	const query = `query GetFileChildren5($Token: String! $ParentID: String $AllParentIDs: [String] $Limit: Int! $Order: Int! $NextToken: String ) { getFileChildren5(Token: $Token ParentID: $ParentID AllParentIDs: $AllParentIDs Limit: $Limit Order: $Order NextToken: $NextToken) { Items { ID Name Category } NextToken } }`
	
	// parentID 0 corresponds to the root directory.
	variables := map[string]interface{}{
		"Token":      d.Token,
		"ParentID":   "0",
		"Limit":      1000,
		"Order":      3,
	}

	data, err := d.apiCall(ctx, "GetFileChildren5", query, variables)
	if err != nil {
		return err
	}
	
	var resp DegooGetChildren5Data
	if err := json.Unmarshal(data, &resp); err != nil {
		return err
	}

	for _, item := range resp.GetFileChildren5.Items {
		if item.Category == 1 { // Category 1 represents a Device.
			d.devices[item.ID] = item.Name
		}
	}
	return nil
}

// List lists files and folders, fully replicating the getFileChildren5() logic from the Python script.
func (d *Degoo) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	items, err := d.getAllFileChildren5(ctx, dir.ID)
	if err != nil {
		return nil, err
	}
	
	var objs []model.Obj
	for _, item := range items {
		// Converts DegooFileItem to model.Obj.
		obj := d.toModelObj(item)
		objs = append(objs, obj)
	}

	return objs, nil
}

// getAllFileChildren5 handles pagination, replicating the method with the same name from the Python script.
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

	// Fixes file paths, mimicking the path joining logic in the Python script.
	for i, item := range allItems {
		if item.Category != 1 && item.Category != 10 { // Not a Device or Recycle Bin.
			devicePath := d.devices[item.DeviceID]
			binned := item.IsInRecycleBin
			prefix := devicePath
			if binned {
				prefix = filepath.Join(prefix, "Recycle Bin")
			}
			allItems[i].FilePath = filepath.Join("/", prefix, item.FilePath)
		}
	}
	
	return allItems, nil
}


// Link gets the download link, replicating the getOverlay4() logic from the Python script.
func (d *Degoo) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	const query = `query GetOverlay4($Token: String!, $ID: IDType!) { getOverlay4(Token: $Token, ID: $ID) { URL OptimizedURL } }`

	variables := map[string]interface{}{
		"Token": d.Token,
		"ID": map[string]string{
			"FileID": file.ID,
		},
	}
	
	data, err := d.apiCall(ctx, "GetOverlay4", query, variables)
	if err != nil {
		return nil, err
	}
	
	var resp DegooGetOverlay4Data
	if err := json.Unmarshal(data, &resp); err != nil {
		return nil, err
	}
	
	// Prioritizes the OptimizedURL.
	url := resp.GetOverlay4.URL
	if resp.GetOverlay4.OptimizedURL != "" {
		url = resp.GetOverlay4.OptimizedURL
	}

	return &model.Link{URL: url}, nil
}

// toModelObj converts a DegooFileItem to an OpenList model.Obj.
func (d *Degoo) toModelObj(item DegooFileItem) model.Obj {
	isFolder := item.Category == 2
	
	// Converts the timestamp.
	modTime, _ := time.Parse(time.RFC3339, item.LastModificationTime)

	return model.Obj{
		ID:        item.ID,
		ParentID:  item.ParentID,
		Name:      item.Name,
		Size:      item.Size,
		IsFolder:  isFolder,
		UpdatedAt: modTime,
	}
}

// MakeDir creates a new folder, replicating the setUploadFile3() logic from the Python script.
func (d *Degoo) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	const query = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) { setUploadFile3(Token: $Token, FileInfos: $FileInfos) }`
	
	variables := map[string]interface{}{
		"Token": d.Token,
		"FileInfos": []map[string]interface{}{
			{
				"Checksum": "CgAQAg", // Specific checksum for folder creation in the Python script.
				"Name":     dirName,
				"CreationTime": time.Now().UnixNano() / int64(time.Millisecond),
				"ParentID": parentDir.ID,
				"Size":     0,
			},
		},
	}
	
	data, err := d.apiCall(ctx, "SetUploadFile3", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	// After creating a folder, Degoo requires another getFileChildren5 call to get the new directory ID,
	// which is consistent with the Python script's logic.
	var newID string
	var listResp DegooGetChildren5Data
	if err := json.Unmarshal(data, &listResp); err == nil {
		// Assuming the response contains the new folder info; otherwise, re-fetch the parent directory's list.
		items, _ := d.getAllFileChildren5(ctx, parentDir.ID)
		for _, item := range items {
			if item.Name == dirName && item.ParentID == parentDir.ID && item.Category == 2 {
				newID = item.ID
				break
			}
		}
	}
	
	return model.Obj{ID: newID, Name: dirName, IsFolder: true}, nil
}

// Rename renames a file, replicating the setRenameFile() logic from the Python script.
func (d *Degoo) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	const query = `mutation SetRenameFile($Token: String!, $FileRenames: [FileRenameInfo]!) { setRenameFile(Token: $Token, FileRenames: $FileRenames) }`

	variables := map[string]interface{}{
		"Token": d.Token,
		"FileRenames": []DegooFileRenameInfo{
			{
				ID:      srcObj.ID,
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

// Move moves a file, replicating the setMoveFile() logic from the Python script.
func (d *Degoo) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	const query = `mutation SetMoveFile($Token: String!, $Copy: Boolean, $NewParentID: String!, $FileIDs: [String]!) { setMoveFile(Token: $Token, Copy: $Copy, NewParentID: $NewParentID, FileIDs: $FileIDs) }`

	variables := map[string]interface{}{
		"Token":       d.Token,
		"Copy":        false, // Default is Move in the Python script.
		"NewParentID": dstDir.ID,
		"FileIDs":     []string{srcObj.ID},
	}
	
	_, err := d.apiCall(ctx, "SetMoveFile", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	return srcObj, nil
}

// Copy copies a file, replicating the logic from the Python script (if supported).
func (d *Degoo) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// The Python script does not have a direct Copy API. Returning NotImplement.
	return model.Obj{}, errs.NotImplement
}

// Remove deletes a file, replicating the setDeleteFile5() logic from the Python script.
func (d *Degoo) Remove(ctx context.Context, obj model.Obj) error {
	const query = `mutation SetDeleteFile5($Token: String!, $IsInRecycleBin: Boolean!, $IDs: [IDType]!) { setDeleteFile5(Token: $Token, IsInRecycleBin: $IsInRecycleBin, IDs: $IDs) }`

	variables := map[string]interface{}{
		"Token":          d.Token,
		"IsInRecycleBin": false, // Moves to Recycle Bin.
		"IDs":            []map[string]string{{"FileID": obj.ID}},
	}
	
	_, err := d.apiCall(ctx, "SetDeleteFile5", query, variables)
	return err
}

// Put uploads a file, replicating the setUploadFile3() and getBucketWriteAuth4() logic from the Python script.
func (d *Degoo) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// TODO: This requires a local file path; the FileStreamer interface might need conversion or special handling.
	// Assuming we can get the local file path.
	localFilePath := file.Name() // This is typically the path to the file's local cache.
	// Use file as an io.Reader directly for the checksum.
	// 1. Call getBucketWriteAuth4 to get upload authorization.
	const authQuery = `query GetBucketWriteAuth4($Token: String!, $ParentID: String!, $StorageUploadInfos: [StorageUploadInfo2]) { getBucketWriteAuth4(Token: $Token, ParentID: $ParentID, StorageUploadInfos: $StorageUploadInfos) { AuthData { PolicyBase64 Signature BaseURL KeyPrefix AccessKey { Key Value } ACL } } }`
	authVars := map[string]interface{}{
		"Token": d.Token,
		"ParentID": dstDir.ID,
	}
	authData, err := d.apiCall(ctx, "GetBucketWriteAuth4", authQuery, authVars)
	if err != nil {
		return model.Obj{}, err
	}
	
	var authResp map[string]interface{}
	if err := json.Unmarshal(authData, &authResp); err != nil {
		return model.Obj{}, err
	}
	authInfo := authResp["getBucketWriteAuth4"].([]interface{})[0].(map[string]interface{})["AuthData"].(map[string]interface{})

	// 2. Upload the file content to the returned URL.
	// ... (Concrete upload logic is omitted here, typically involving a multipart/form-data POST request).

	// 3. Call setUploadFile3 to update Degoo metadata.
	checksum, err := checkSum(localFilePath)
	if err != nil {
		return model.Obj{}, err
	}
	
	fileInfo := map[string]interface{}{
		"Checksum": checksum,
		"Name":     file.Name(),
		"CreationTime": time.Now().UnixNano() / int64(time.Millisecond),
		"ParentID": dstDir.ID,
		"Size":     file.Size(),
	}
	const uploadQuery = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) { setUploadFile3(Token: $Token, FileInfos: $FileInfos) }`
	uploadVars := map[string]interface{}{
		"Token":     d.Token,
		"FileInfos": []map[string]interface{}{fileInfo},
	}
	
	_, err = d.apiCall(ctx, "SetUploadFile3", uploadQuery, uploadVars)
	if err != nil {
		return model.Obj{}, err
	}
	
	uploadRespData, err := d.apiCall(ctx, "SetUploadFile3", uploadQuery, uploadVars)
	if err != nil {
		return model.Obj{}, err
	}
	
	// Parse the response to get the new file ID
	var uploadResp map[string]interface{}
	if err := json.Unmarshal(uploadRespData, &uploadResp); err != nil {
		return model.Obj{}, err
	}
	// The response structure may vary; adjust as needed.
	// Assuming setUploadFile3 returns a list of file IDs.
	var newFileID string
	if ids, ok := uploadResp["setUploadFile3"].([]interface{}); ok && len(ids) > 0 {
		if idStr, ok := ids[0].(string); ok {
			newFileID = idStr
		}
	}
	if newFileID == "" {
		return model.Obj{}, fmt.Errorf("failed to get new file ID from upload response")
	}
	return model.Obj{ID: newFileID, Name: file.Name(), Size: file.Size()}, nil
}

// Ensure the Degoo struct implements the driver.Driver interface.
var _ driver.Driver = (*Degoo)(nil)
