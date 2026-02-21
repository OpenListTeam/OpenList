package doubao_new

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/go-resty/resty/v2"
)

func (d *DoubaoNew) prepareUpload(ctx context.Context, name string, size int64, mountNodeToken string) (UploadPrepareData, error) {
	var resp UploadPrepareResp
	_, err := d.request(ctx, "/space/api/box/upload/prepare/", http.MethodPost, func(req *resty.Request) {
		values := url.Values{}
		values.Set("shouldBypassScsDialog", "true")
		values.Set("doubao_storage", "imagex_other")
		values.Set("doubao_app_id", "497858")
		req.SetQueryParamsFromValues(values)
		req.SetHeader("Content-Type", "application/json")
		req.SetHeader("x-command", "space.api.box.upload.prepare")
		req.SetHeader("rpc-persist-doubao-pan", "true")
		req.SetHeader("cache-control", "no-cache")
		req.SetHeader("pragma", "no-cache")
		body := base.Json{
			"mount_point":      "explorer",
			"mount_node_token": "",
			"name":             name,
			"size":             size,
			"size_checker":     true,
		}
		if mountNodeToken != "" {
			body["mount_node_token"] = mountNodeToken
		}
		req.SetBody(body)
	}, &resp)
	if err != nil {
		return UploadPrepareData{}, err
	}
	return resp.Data, nil
}

func (d *DoubaoNew) uploadBlocks(ctx context.Context, uploadID string, blocks []UploadBlock, mountPoint string) (UploadBlocksData, error) {
	if uploadID == "" {
		return UploadBlocksData{}, fmt.Errorf("[doubao_new] upload blocks missing upload_id")
	}
	if mountPoint == "" {
		mountPoint = "explorer"
	}
	var resp UploadBlocksResp
	_, err := d.request(ctx, "/space/api/box/upload/blocks/", http.MethodPost, func(req *resty.Request) {
		values := url.Values{}
		values.Set("shouldBypassScsDialog", "true")
		values.Set("doubao_storage", "imagex_other")
		values.Set("doubao_app_id", "497858")
		req.SetQueryParamsFromValues(values)
		req.SetHeader("Content-Type", "application/json")
		req.SetHeader("x-command", "space.api.box.upload.blocks")
		req.SetHeader("rpc-persist-doubao-pan", "true")
		req.SetHeader("cache-control", "no-cache")
		req.SetHeader("pragma", "no-cache")
		req.SetBody(base.Json{
			"blocks":      blocks,
			"upload_id":   uploadID,
			"mount_point": mountPoint,
		})
	}, &resp)
	if err != nil {
		return UploadBlocksData{}, err
	}
	return resp.Data, nil
}

func (d *DoubaoNew) mergeUploadBlocks(ctx context.Context, uploadID string, seqList []int, checksumList []string, sizeList []int64, blockOriginSize int64, data []byte) (UploadMergeData, error) {
	if uploadID == "" {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks missing upload_id")
	}
	if len(seqList) == 0 {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks empty seq list")
	}
	if len(checksumList) == 0 {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks empty checksum list")
	}
	if len(sizeList) != len(seqList) {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks size list mismatch")
	}
	if blockOriginSize <= 0 {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks invalid block origin size")
	}
	if len(data) == 0 {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks empty data")
	}

	seqHeader := joinIntComma(seqList)
	checksumHeader := buildCommaHeader(checksumList)

	client := base.NewRestyClient()
	client.SetCookieJar(nil)
	req := client.R()
	req.SetContext(ctx)
	req.SetHeader("accept", "application/json, text/plain, */*")
	req.SetHeader("origin", "https://www.doubao.com")
	req.SetHeader("referer", "https://www.doubao.com/")
	req.SetHeader("rpc-persist-doubao-pan", "true")
	req.SetHeader("content-type", "application/octet-stream")
	req.SetHeader("x-block-list-checksum", checksumHeader)
	req.SetHeader("x-seq-list", seqHeader)
	req.SetHeader("x-block-origin-size", strconv.FormatInt(blockOriginSize, 10))
	req.SetHeader("x-command", "space.api.box.stream.upload.merge_block")
	req.SetHeader("x-csrftoken", "")
	reqID := ""
	if buf := make([]byte, 16); true {
		if _, err := rand.Read(buf); err == nil {
			reqID = hex.EncodeToString(buf)
		}
	}
	if reqID != "" {
		req.SetHeader("x-request-id", reqID)
	}
	if auth := d.resolveAuthorization(); auth != "" {
		req.SetHeader("authorization", auth)
	}
	if dpop := d.resolveDpop(); dpop != "" {
		req.SetHeader("dpop", dpop)
	}
	req.Header.Del("cookie")
	if req.Header.Get("x-command") == "" {
		return UploadMergeData{}, fmt.Errorf("[doubao_new] merge blocks missing x-command header")
	}
	req.SetBody(data)

	values := url.Values{}
	values.Set("shouldBypassScsDialog", "true")
	values.Set("upload_id", uploadID)
	values.Set("mount_point", "explorer")
	values.Set("doubao_storage", "imagex_other")
	values.Set("doubao_app_id", "497858")
	urlStr := "https://internal-api-drive-stream.feishu.cn/space/api/box/stream/upload/merge_block/?" + values.Encode()

	res, err := req.Execute(http.MethodPost, urlStr)
	if err != nil {
		return UploadMergeData{}, err
	}
	if v := res.Header().Get("X-Tt-Logid"); v != "" {
		d.TtLogid = v
	} else if v := res.Header().Get("x-tt-logid"); v != "" {
		d.TtLogid = v
	}
	body := res.Body()
	var resp UploadMergeResp
	if err := json.Unmarshal(body, &resp); err != nil {
		msg := fmt.Sprintf("[doubao_new] decode response failed (status: %s, content-type: %s, body: %s): %v",
			res.Status(),
			res.Header().Get("Content-Type"),
			string(body),
			err,
		)
		return UploadMergeData{}, fmt.Errorf("%s", msg)
	}
	if resp.Code != 0 {
		if res != nil && res.StatusCode() == http.StatusBadRequest && resp.Code == 2 {
			success := make([]int, 0, len(seqList))
			offset := 0
			for i, seq := range seqList {
				size := sizeList[i]
				if size <= 0 {
					return UploadMergeData{SuccessSeqList: success}, fmt.Errorf("[doubao_new] v3 fallback invalid size: seq=%d size=%d", seq, size)
				}
				if offset+int(size) > len(data) {
					return UploadMergeData{SuccessSeqList: success}, fmt.Errorf("[doubao_new] v3 fallback payload out of range: seq=%d offset=%d size=%d total=%d", seq, offset, size, len(data))
				}
				payload := data[offset : offset+int(size)]
				block := UploadBlockNeed{
					Seq:      seq,
					Size:     size,
					Checksum: checksumList[i],
				}
				if err := d.uploadBlockV3(ctx, uploadID, block, payload); err != nil {
					return UploadMergeData{SuccessSeqList: success}, err
				}
				success = append(success, seq)
				offset += int(size)
			}
			return UploadMergeData{SuccessSeqList: success}, nil
		}
		errMsg := resp.Msg
		if errMsg == "" {
			errMsg = resp.Message
		}
		return UploadMergeData{}, fmt.Errorf("[doubao_new] API error (code: %d): %s", resp.Code, errMsg)
	}

	return resp.Data, nil
}

