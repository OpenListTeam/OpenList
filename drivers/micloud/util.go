package micloud

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	streamPkg "github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/tidwall/gjson"
)

// MiCloudClient 小米云盘客户端
type MiCloudClient struct {
	userId         string
	serviceToken   string
	deviceId       string
	httpClient     *http.Client
	rootId         string
	pathIdCache    map[string]string // 路径到ID的缓存
	cancelRenew    context.CancelFunc
	onCookieUpdate func(userId, serviceToken, deviceId string)
}

// NewMiCloudClient 创建小米云盘客户端
func NewMiCloudClient(userId, serviceToken, deviceId string) (*MiCloudClient, error) {
	client := &MiCloudClient{
		userId:       userId,
		serviceToken: serviceToken,
		deviceId:     deviceId,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		rootId:      "0",
		pathIdCache: make(map[string]string),
	}

	return client, nil
}

// StartAutoRenewal 周期性调用自动续期接口，刷新 serviceToken 等 Cookie
func (c *MiCloudClient) StartAutoRenewal() {
	// 如果已在运行，先停止
	c.StopAutoRenewal()
	ctx, cancel := context.WithCancel(context.Background())
	c.cancelRenew = cancel
	ticker := time.NewTicker(30 * time.Second)
	go func() {
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				ts := time.Now().UnixMilli()
				url := fmt.Sprintf(AutoRenewal, ts)
				req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
				// 加上基础头与 cookie
				req.Header.Set("User-Agent", "Mozilla/5.0")
				req.Header.Set("Referer", "https://i.mi.com/")
				req.AddCookie(&http.Cookie{Name: "userId", Value: c.userId})
				req.AddCookie(&http.Cookie{Name: "serviceToken", Value: c.serviceToken})
				req.AddCookie(&http.Cookie{Name: "deviceId", Value: c.deviceId})
				resp, err := c.httpClient.Do(req)
				if err != nil {
					continue
				}
				// 若服务端下发新 cookie，则更新
				c.updateCookiesFromResponse(resp)
				io.Copy(io.Discard, resp.Body)
				resp.Body.Close()
			case <-ctx.Done():
				return
			}
		}
	}()
}

// StopAutoRenewal 停止自动续期
func (c *MiCloudClient) StopAutoRenewal() {
	if c.cancelRenew != nil {
		c.cancelRenew()
		c.cancelRenew = nil
	}
}

// 在收到响应时，若包含新的 cookie，刷新到客户端
func (c *MiCloudClient) updateCookiesFromResponse(resp *http.Response) {
	for _, ck := range resp.Cookies() {
		switch ck.Name {
		case "serviceToken":
			c.serviceToken = ck.Value
		case "userId":
			c.userId = ck.Value
		case "deviceId":
			c.deviceId = ck.Value
		}
	}
	if c.onCookieUpdate != nil {
		c.onCookieUpdate(c.userId, c.serviceToken, c.deviceId)
	}
}

// SetOnCookieUpdate 注册 Cookie 刷新回调（用于把新的 serviceToken 持久化到存储配置）
func (c *MiCloudClient) SetOnCookieUpdate(cb func(userId, serviceToken, deviceId string)) {
	c.onCookieUpdate = cb
}

// Login 登录或验证登录状态
func (c *MiCloudClient) Login() error {
	// 验证当前token是否有效
	resp, err := c.Get(fmt.Sprintf(GetFolders, c.rootId))
	if err != nil {
		return fmt.Errorf("验证登录状态失败: %w", err)
	}

	result := gjson.Get(string(resp), "result").String()
	if result != "ok" {
		return fmt.Errorf("登录验证失败，可能服务令牌无效")
	}

	return nil
}

// GetFolder 获取文件夹内容
func (c *MiCloudClient) GetFolder(folderId string) ([]File, error) {
	result, err := c.Get(fmt.Sprintf(GetFolders, folderId))
	if err != nil {
		return nil, err
	}

	var msg Msg
	if err := json.Unmarshal(result, &msg); err != nil {
		return nil, err
	}

	if msg.Result == "ok" {
		return msg.Data.List, nil
	} else {
		return nil, fmt.Errorf("获取文件夹信息失败: %s", string(result))
	}
}

