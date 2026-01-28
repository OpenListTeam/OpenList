package flash_transfer

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

type FlashClient struct {
	BaseUrl    string
	HttpClient *http.Client
	SceneType  int
	Cookie     string
}

func NewFlashClient() *FlashClient {
	return &FlashClient{
		BaseUrl: "https://qfile.qq.com",
		HttpClient: &http.Client{
			Timeout: time.Second * 5,
		},
		SceneType: 103,
		Cookie:    "uin=9000002; p_uin=9000002;", // 匿名 QQ UIN
	}
}

func (c *FlashClient) doRequest(url string, cmdType string, reqBody interface{}, respData interface{}) error {
	jsonBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("json marshal err: %v", err)
	}

	req, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonBytes))
	if err != nil {
		return fmt.Errorf("create request err: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Cookie", c.Cookie)
	req.Header.Set("X-Oidb", cmdType)

	resp, err := c.HttpClient.Do(req)
	if err != nil {
		return fmt.Errorf("http do err: %v", err)
	}

	defer func() { _ = resp.Body.Close() }()

	bodyBytes, _ := io.ReadAll(resp.Body)
	if err := json.Unmarshal(bodyBytes, respData); err != nil {
		return fmt.Errorf("json unmarshal err: %v | body: %s", err, string(bodyBytes))
	}

	return nil
}

func (c *FlashClient) GetFileFolder(fileSetID string, ParentID string) (*FileListResponse, error) {
	url := c.BaseUrl + "/http2rpc/gotrpc/noauth/trpc.file.FileFlashTrans/GetFileList"
	cmd := `{"uint32_command":"0x93d4", "uint32_service_type":"1"}`
	payload := FileFolderRequests{
		FilesetId:           fileSetID,
		SupportFolderStatus: true,
		SceneType:           c.SceneType,

		ReqInfos: []ReqInfo{
			{
				ParentId:       ParentID,
				ReqDepth:       1,
				Count:          70,
				PaginationInfo: nil,

				FilterCondition: struct {
					FileCategory int `json:"file_category"`
				}{
					FileCategory: 0,
				},
				SortConditions: []struct {
					SortField int `json:"sort_field"`
					SortOrder int `json:"sort_order"`
				}{
					{
						SortField: 0,
						SortOrder: 0,
					},
				},
			},
		},
	}
	var result FileListResponse

	if err := c.doRequest(url, cmd, payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *FlashClient) GetCompressedFileFolder(fileSetID string, cliFileID string, parentID string) (*CompressedFileFolderResponse, error) {
	url := c.BaseUrl + "/http2rpc/gotrpc/noauth/trpc.file.flashtransfer.FlashTransferService/GetCompressedFileFolder"
	cmd := `{"uint32_command":"0x9402", "uint32_service_type":"1"}`
	payload := CompressedFolderRequests{
		FilesetId: fileSetID,
		CliFileId: cliFileID,
		ReqInfos: []struct {
			ParentId string `json:"parent_id"`
			ReqDepth int    `json:"req_depth"`
			Count    int    `json:"count"`
		}{
			{
				ParentId: parentID,
				ReqDepth: 1,
				Count:    70,
			},
		},
		SceneType: c.SceneType,
	}

	var result CompressedFileFolderResponse
	if err := c.doRequest(url, cmd, payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *FlashClient) GetDownloadUrl(batchId string, fileUuid string) (*DownloadUrlResponse, error) {
	url := c.BaseUrl + "/http2rpc/gotrpc/noauth/trpc.qqntv2.richmedia.InnerProxy/BatchDownload"
	cmd := `{"uint32_command":"0x9248", "uint32_service_type":"4"}`
	payload := DownloadUrlRequests{
		ReqHead: struct {
			Agent int `json:"agent"`
		}{Agent: 8},
		DownloadInfo: []DownloadInfo{
			{
				BatchId: batchId,
				Scene: struct {
					BusinessType int `json:"business_type"`
					AppType      int `json:"app_type"`
					SceneType    int `json:"scene_type"`
				}{
					BusinessType: 4,
					AppType:      22,
					SceneType:    5,
				},
				IndexNode: struct {
					FileUUid string `json:"file_uuid"`
				}{FileUUid: fileUuid},
				UrlType:       2,
				DownloadScene: 0,
			},
		},
		SceneType: c.SceneType,
	}

	var result DownloadUrlResponse
	if err := c.doRequest(url, cmd, payload, &result); err != nil {
		return nil, err
	}
	return &result, nil
}
