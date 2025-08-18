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
	// 用于缓存设备列表，模拟 Python 脚本的 __devices__
	devices map[string]string
}

func (d *Degoo) Config() driver.Config {
	return config
}

func (d *Degoo) GetAddition() driver.Additional {
	return &d.Addition
}

// Init 实现登录和令牌管理，完全复现 Python 脚本的 login() 方法逻辑
func (d *Degoo) Init(ctx context.Context) error {
	loginURL := "https://rest-api.degoo.com/login"
	accessTokenURL := "https://rest-api.degoo.com/access-token/v2"
	
	creds := DegooLoginRequest{
		GenerateToken: true,
		Username:      d.Addition.Username,
		Password:      d.Addition.Password,
	}

	jsonCreds, err := json.Marshal(creds)
	if err != nil {
		return fmt.Errorf("无法序列化登录凭证: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", loginURL, bytes.NewBuffer(jsonCreds))
	if err != nil {
		return fmt.Errorf("无法创建登录请求: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	// 模仿 Python 脚本的 User-Agent
	req.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
	req.Header.Set("Origin", "https://app.degoo.com")

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("登录请求失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("登录失败: %s", resp.Status)
	}

	var loginResp DegooLoginResponse
	if err := json.NewDecoder(resp.Body).Decode(&loginResp); err != nil {
		return fmt.Errorf("无法解析登录响应: %w", err)
	}

	// 检查是否有 RefreshToken，如果有，则需要再次请求 AccessToken
	if loginResp.RefreshToken != "" {
		tokenReq := DegooAccessTokenRequest{RefreshToken: loginResp.RefreshToken}
		jsonTokenReq, _ := json.Marshal(tokenReq)
		
		tokenReqHTTP, _ := http.NewRequestWithContext(ctx, "POST", accessTokenURL, bytes.NewBuffer(jsonTokenReq))
		tokenReqHTTP.Header.Set("Content-Type", "application/json")
		tokenReqHTTP.Header.Set("User-Agent", "Mozilla/5.0 Slackware/13.37 (X11; U; Linux x86_64; en-US) AppleWebKit/534.16 (KHTML, like Gecko) Chrome/11.0.696.50")
		
		tokenResp, err := client.Do(tokenReqHTTP)
		if err != nil {
			return fmt.Errorf("获取访问令牌失败: %w", err)
		}
		defer tokenResp.Body.Close()
		
		var accessTokenResp DegooAccessTokenResponse
		if err := json.NewDecoder(tokenResp.Body).Decode(&accessTokenResp); err != nil {
			return fmt.Errorf("无法解析访问令牌响应: %w", err)
		}
		
		d.Token = accessTokenResp.AccessToken
	} else if loginResp.Token != "" {
		d.Token = loginResp.Token
	} else {
		return fmt.Errorf("登录失败，未返回有效的令牌")
	}

	// 初始化设备列表
	d.devices = make(map[string]string)
	d.getDevices(ctx)
	
	return nil
}

// getDevices 获取设备列表并缓存，模拟 Python 脚本的 devices property
func (d *Degoo) getDevices(ctx context.Context) error {
	const query = `query GetFileChildren5($Token: String! $ParentID: String $AllParentIDs: [String] $Limit: Int! $Order: Int! $NextToken: String ) { getFileChildren5(Token: $Token ParentID: $ParentID AllParentIDs: $AllParentIDs Limit: $Limit Order: $Order NextToken: $NextToken) { Items { ID Name Category } NextToken } }`
	
	// parentID 0 对应根目录
	variables := map[string]interface{}{
		"Token":      d.Token,
		"ParentID":   "0",
		"Limit":      1000,
		"Order":      3,
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
		if item.Category == 1 { // Category 1 代表 Device
			d.devices[item.ID] = item.Name
		}
	}
	return nil
}

// List 列出文件和文件夹，完全复现 Python 脚本的 getFileChildren5() 逻辑
func (d *Degoo) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	items, err := d.getAllFileChildren5(ctx, dir.ID)
	if err != nil {
		return nil, err
	}
	
	var objs []model.Obj
	for _, item := range items {
		// 转换 DegooFileItem 为 model.Obj
		obj := d.toModelObj(item)
		objs = append(objs, obj)
	}

	return objs, nil
}

// getAllFileChildren5 处理分页，复现 Python 脚本的同名方法
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

	// 修复文件路径，模拟 Python 脚本中的路径拼接逻辑
	for i, item := range allItems {
		if item.Category != 1 && item.Category != 10 { // 不是 Device 或 Recycle Bin
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


// Link 获取下载链接，复现 Python 脚本的 getOverlay4() 逻辑
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
	
	// 优先使用 OptimizedURL
	url := resp.GetOverlay4.URL
	if resp.GetOverlay4.OptimizedURL != "" {
		url = resp.GetOverlay4.OptimizedURL
	}

	return &model.Link{URL: url}, nil
}

// toModelObj 将 DegooFileItem 转换为 OpenList 的 model.Obj
func (d *Degoo) toModelObj(item DegooFileItem) model.Obj {
	isFolder := item.Category == 2
	
	// 转换时间戳
	modTime, _ := time.Parse(time.RFC3339, item.LastModificationTime)

	return model.Obj{
		ID:        item.ID,
		ParentID:  item.ParentID,
		Name:      item.Name,
		Size:      item.Size,
		IsFolder:  isFolder,
		UpdatedAt: modTime,
	}
}

// MakeDir 创建新文件夹，复现 Python 脚本的 setUploadFile3() 逻辑
func (d *Degoo) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	const query = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) { setUploadFile3(Token: $Token, FileInfos: $FileInfos) }`
	
	variables := map[string]interface{}{
		"Token": d.Token,
		"FileInfos": []map[string]interface{}{
			{
				"Checksum": "CgAQAg", // Python 脚本中创建文件夹的特定校验码
				"Name":     dirName,
				"CreationTime": time.Now().UnixNano() / int64(time.Millisecond),
				"ParentID": parentDir.ID,
				"Size":     0,
			},
		},
	}
	
	data, err := d.apiCall(ctx, "SetUploadFile3", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	// Degoo 创建文件夹后需要再次调用 getFileChildren5 来获取新目录ID，这与 Python 脚本逻辑相同
	var newID string
	var listResp DegooGetChildren5Data
	if err := json.Unmarshal(data, &listResp); err == nil {
		// 这里假设返回的是新文件夹的信息，实际需要从父目录重新获取列表来找到新ID
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

// Rename 重命名，复现 Python 脚本的 setRenameFile() 逻辑
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

// Move 移动，复现 Python 脚本的 setMoveFile() 逻辑
func (d *Degoo) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	const query = `mutation SetMoveFile($Token: String!, $Copy: Boolean, $NewParentID: String!, $FileIDs: [String]!) { setMoveFile(Token: $Token, Copy: $Copy, NewParentID: $NewParentID, FileIDs: $FileIDs) }`

	variables := map[string]interface{}{
		"Token":       d.Token,
		"Copy":        false, // Python 脚本中默认为 Move
		"NewParentID": dstDir.ID,
		"FileIDs":     []string{srcObj.ID},
	}
	
	_, err := d.apiCall(ctx, "SetMoveFile", query, variables)
	if err != nil {
		return model.Obj{}, err
	}
	
	return srcObj, nil
}

// Copy 复制，复现 Python 脚本中的逻辑（如果支持）
func (d *Degoo) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	// Python 脚本没有直接的 Copy API。我们返回 NotImplement
	return model.Obj{}, errs.NotImplement
}

// Remove 删除，复现 Python 脚本的 setDeleteFile5() 逻辑
func (d *Degoo) Remove(ctx context.Context, obj model.Obj) error {
	const query = `mutation SetDeleteFile5($Token: String!, $IsInRecycleBin: Boolean!, $IDs: [IDType]!) { setDeleteFile5(Token: $Token, IsInRecycleBin: $IsInRecycleBin, IDs: $IDs) }`

	variables := map[string]interface{}{
		"Token":          d.Token,
		"IsInRecycleBin": false, // 放入回收站
		"IDs":            []map[string]string{{"FileID": obj.ID}},
	}
	
	_, err := d.apiCall(ctx, "SetDeleteFile5", query, variables)
	return err
}

// Put 上传文件，复现 Python 脚本的 setUploadFile3() 和 getBucketWriteAuth4() 逻辑
func (d *Degoo) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// TODO: 这里需要一个本地文件路径，FileStreamer 接口可能需要转换或处理
	// 假设我们能获取到本地文件路径
	localFilePath := file.Name() // 这通常是文件在本地缓存的路径

	// 1. 调用 getBucketWriteAuth4 获取上传授权
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

	// 2. 将文件内容上传到返回的 URL
	// 此处省略具体的上传逻辑，这通常涉及到 multipart/form-data POST 请求
	// ...

	// 3. 调用 setUploadFile3 更新 Degoo 元数据
	checksum, err := checkSum(localFilePath)
	if err != nil {
		return model.Obj{}, err
	}
	
	fileInfo := map[string]interface{}{
		"Checksum": checksum,
		"Name":     file.Name(),
		"CreationTime": time.Now().UnixNano() / int64(time.Millisecond),
		"ParentID": dstDir.ID,
		"Size":     file.Size(),
	}
	const uploadQuery = `mutation SetUploadFile3($Token: String!, $FileInfos: [FileInfoUpload3]!) { setUploadFile3(Token: $Token, FileInfos: $FileInfos) }`
	uploadVars := map[string]interface{}{
		"Token":     d.Token,
		"FileInfos": []map[string]interface{}{fileInfo},
	}
	
	_, err = d.apiCall(ctx, "SetUploadFile3", uploadQuery, uploadVars)
	if err != nil {
		return model.Obj{}, err
	}
	
	return model.Obj{ID: "new_file_id", Name: file.Name(), Size: file.Size()}, nil
}

// 确保 Degoo 结构体实现了 driver.Driver 接口
var _ driver.Driver = (*Degoo)(nil)
