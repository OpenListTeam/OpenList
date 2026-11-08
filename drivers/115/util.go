package _115

import (
	"bytes"
	"context"
	"crypto/md5"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	netutil "github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	cipher "github.com/SheltonZhu/115driver/pkg/crypto/ec115"
	driver115 "github.com/SheltonZhu/115driver/pkg/driver"
	"github.com/aliyun/aliyun-oss-go-sdk/oss"
	"github.com/pkg/errors"
)

// var UserAgent = driver115.UA115Browser
func (d *Pan115) login() error {
	var err error
	opts := []driver115.Option{
		driver115.UA(d.getUA()),
		func(c *driver115.Pan115Client) {
			c.Client.SetTLSClientConfig(&tls.Config{InsecureSkipVerify: conf.Conf.TlsInsecureSkipVerify})
		},
	}
	d.client = driver115.New(opts...)
	cr := &driver115.Credential{}
	if d.QRCodeToken != "" {
		s := &driver115.QRCodeSession{
			UID: d.QRCodeToken,
		}
		if cr, err = d.client.QRCodeLoginWithApp(s, driver115.LoginApp(d.QRCodeSource)); err != nil {
			return errors.Wrap(err, "failed to login by qrcode")
		}
		d.Cookie = fmt.Sprintf("UID=%s;CID=%s;SEID=%s;KID=%s", cr.UID, cr.CID, cr.SEID, cr.KID)
		d.QRCodeToken = ""
	} else if d.Cookie != "" {
		if err = cr.FromCookie(d.Cookie); err != nil {
			return errors.Wrap(err, "failed to login by cookies")
		}
		d.client.ImportCredential(cr)
	} else {
		return errors.New("missing cookie or qrcode account")
	}
	return d.client.LoginCheck()
}

func (d *Pan115) getFiles(fileId string) ([]FileObj, error) {
	res := make([]FileObj, 0)
	if d.PageSize <= 0 {
		d.PageSize = driver115.FileListLimit
	}
	files, err := d.client.ListWithLimit(fileId, d.PageSize, driver115.WithMultiUrls())
	if err != nil {
		return nil, err
	}
	for _, file := range *files {
		res = append(res, FileObj{file})
	}
	return res, nil
}

func (d *Pan115) getNewFile(fileId string) (*FileObj, error) {
	file, err := d.client.GetFile(fileId)
	if err != nil {
		return nil, err
	}
	return &FileObj{*file}, nil
}

func (d *Pan115) getNewFileByPickCode(pickCode string) (*FileObj, error) {
	result := driver115.GetFileInfoResponse{}
	req := d.client.NewRequest().
		SetQueryParam("pick_code", pickCode).
		ForceContentType("application/json;charset=UTF-8").
		SetResult(&result)
	resp, err := req.Get(driver115.ApiFileInfo)
	if err := driver115.CheckErr(err, &result, resp); err != nil {
		return nil, err
	}
	if len(result.Files) == 0 {
		return nil, errors.New("not get file info")
	}
	fileInfo := result.Files[0]

	f := &FileObj{}
	f.From(fileInfo)
	return f, nil
}

func (d *Pan115) getUA() string {
	return fmt.Sprintf("Mozilla/5.0 115Browser/%s", appVer)
}

func (c *Pan115) GenerateToken(fileID, preID, timeStamp, fileSize, signKey, signVal string) string {
	userID := strconv.FormatInt(c.client.UserID, 10)
	userIDMd5 := md5.Sum([]byte(userID))
	tokenMd5 := md5.Sum([]byte(md5Salt + fileID + fileSize + signKey + signVal + userID + timeStamp + hex.EncodeToString(userIDMd5[:]) + appVer))
	return hex.EncodeToString(tokenMd5[:])
}