// 直链下载已使用 v2 接口实现，去除 JSONP 下载方式

// GetFileDownLoadUrl 获取文件下载URL
func (c *MiCloudClient) GetFileDownLoadUrl(fileId string) (string, error) {
	// 使用 v2 接口获取直链
	ts := time.Now().UnixMilli()
	ids := fmt.Sprintf("[%q]", fileId)
	q := url.Values{}
	q.Set("ts", fmt.Sprintf("%d", ts))
	q.Set("ids", ids)
	// 注意：ids 需要作为原始 JSON 数组，不进行额外转义
	full := GetDirectDL + "?" + q.Encode()
	// Encode 会对 ids 进行转义成 %5B%22...%22%5D，符合示例
	resp, err := c.Get(full)
	if err != nil {
		return "", err
	}
	if gjson.Get(string(resp), "result").String() != "ok" {
		return "", fmt.Errorf("获取直链失败: %s", string(resp))
	}
	// 兼容 downLoads / downloads
	dl := gjson.Get(string(resp), "data.downLoads.0.downloadUrl").String()
	if dl == "" {
		dl = gjson.Get(string(resp), "data.downloads.0.downloadUrl").String()
	}
	if dl == "" {
		return "", fmt.Errorf("直链为空")
	}
	return dl, nil
}

// JSONP 下载信息方法已废弃

// DeleteFile 删除文件
func (c *MiCloudClient) DeleteFile(fileId, fType string) error {
	// 构造删除记录
	record := []struct {
		Id   string `json:"id"`
		Type string `json:"type"`
	}{{
		Id:   fileId,
		Type: fType,
	}}

	content, _ := json.Marshal(record)

	resp, err := c.PostForm(DeleteFiles, url.Values{
		"operateType":    []string{"DELETE"},
		"operateRecords": []string{string(content)},
		"serviceToken":   []string{c.serviceToken},
	})
	if err != nil {
		return err
	}

	if result := gjson.Get(string(resp), "result").String(); result == "ok" {
		return nil
	} else {
		return fmt.Errorf("删除失败: %s", string(resp))
	}
}

// CreateFolder 创建文件夹
func (c *MiCloudClient) CreateFolder(name, parentId string) (string, error) {
	resp, err := c.PostForm(CreateFolder, url.Values{
		"name":         []string{name},
		"parentId":     []string{parentId},
		"serviceToken": []string{c.serviceToken},
	})
	if err != nil {
		return "", err
	}

	if result := gjson.Get(string(resp), "result").String(); result == "ok" {
		return gjson.Get(string(resp), "data.id").String(), nil
	} else {
		return "", fmt.Errorf("创建目录失败: %s", string(resp))
	}
}

// Get 执行GET请求
func (c *MiCloudClient) Get(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	// 设置请求头
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Referer", "https://i.mi.com/")

	// 设置Cookie
	req.AddCookie(&http.Cookie{
		Name:  "userId",
		Value: c.userId,
	})
	req.AddCookie(&http.Cookie{
		Name:  "serviceToken",
		Value: c.serviceToken,
	})
	req.AddCookie(&http.Cookie{
		Name:  "deviceId",
		Value: c.deviceId,
	})

	result, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	// 更新 cookie（若有）
	c.updateCookiesFromResponse(result)

	if result.StatusCode == http.StatusFound {
		return c.Get(result.Header.Get("Location"))
	}
	if result.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("登录授权失败")
	}

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}

	if gjson.Get(string(body), "R").Int() == 401 {
		return nil, fmt.Errorf("登录授权失败")
	}

	return body, nil
}

