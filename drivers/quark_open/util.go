package quark_open

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/google/uuid"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/go-resty/resty/v2"
	log "github.com/sirupsen/logrus"
)

func (d *QuarkOpen) request(ctx context.Context, pathname string, method string, callback base.ReqCallback, resp interface{}, manualSign ...*ManualSign) ([]byte, error) {
	u := d.conf.api + pathname

	var tm, token, reqID string

	// 检查是否手动传入签名参数
	if len(manualSign) > 0 && manualSign[0] != nil {
		tm = manualSign[0].Tm
		token = manualSign[0].Token
		reqID = manualSign[0].ReqID
	} else {
		// 自动生成签名参数
		tm, token, reqID = d.generateReqSign(method, pathname, d.Addition.SignKey)
	}

	req := base.RestyClient.R()
	req.SetContext(ctx)
	req.SetHeaders(map[string]string{
		"Accept":          "application/json, text/plain, */*",
		"User-Agent":      d.conf.ua,
		"x-pan-tm":        tm,
		"x-pan-token":     token,
		"x-pan-client-id": d.Addition.AppID,
	})
	req.SetQueryParams(map[string]string{
		"req_id":       reqID,
		"access_token": d.Addition.AccessToken,
	})
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e Resp
	req.SetError(&e)
	res, err := req.Execute(method, u)
	if err != nil {
		return nil, err
	}
	// 判断 是否需要 刷新 access_token
	if e.Status == -1 && (e.Errno == 11001 || (e.Errno == 14001 && strings.Contains(e.ErrorInfo, "access_token"))) {
		// token 过期
		err = d.refreshToken()
		if err != nil {
			return nil, err
		}
		ctx1, cancelFunc := context.WithTimeout(ctx, 10*time.Second)
		defer cancelFunc()
		return d.request(ctx1, pathname, method, callback, resp)
	}

	if e.Status >= 400 || e.Errno != 0 {
		return nil, errors.New(e.ErrorInfo)
	}

	return res.Body(), nil
}

func (d *QuarkOpen) GetFiles(ctx context.Context, parent string) ([]File, error) {
	files := make([]File, 0)
	var queryCursor QueryCursor

	for {
		reqBody := map[string]interface{}{
			"parent_fid": parent,
			"size":       100,             // 默认每页100个文件
			"sort":       "file_name:asc", // 基本排序方式
		}
		// 如果有排序设置
		if d.OrderBy != "none" {
			reqBody["sort"] = d.OrderBy + ":" + d.OrderDirection
		}
		// 设置查询游标（用于分页）
		if queryCursor.Token != "" {
			reqBody["query_cursor"] = queryCursor
		}

		var resp FileListResp
		_, err := d.request(ctx, "/open/v1/file/list", http.MethodPost, func(req *resty.Request) {
			req.SetBody(reqBody)
		}, &resp)

		if err != nil {
			return nil, err
		}

		files = append(files, resp.Data.FileList...)
		if resp.Data.LastPage {
			break
		}

		queryCursor = resp.Data.NextQueryCursor
	}

	return files, nil
}

func (d *QuarkOpen) upPre(ctx context.Context, file model.FileStreamer, parentId, md5, sha1 string) (UpPreResp, error) {
	// 获取当前时间
	now := time.Now()
	// 获取文件大小
	fileSize := file.GetSize()

	// 手动生成 x-pan-token
	httpMethod := "POST"
	apiPath := "/open/v1/file/upload_pre"
	tm, xPanToken, reqID := d.generateReqSign(httpMethod, apiPath, d.Addition.SignKey)

	// 生成proof相关字段，传入 x-pan-token
	proofVersion, proofSeed1, proofSeed2, proofCode1, proofCode2, err := d.generateProof(file, xPanToken)
	if err != nil {
		return UpPreResp{}, fmt.Errorf("failed to generate proof: %w", err)
	}

	data := base.Json{
		"file_name":       file.GetName(),
		"size":            fileSize,
		"format_type":     file.GetMimetype(),
		"md5":             md5,
		"sha1":            sha1,
		"l_created_at":    now.UnixMilli(),
		"l_updated_at":    now.UnixMilli(),
		"pdir_fid":        parentId,
		"same_path_reuse": true,
		"proof_version":   proofVersion,
		"proof_seed1":     proofSeed1,
		"proof_seed2":     proofSeed2,
		"proof_code1":     proofCode1,
		"proof_code2":     proofCode2,
	}

	var resp UpPreResp

	// 使用手动生成的签名参数
	manualSign := &ManualSign{
		Tm:    tm,
		Token: xPanToken,
		ReqID: reqID,
	}

	_, err = d.request(ctx, "/open/v1/file/upload_pre", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp, manualSign)

	return resp, err
}