func (d *DoubaoNew) uploadBlockV3(ctx context.Context, uploadID string, block UploadBlockNeed, data []byte) error {
	if uploadID == "" {
		return fmt.Errorf("[doubao_new] upload v3 block missing upload_id")
	}
	if block.Seq < 0 {
		return fmt.Errorf("[doubao_new] upload v3 block invalid seq")
	}
	if len(data) == 0 {
		return fmt.Errorf("[doubao_new] upload v3 block empty data")
	}

	req := base.RestyClient.R()
	req.SetContext(ctx)
	req.SetHeader("accept", "*/*")
	req.SetHeader("origin", "https://www.doubao.com")
	req.SetHeader("referer", "https://www.doubao.com/")
	req.SetHeader("rpc-persist-doubao-pan", "true")
	req.SetHeader("x-block-seq", strconv.Itoa(block.Seq))
	req.SetHeader("x-block-checksum", block.Checksum)
	if auth := d.resolveAuthorization(); auth != "" {
		req.SetHeader("authorization", auth)
	}
	if dpop := d.resolveDpop(); dpop != "" {
		req.SetHeader("dpop", dpop)
	}

	req.SetMultipartFormData(map[string]string{
		"upload_id": uploadID,
		"size":      strconv.FormatInt(int64(len(data)), 10),
	})
	req.SetMultipartField("file", "blob", "application/octet-stream", bytes.NewReader(data))

	values := url.Values{}
	values.Set("shouldBypassScsDialog", "true")
	values.Set("upload_id", uploadID)
	values.Set("seq", strconv.Itoa(block.Seq))
	values.Set("size", strconv.FormatInt(int64(len(data)), 10))
	values.Set("checksum", block.Checksum)
	values.Set("mount_point", "explorer")
	values.Set("doubao_storage", "imagex_other")
	values.Set("doubao_app_id", "497858")
	urlStr := "https://internal-api-drive-stream.feishu.cn/space/api/box/stream/upload/v3/block/?" + values.Encode()

	res, err := req.Execute(http.MethodPost, urlStr)
	if err != nil {
		return err
	}
	body := res.Body()
	if err := decodeBaseResp(body, res); err != nil {
		return err
	}
	return nil
}

func (d *DoubaoNew) finishUpload(ctx context.Context, uploadID string, numBlocks int, mountPoint string) (UploadFinishData, error) {
	if uploadID == "" {
		return UploadFinishData{}, fmt.Errorf("[doubao_new] finish upload missing upload_id")
	}
	if numBlocks <= 0 {
		return UploadFinishData{}, fmt.Errorf("[doubao_new] finish upload invalid num_blocks")
	}
	if mountPoint == "" {
		mountPoint = "explorer"
	}
	var resp UploadFinishResp
	_, err := d.request(ctx, "/space/api/box/upload/finish/", http.MethodPost, func(req *resty.Request) {
		values := url.Values{}
		values.Set("shouldBypassScsDialog", "true")
		values.Set("doubao_storage", "imagex_other")
		values.Set("doubao_app_id", "497858")
		req.SetQueryParamsFromValues(values)
		req.SetHeader("Content-Type", "application/json")
		req.SetHeader("x-command", "space.api.box.upload.finish")
		req.SetHeader("rpc-persist-doubao-pan", "true")
		req.SetHeader("cache-control", "no-cache")
		req.SetHeader("pragma", "no-cache")
		req.SetHeader("biz-scene", "file_upload")
		req.SetHeader("biz-ua-type", "Web")
		req.SetBody(base.Json{
			"upload_id":                uploadID,
			"num_blocks":               numBlocks,
			"mount_point":              mountPoint,
			"push_open_history_record": 1,
		})
	}, &resp)
	if err != nil {
		return UploadFinishData{}, err
	}
	return resp.Data, nil
}