func (d *Pan115) rapidUpload(fileSize int64, fileName, dirID, preID, fileID string, stream model.FileStreamer) (*driver115.UploadInitResp, error) {
	var (
		ecdhCipher   *cipher.EcdhCipher
		encrypted    []byte
		decrypted    []byte
		encodedToken string
		err          error
		target       = "U_1_" + dirID
		bodyBytes    []byte
		result       = driver115.UploadInitResp{}
		fileSizeStr  = strconv.FormatInt(fileSize, 10)
	)
	if ecdhCipher, err = cipher.NewEcdhCipher(); err != nil {
		return nil, err
	}

	userID := strconv.FormatInt(d.client.UserID, 10)
	form := url.Values{}
	form.Set("appid", "0")
	form.Set("appversion", appVer)
	form.Set("userid", userID)
	form.Set("filename", fileName)
	form.Set("filesize", fileSizeStr)
	form.Set("fileid", fileID)
	form.Set("target", target)
	form.Set("sig", d.client.GenerateSignature(fileID, target))

	signKey, signVal := "", ""
	decryptRetryCount := 0
	for retry := true; retry; {
		t := driver115.NowMilli()

		if encodedToken, err = ecdhCipher.EncodeToken(t.ToInt64()); err != nil {
			return nil, err
		}

		params := map[string]string{
			"k_ec": encodedToken,
		}

		form.Set("t", t.String())
		form.Set("token", d.GenerateToken(fileID, preID, t.String(), fileSizeStr, signKey, signVal))
		if signKey != "" && signVal != "" {
			form.Set("sign_key", signKey)
			form.Set("sign_val", signVal)
		}
		if encrypted, err = ecdhCipher.Encrypt([]byte(form.Encode())); err != nil {
			return nil, err
		}

		req := d.client.NewRequest().
			SetQueryParams(params).
			SetBody(encrypted).
			SetHeaderVerbatim("Content-Type", "application/x-www-form-urlencoded").
			SetDoNotParseResponse(true)
		resp, err := req.Post(driver115.ApiUploadInit)
		if err != nil {
			return nil, err
		}
		data := resp.RawBody()
		if bodyBytes, err = io.ReadAll(data); err != nil {
			_ = data.Close()
			return nil, err
		}
		_ = data.Close()
		if decrypted, err = ecdhCipher.Decrypt(bodyBytes); err != nil {
			decryptRetryCount++
			if decryptRetryCount < 3 {
				continue
			}
			return nil, err
		}
		if err = driver115.CheckErr(json.Unmarshal(decrypted, &result), &result, resp); err != nil {
			return nil, err
		}
		if result.Status == 7 {
			// Update signKey & signVal
			signKey = result.SignKey
			signVal, err = UploadDigestRange(stream, result.SignCheck)
			if err != nil {
				return nil, err
			}
		} else {
			retry = false
		}
		result.SHA1 = fileID
	}

	return &result, nil
}

func UploadDigestRange(stream model.FileStreamer, rangeSpec string) (result string, err error) {
	var start, end int64
	if _, err = fmt.Sscanf(rangeSpec, "%d-%d", &start, &end); err != nil {
		return
	}

	length := end - start + 1
	reader, err := stream.RangeRead(http_range.Range{Start: start, Length: length})
	if err != nil {
		return "", err
	}
	hashStr, err := utils.HashReader(utils.SHA1, reader)
	if err != nil {
		return "", err
	}
	result = strings.ToUpper(hashStr)
	return
}

// UploadByOSS use aliyun sdk to upload
func (c *Pan115) UploadByOSS(ctx context.Context, params *driver115.UploadOSSParams, s model.FileStreamer, dirID string, up driver.UpdateProgress) (*UploadResult, error) {
	ossToken, err := c.client.GetOSSToken()
	if err != nil {
		return nil, err
	}
	ossClient, err := netutil.NewOSSClient(driver115.OSSEndpoint, ossToken.AccessKeyID, ossToken.AccessKeySecret)
	if err != nil {
		return nil, err
	}
	bucket, err := ossClient.Bucket(params.Bucket)
	if err != nil {
		return nil, err
	}

	var bodyBytes []byte
	r := driver.NewLimitedUploadStream(ctx, &driver.ReaderUpdatingProgress{
		Reader:         s,
		UpdateProgress: up,
	})
	if err = bucket.PutObject(params.Object, r, append(
		driver115.OssOption(params, ossToken),
		oss.CallbackResult(&bodyBytes),
	)...); err != nil {
		return nil, err
	}

	var uploadResult UploadResult
	if err = json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return nil, err
	}
	return &uploadResult, uploadResult.Err(string(bodyBytes))
}