// PostForm 执行POST表单请求
func (c *MiCloudClient) PostForm(url string, values url.Values) ([]byte, error) {
	req, err := http.NewRequest("POST", url, strings.NewReader(values.Encode()))
	if err != nil {
		return nil, err
	}

	// 设置请求头
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	req.Header.Set("Referer", "https://i.mi.com/")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	// 设置Cookie
	req.AddCookie(&http.Cookie{
		Name:  "userId",
		Value: c.userId,
	})
	req.AddCookie(&http.Cookie{
		Name:  "serviceToken",
		Value: c.serviceToken,
	})
	req.AddCookie(&http.Cookie{
		Name:  "deviceId",
		Value: c.deviceId,
	})

	result, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	// 更新 cookie（若有）
	c.updateCookiesFromResponse(result)

	if result.StatusCode == http.StatusUnauthorized {
		return nil, fmt.Errorf("登录授权失败")
	}

	body, err := io.ReadAll(result.Body)
	if err != nil {
		return nil, err
	}

	if gjson.Get(string(body), "R").Int() == 401 {
		return nil, fmt.Errorf("登录授权失败")
	}

	return body, nil
}

// pathToId 路径转ID
func (c *MiCloudClient) pathToId(path string) (string, error) {
	if path == "/" || path == "" {
		return c.rootId, nil
	}

	// 检查缓存
	if id, ok := c.pathIdCache[path]; ok {
		return id, nil
	}

	// 逐级解析路径
	paths := strings.Split(strings.Trim(path, "/"), "/")
	currentId := c.rootId

	for _, p := range paths {
		if p == "" {
			continue
		}

		// 获取当前目录下的文件列表
		files, err := c.GetFolder(currentId)
		if err != nil {
			return "", fmt.Errorf("获取目录 %s 失败: %w", path, err)
		}

		// 查找对应的目录
		found := false
		for _, file := range files {
			if file.Name == p && file.Type == "folder" {
				currentId = file.Id
				found = true
				break
			}
		}

		if !found {
			return "", fmt.Errorf("路径不存在: %s", path)
		}
	}

	// 缓存结果
	c.pathIdCache[path] = currentId
	return currentId, nil
}

// Delete 删除文件或目录
func (c *MiCloudClient) Delete(fileId string) error {
	// 首先获取文件信息以确定类型
	// 这里我们假设在删除时已经知道文件类型，可以传入"file"或"folder"
	// 通常在OpenList中，删除操作会先获取对象信息，所以我们可以假定知道类型
	return c.DeleteFile(fileId, "file") // 默认为文件，实际使用时应根据实际情况传入类型
}

// Move 移动文件或目录（小米云盘API可能不直接支持移动，需要复制后删除）
func (c *MiCloudClient) Move(fileId, targetParentId string) (*File, error) {
	// 小米云盘API没有直接的移动接口，我们先实现一个简单的版本
	// 实际上可能需要先复制再删除，或者检查API是否有相关功能
	return nil, fmt.Errorf("移动功能暂未实现")
}

// Rename 重命名文件或目录
func (c *MiCloudClient) Rename(fileId, newName string) (*File, error) {
	// 小米云盘API没有直接的重命名接口，可能需要通过其他方式实现
	return nil, fmt.Errorf("重命名功能暂未实现")
}