// generateProof 生成夸克云盘文件上传的proof验证信息
func (d *QuarkOpen) generateProof(file model.FileStreamer, xPanToken string) (proofVersion, proofSeed1, proofSeed2, proofCode1, proofCode2 string, err error) {
	// 获取文件大小
	fileSize := file.GetSize()
	// 设置proof_version (固定为"v1")
	proofVersion = "v1"
	// 生成proof_seed1 - 算法: md5(userid+x-pan-token)
	proofSeed1 = d.generateProofSeed1(xPanToken)
	// 生成proof_seed2 - 算法: md5(fileSize)
	proofSeed2 = d.generateProofSeed2(fileSize)
	// 生成proof_code1和proof_code2
	proofCode1, err = d.generateProofCode(file, proofSeed1, fileSize)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to generate proof_code1: %w", err)
	}

	proofCode2, err = d.generateProofCode(file, proofSeed2, fileSize)
	if err != nil {
		return "", "", "", "", "", fmt.Errorf("failed to generate proof_code2: %w", err)
	}

	return proofVersion, proofSeed1, proofSeed2, proofCode1, proofCode2, nil
}

// generateProofSeed1 生成proof_seed1，基于 userId、x-pan-token
func (d *QuarkOpen) generateProofSeed1(xPanToken string) string {
	concatString := d.conf.userId + xPanToken
	md5Hash := md5.Sum([]byte(concatString))
	return hex.EncodeToString(md5Hash[:])
}

// generateProofSeed2 生成proof_seed2，基于 fileSize
func (d *QuarkOpen) generateProofSeed2(fileSize int64) string {
	md5Hash := md5.Sum([]byte(strconv.FormatInt(fileSize, 10)))
	return hex.EncodeToString(md5Hash[:])
}

type ProofRange struct {
	Start int64
	End   int64
}

// generateProofCode 根据proof_seed和文件大小生成proof_code
func (d *QuarkOpen) generateProofCode(file model.FileStreamer, proofSeed string, fileSize int64) (string, error) {
	// 获取读取范围
	proofRange, err := d.getProofRange(proofSeed, fileSize)
	if err != nil {
		return "", fmt.Errorf("failed to get proof range: %w", err)
	}

	// 计算需要读取的长度
	length := proofRange.End - proofRange.Start
	if length == 0 {
		return "", nil
	}

	// 使用FileStreamer的RangeRead方法读取特定范围的数据
	reader, err := file.RangeRead(http_range.Range{
		Start:  proofRange.Start,
		Length: length,
	})
	if err != nil {
		return "", fmt.Errorf("failed to range read: %w", err)
	}
	defer func() {
		if closer, ok := reader.(io.Closer); ok {
			closer.Close()
		}
	}()

	// 读取数据
	buf := make([]byte, length)
	n, err := io.ReadFull(reader, buf)
	if n != int(length) {
		return "", fmt.Errorf("failed to read all data: (expect =%d, actual =%d) %w", length, n, err)
	}

	// Base64编码
	return base64.StdEncoding.EncodeToString(buf), nil
}

// getProofRange 根据proof_seed和文件大小计算需要读取的文件范围
func (d *QuarkOpen) getProofRange(proofSeed string, fileSize int64) (*ProofRange, error) {
	if fileSize == 0 {
		return &ProofRange{}, nil
	}
	// 对 proofSeed 进行 MD5 处理，取前16个字符
	md5Hash := md5.Sum([]byte(proofSeed))
	tmpStr := hex.EncodeToString(md5Hash[:])[:16]
	// 转为 uint64
	tmpInt, err := strconv.ParseUint(tmpStr, 16, 64)
	if err != nil {
		return nil, fmt.Errorf("failed to parse hex string: %w", err)
	}
	// 计算索引位置
	index := tmpInt % uint64(fileSize)

	pr := &ProofRange{
		Start: int64(index),
		End:   int64(index) + 8,
	}
	// 确保 End 不超过文件大小
	if pr.End > fileSize {
		pr.End = fileSize
	}

	return pr, nil
}

func (d *QuarkOpen) _getPartInfo(stream model.FileStreamer, partSize int64) []base.Json {
	// 计算分片信息
	partInfo := make([]base.Json, 0)
	total := stream.GetSize()
	left := total
	partNumber := 1

	// 计算每个分片的大小和编号
	for left > 0 {
		size := partSize
		if left < partSize {
			size = left
		}

		partInfo = append(partInfo, base.Json{
			"part_number": partNumber,
			"part_size":   size,
		})

		left -= size
		partNumber++
	}

	return partInfo
}

func (d *QuarkOpen) upUrl(ctx context.Context, pre UpPreResp, partInfo []base.Json) (upUrlInfo UpUrlInfo, err error) {
	// 构建请求体
	data := base.Json{
		"task_id":        pre.Data.TaskID,
		"part_info_list": partInfo,
	}
	var resp UpUrlResp

	_, err = d.request(ctx, "/open/v1/file/get_upload_urls", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)

	if err != nil {
		return upUrlInfo, err
	}

	return resp.Data, nil

}