// UploadByMultipart upload by mutipart blocks
func (d *Pan115) UploadByMultipart(ctx context.Context, params *driver115.UploadOSSParams, fileSize int64, s model.FileStreamer,
	dirID string, up driver.UpdateProgress, opts ...driver115.UploadMultipartOption,
) (*UploadResult, error) {
	var (
		chunks    []oss.FileChunk
		parts     []oss.UploadPart
		imur      oss.InitiateMultipartUploadResult
		ossClient *oss.Client
		bucket    *oss.Bucket
		ossToken  *driver115.UploadOSSTokenResp
		bodyBytes []byte
		err       error
	)

	tmpF, err := s.CacheFullAndWriter(&up, nil)
	if err != nil {
		return nil, err
	}

	options := driver115.DefalutUploadMultipartOptions()
	if len(opts) > 0 {
		for _, f := range opts {
			f(options)
		}
	}
	// oss 启用Sequential必须按顺序上传
	options.ThreadsNum = 1

	if ossToken, err = d.client.GetOSSToken(); err != nil {
		return nil, err
	}

	if ossClient, err = netutil.NewOSSClient(driver115.OSSEndpoint, ossToken.AccessKeyID, ossToken.AccessKeySecret, oss.EnableMD5(true), oss.EnableCRC(true)); err != nil {
		return nil, err
	}

	if bucket, err = ossClient.Bucket(params.Bucket); err != nil {
		return nil, err
	}

	// ossToken一小时后就会失效，所以每50分钟重新获取一次
	ticker := time.NewTicker(options.TokenRefreshTime)
	defer ticker.Stop()
	// 设置超时
	timeout := time.NewTimer(options.Timeout)
	uploadCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer timeout.Stop()

	if chunks, err = SplitFile(fileSize); err != nil {
		return nil, err
	}

	if imur, err = bucket.InitiateMultipartUpload(params.Object,
		oss.SetHeader(driver115.OssSecurityTokenHeaderName, ossToken.SecurityToken),
		oss.UserAgentHeader(driver115.OSSUserAgent),
		oss.EnableSha1(), oss.Sequential(), oss.WithContext(uploadCtx),
	); err != nil {
		return nil, err
	}

	chunksCh := make(chan oss.FileChunk)
	errCh := make(chan error, 1)
	uploadedPartsCh := make(chan oss.UploadPart)
	producerDone := make(chan struct{})
	workersDone := make(chan struct{})
	partsDone := make(chan struct{})
	workerWG := sync.WaitGroup{}
	workerWG.Add(options.ThreadsNum)
	var tokenMu sync.RWMutex

	reportErr := func(e error) {
		if e == nil {
			return
		}
		select {
		case errCh <- e:
		default:
		}
		cancel()
	}

	// producer
	go func() {
		defer close(producerDone)
		defer close(chunksCh)
		for _, chunk := range chunks {
			select {
			case chunksCh <- chunk:
			case <-uploadCtx.Done():
				return
			}
		}
	}()

	completedNum := atomic.Int32{}
	// consumers
	for i := 0; i < options.ThreadsNum; i++ {
		go func() {
			defer workerWG.Done()
			defer func() {
				if r := recover(); r != nil {
					reportErr(fmt.Errorf("recovered in %v", r))
				}
			}()
			for {
				var chunk oss.FileChunk
				var ok bool
				select {
				case chunk, ok = <-chunksCh:
					if !ok {
						return
					}
				case <-uploadCtx.Done():
					return
				}

				var part oss.UploadPart
				var partErr error
				for retry := 0; retry < 3; retry++ {
					if uploadCtx.Err() != nil {
						return
					}
					buf := make([]byte, chunk.Size)
					if _, partErr = tmpF.ReadAt(buf, chunk.Offset); partErr != nil && !errors.Is(partErr, io.EOF) {
						continue
					}
					tokenMu.RLock()
					currentToken := ossToken
					tokenMu.RUnlock()
					if part, partErr = bucket.UploadPart(imur, driver.NewLimitedUploadStream(uploadCtx, bytes.NewReader(buf)),
						chunk.Size, chunk.Number, append(driver115.OssOption(params, currentToken), oss.WithContext(uploadCtx))...); partErr == nil {
						break
					}
				}
				if partErr != nil {
					reportErr(errors.Wrap(partErr, fmt.Sprintf("上传 %s 的第%d个分片时出现错误：%v", s.GetName(), chunk.Number, partErr)))
					return
				}
				num := completedNum.Add(1)
				up(float64(num) * 100.0 / float64(len(chunks)))
				select {
				case uploadedPartsCh <- part:
				case <-uploadCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		workerWG.Wait()
		close(uploadedPartsCh)
		close(workersDone)
	}()
	go func() {
		defer close(partsDone)
		for part := range uploadedPartsCh {
			parts = append(parts, part)
		}
	}()

	var uploadErr error
LOOP:
	for {
		select {
		case uploadErr = <-errCh:
			break LOOP
		case <-ctx.Done():
			select {
			case uploadErr = <-errCh:
			default:
				uploadErr = ctx.Err()
			}
			cancel()
			break LOOP
		case <-ticker.C:
			// 到时重新获取ossToken
			currentToken, tokenErr := d.client.GetOSSToken()
			if tokenErr != nil {
				select {
				case uploadErr = <-errCh:
				default:
					uploadErr = tokenErr
				}
				cancel()
				break LOOP
			}
			tokenMu.Lock()
			ossToken = currentToken
			tokenMu.Unlock()
		case <-timeout.C:
			select {
			case uploadErr = <-errCh:
			default:
				uploadErr = fmt.Errorf("time out")
			}
			cancel()
			break LOOP
		case <-workersDone:
			select {
			case uploadErr = <-errCh:
			default:
			}
			if uploadErr == nil && ctx.Err() != nil {
				uploadErr = ctx.Err()
			}
			break LOOP
		}
	}
	if uploadErr != nil {
		cancel()
	}
	<-workersDone
	<-producerDone
	<-partsDone
	tokenMu.RLock()
	currentToken := ossToken
	tokenMu.RUnlock()
	if uploadErr != nil {
		abortCtx, abortCancel := context.WithTimeout(context.Background(), time.Minute)
		_ = bucket.AbortMultipartUpload(imur,
			oss.SetHeader(driver115.OssSecurityTokenHeaderName, currentToken.SecurityToken),
			oss.WithContext(abortCtx))
		abortCancel()
		return nil, uploadErr
	}

	// 不知道啥原因，oss那边分片上传不计算sha1，导致115服务器校验错误
	// params.Callback.Callback = strings.ReplaceAll(params.Callback.Callback, "${sha1}", params.SHA1)
	if _, err := bucket.CompleteMultipartUpload(imur, parts, append(
		driver115.OssOption(params, currentToken),
		oss.CallbackResult(&bodyBytes),
		oss.WithContext(uploadCtx),
	)...); err != nil {
		abortCtx, abortCancel := context.WithTimeout(context.Background(), time.Minute)
		_ = bucket.AbortMultipartUpload(imur,
			oss.SetHeader(driver115.OssSecurityTokenHeaderName, currentToken.SecurityToken),
			oss.WithContext(abortCtx))
		abortCancel()
		return nil, err
	}

	var uploadResult UploadResult
	if err = json.Unmarshal(bodyBytes, &uploadResult); err != nil {
		return nil, err
	}
	return &uploadResult, uploadResult.Err(string(bodyBytes))
}

func SplitFile(fileSize int64) (chunks []oss.FileChunk, err error) {
	for i := int64(1); i < 10; i++ {
		if fileSize < i*utils.GB { // 文件大小小于iGB时分为i*1000片
			if chunks, err = SplitFileByPartNum(fileSize, int(i*1000)); err != nil {
				return
			}
			break
		}
	}
	if fileSize > 9*utils.GB { // 文件大小大于9GB时分为10000片
		if chunks, err = SplitFileByPartNum(fileSize, 10000); err != nil {
			return
		}
	}
	// 单个分片大小不能小于100KB
	if chunks[0].Size < 100*utils.KB {
		if chunks, err = SplitFileByPartSize(fileSize, 100*utils.KB); err != nil {
			return
		}
	}
	return
}

// SplitFileByPartNum splits big file into parts by the num of parts.
// Split the file with specified parts count, returns the split result when error is nil.
func SplitFileByPartNum(fileSize int64, chunkNum int) ([]oss.FileChunk, error) {
	if chunkNum <= 0 || chunkNum > 10000 {
		return nil, errors.New("chunkNum invalid")
	}

	if int64(chunkNum) > fileSize {
		return nil, errors.New("oss: chunkNum invalid")
	}

	var chunks []oss.FileChunk
	chunk := oss.FileChunk{}
	chunkN := (int64)(chunkNum)
	for i := int64(0); i < chunkN; i++ {
		chunk.Number = int(i + 1)
		chunk.Offset = i * (fileSize / chunkN)
		if i == chunkN-1 {
			chunk.Size = fileSize/chunkN + fileSize%chunkN
		} else {
			chunk.Size = fileSize / chunkN
		}
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}

// SplitFileByPartSize splits big file into parts by the size of parts.
// Splits the file by the part size. Returns the FileChunk when error is nil.
func SplitFileByPartSize(fileSize int64, chunkSize int64) ([]oss.FileChunk, error) {
	if chunkSize <= 0 {
		return nil, errors.New("chunkSize invalid")
	}

	chunkN := fileSize / chunkSize
	if chunkN >= 10000 {
		return nil, errors.New("Too many parts, please increase part size")
	}

	var chunks []oss.FileChunk
	chunk := oss.FileChunk{}
	for i := int64(0); i < chunkN; i++ {
		chunk.Number = int(i + 1)
		chunk.Offset = i * chunkSize
		chunk.Size = chunkSize
		chunks = append(chunks, chunk)
	}

	if fileSize%chunkSize > 0 {
		chunk.Number = len(chunks) + 1
		chunk.Offset = int64(len(chunks)) * chunkSize
		chunk.Size = fileSize % chunkSize
		chunks = append(chunks, chunk)
	}

	return chunks, nil
}