// Upload 上传文件的完整实现
func (c *MiCloudClient) Upload(parentId string, file model.FileStreamer, up driver.UpdateProgress) (*File, error) {
	fileSize := file.GetSize()
	fileName := file.GetName()

	// 缓存完整流并计算整体 SHA1（不破坏后续读取）
	var upPtr = model.UpdateProgress(up)
	tmpF, fileSha1, err := streamPkg.CacheFullAndHash(file, &upPtr, utils.SHA1)
	if err != nil {
		return nil, fmt.Errorf("缓存与计算SHA1失败: %w", err)
	}

	// 计算块信息
	blocks, err := c.getFileBlocks(tmpF, fileSize)
	if err != nil {
		return nil, fmt.Errorf("计算文件分片失败: %w", err)
	}

	// 组装创建分片请求
	upJson := UploadJson{
		Content: UploadContent{
			Name: fileName,
			Storage: UploadStorage{
				Size: fileSize,
				Sha1: fileSha1,
				Kss:  UploadKss{BlockInfos: blocks},
			},
		},
	}
	data, _ := json.Marshal(upJson)

	// 创建分片
	form := url.Values{}
	form.Add("data", string(data))
	form.Add("serviceToken", c.serviceToken)
	resp, err := c.PostForm(CreateFile, form)
	if err != nil {
		return nil, err
	}
	if gjson.Get(string(resp), "result").String() != "ok" {
		return nil, fmt.Errorf("创建文件分片失败: %s", string(resp))
	}

	// 文件已存在于云端，直接完成
	if gjson.Get(string(resp), "data.storage.exists").Bool() {
		data := UploadJson{Content: UploadContent{
			Name: fileName,
			Storage: UploadExistedStorage{
				UploadId: gjson.Get(string(resp), "data.storage.uploadId").String(),
				Exists:   true,
			},
		}}
		return c.finalizeCreate(parentId, data)
	}

	// 不存在则上传每个分块
	kss := gjson.Get(string(resp), "data.storage.kss")
	nodeUrls := kss.Get("node_urls").Array()
	fileMeta := kss.Get("file_meta").String()
	blockMetas := kss.Get("block_metas").Array()
	if len(nodeUrls) == 0 || fileMeta == "" {
		return nil, fmt.Errorf("暂无可用上传节点")
	}
	apiNode := nodeUrls[0].String()

	// 逐块上传
	var commitMetas []map[string]string
	var uploaded int64
	for i, blk := range blockMetas {
		cm, sz, err := c.uploadBlock(apiNode, fileMeta, tmpF, int64(i), blk)
		if err != nil {
			return nil, err
		}
		commitMetas = append(commitMetas, cm)
		uploaded += sz
		if up != nil && fileSize > 0 {
			up(float64(uploaded) * 100 / float64(fileSize))
		}
	}

	// 完成上传
	data2 := UploadJson{Content: UploadContent{
		Name: fileName,
		Storage: UploadStorage{
			Size: fileSize,
			Sha1: fileSha1,
			Kss: Kss{
				Stat:            "OK",
				NodeUrls:        nodeUrls,
				SecureKey:       kss.Get("secure_key").String(),
				ContentCacheKey: kss.Get("contentCacheKey").String(),
				FileMeta:        kss.Get("file_meta").String(),
				CommitMetas:     commitMetas,
			},
			UploadId: gjson.Get(string(resp), "data.storage.uploadId").String(),
			Exists:   false,
		},
	}}
	return c.finalizeCreate(parentId, data2)
}