func (d *QuarkOpen) upPart(ctx context.Context, upUrlInfo UpUrlInfo, partNumber int, bytes io.Reader) (string, error) {
	// 创建请求
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, upUrlInfo.UploadUrls[partNumber].UploadURL, bytes)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", upUrlInfo.UploadUrls[partNumber].SignatureInfo.Signature)
	req.Header.Set("X-Oss-Date", upUrlInfo.CommonHeaders.XOssDate)
	req.Header.Set("X-Oss-Content-Sha256", upUrlInfo.CommonHeaders.XOssContentSha256)
	req.Header.Set("Accept-Encoding", "gzip")
	req.Header.Set("User-Agent", "Go-http-client/1.1")

	// 发送请求
	resp, err := base.HttpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("up status: %d, error: %s", resp.StatusCode, string(body))
	}
	// 返回 Etag 作为分片上传的标识
	return resp.Header.Get("Etag"), nil
}

func (d *QuarkOpen) upFinish(ctx context.Context, pre UpPreResp, partInfo []base.Json, etags []string) error {
	// 创建 part_info_list
	partInfoList := make([]base.Json, len(partInfo))
	// 确保 partInfo 和 etags 长度一致
	if len(partInfo) != len(etags) {
		return fmt.Errorf("part info count (%d) does not match etags count (%d)", len(partInfo), len(etags))
	}
	// 组合 part_info_list
	for i, part := range partInfo {
		partInfoList[i] = base.Json{
			"part_number": part["part_number"],
			"part_size":   part["part_size"],
			"etag":        etags[i],
		}
	}
	// 构建请求体
	data := base.Json{
		"task_id":        pre.Data.TaskID,
		"part_info_list": partInfoList,
	}

	// 发送请求
	var resp UpFinishResp
	_, err := d.request(ctx, "/open/v1/file/upload_finish", http.MethodPost, func(req *resty.Request) {
		req.SetBody(data)
	}, &resp)

	if err != nil {
		return err
	}

	if resp.Data.Finish != true {
		return fmt.Errorf("upload finish failed, task_id: %s", resp.Data.TaskID)
	}

	return nil
}

// ManualSign 用于手动签名URL的结构体
type ManualSign struct {
	Tm    string
	Token string
	ReqID string
}

func (d *QuarkOpen) generateReqSign(method string, pathname string, signKey string) (string, string, string) {
	// 生成时间戳 (13位毫秒级)
	timestamp := strconv.FormatInt(time.Now().UnixNano()/int64(time.Millisecond), 10)

	// 生成 x-pan-token token的组成是: method + "&" + pathname + "&" + timestamp + "&" + signKey
	tokenData := method + "&" + pathname + "&" + timestamp + "&" + signKey
	tokenHash := sha256.Sum256([]byte(tokenData))
	xPanToken := hex.EncodeToString(tokenHash[:])

	// 生成 req_id
	reqUuid, _ := uuid.NewRandom()
	reqID := reqUuid.String()

	return timestamp, xPanToken, reqID
}

func (d *QuarkOpen) refreshToken() error {
	refresh, access, err := d._refreshToken()
	for i := 0; i < 3; i++ {
		if err == nil {
			break
		} else {
			log.Errorf("[quark_open] failed to refresh token: %s", err)
		}
		refresh, access, err = d._refreshToken()
	}
	if err != nil {
		return err
	}
	log.Infof("[quark_open] token exchange: %s -> %s", d.RefreshToken, refresh)
	d.RefreshToken, d.AccessToken = refresh, access
	op.MustSaveDriverStorage(d)
	return nil
}

func (d *QuarkOpen) _refreshToken() (string, string, error) {
	if d.UseOnlineAPI && d.APIAddress != "" {
		u := d.APIAddress
		var resp RefreshTokenOnlineAPIResp
		_, err := base.RestyClient.R().
			SetResult(&resp).
			SetQueryParams(map[string]string{
				"refresh_ui": d.RefreshToken,
				"server_use": "true",
				"driver_txt": "quarkyun_oa",
			}).
			Get(u)
		if err != nil {
			return "", "", err
		}
		if resp.RefreshToken == "" || resp.AccessToken == "" {
			if resp.ErrorMessage != "" {
				return "", "", fmt.Errorf("failed to refresh token: %s", resp.ErrorMessage)
			}
			return "", "", fmt.Errorf("empty token returned from official API, a wrong refresh token may have been used")
		}
		return resp.RefreshToken, resp.AccessToken, nil
	}

	// TODO 本地刷新逻辑
	return "", "", fmt.Errorf("local refresh token logic is not implemented yet, please use online API or contact the developer")
}

// 生成认证 Cookie
func (d *QuarkOpen) generateAuthCookie() string {
	return fmt.Sprintf("x_pan_client_id=%s; x_pan_access_token=%s",
		d.Addition.AppID, d.Addition.AccessToken)
}