// finalizeCreate 最终创建文件
func (c *MiCloudClient) finalizeCreate(parentId string, data UploadJson) (*File, error) {
	dataJson, err := json.Marshal(data)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Add("data", string(dataJson))
	form.Add("serviceToken", c.serviceToken)
	form.Add("parentId", parentId)

	request, err := http.NewRequest("POST", UploadFile, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	// headers
	request.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/91.0.4472.124 Safari/537.36")
	request.Header.Set("Referer", "https://i.mi.com/")
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	// cookies
	request.AddCookie(&http.Cookie{Name: "userId", Value: c.userId})
	request.AddCookie(&http.Cookie{Name: "serviceToken", Value: c.serviceToken})
	request.AddCookie(&http.Cookie{Name: "deviceId", Value: c.deviceId})

	resp, err := c.httpClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if gjson.Get(string(body), "result").String() != "ok" {
		return nil, fmt.Errorf("创建文件失败: %s", string(body))
	}
	// assemble result
	fileId := gjson.Get(string(body), "data.id").String()
	rName := gjson.Get(string(body), "data.name").String()
	rSize := gjson.Get(string(body), "data.size").Int()
	rType := gjson.Get(string(body), "data.type").String()
	return &File{Id: fileId, Name: rName, Size: rSize, Type: rType}, nil
}

// getFileBlocks 计算文件分片（4MB一块）
func (c *MiCloudClient) getFileBlocks(tmp model.File, fileSize int64) ([]BlockInfo, error) {
	if fileSize <= ChunkSize {
		// small file: compute full hashes
		sha1Str, err := utils.HashFile(utils.SHA1, tmp)
		if err != nil {
			return nil, err
		}
		md5Str, err := utils.HashFile(utils.MD5, tmp)
		if err != nil {
			return nil, err
		}
		return []BlockInfo{{Blob: struct{}{}, Sha1: sha1Str, Md5: md5Str, Size: fileSize}}, nil
	}
	num := int((fileSize + ChunkSize - 1) / ChunkSize)
	blocks := make([]BlockInfo, 0, num)
	for i := 0; i < num; i++ {
		off := int64(i) * ChunkSize
		sz := int64(ChunkSize)
		if off+sz > fileSize {
			sz = fileSize - off
		}
		sr := io.NewSectionReader(tmp, off, sz)
		sha1Str, err := utils.HashReader(utils.SHA1, sr)
		if err != nil {
			return nil, err
		}
		sr = io.NewSectionReader(tmp, off, sz)
		md5Str, err := utils.HashReader(utils.MD5, sr)
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, BlockInfo{Blob: struct{}{}, Sha1: sha1Str, Md5: md5Str, Size: sz})
	}
	return blocks, nil
}

// uploadBlock 上传单个分块
func (c *MiCloudClient) uploadBlock(apiNode, fileMeta string, tmp model.File, idx int64, blk gjson.Result) (map[string]string, int64, error) {
	if blk.Get("is_existed").Int() == 1 {
		return map[string]string{"commit_meta": blk.Get("commit_meta").String()}, 0, nil
	}
	uploadURL := apiNode + "/upload_block_chunk?chunk_pos=0&file_meta=" + fileMeta + "&block_meta=" + blk.Get("block_meta").String()
	off := idx * ChunkSize
	// read chunk
	sz := int64(ChunkSize)
	buf := make([]byte, sz)
	n, err := tmp.ReadAt(buf, off)
	if err != nil && err != io.EOF {
		return nil, 0, err
	}
	buf = buf[:n]
	req, _ := http.NewRequest("POST", uploadURL, strings.NewReader(string(buf)))
	req.Header.Set("DNT", "1")
	req.Header.Set("Origin", "https://i.mi.com")
	req.Header.Set("Referer", "https://i.mi.com/drive")
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if gjson.Get(string(body), "stat").String() != "BLOCK_COMPLETED" {
		return nil, 0, fmt.Errorf("block not completed: %s", string(body))
	}
	return map[string]string{"commit_meta": gjson.Get(string(body), "commit_meta").String()}, int64(n), nil
}

// 上传小文件（小于4MB）
// uploadSmallFile/uploadLargeFile paths were deprecated and removed. The unified Upload handles both cases.

// GetStorageDetails 获取存储详情
func (c *MiCloudClient) GetStorageDetails() (*model.StorageDetails, error) {
	return nil, fmt.Errorf("获取存储详情功能暂未实现")
}

// ConvertFileToObj 将小米云盘文件转换为OpenList对象
func ConvertFileToObj(file File) *model.Object {
	// 小米云时间戳可能是毫秒或秒，这里智能判断
	toTime := func(ts uint) time.Time {
		v := int64(ts)
		if v <= 0 {
			return time.Time{}
		}
		if v > 1_000_000_000_000 { // > ~2001-09 in ms
			return time.UnixMilli(v)
		}
		return time.Unix(v, 0)
	}

	obj := &model.Object{
		ID:       file.Id,
		Name:     file.Name,
		Size:     file.Size,
		Modified: toTime(file.ModifyTime),
		Ctime:    toTime(file.CreateTime),
		IsFolder: file.Type == "folder",
	}

	return obj
}
