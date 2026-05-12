package _189

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	myrand "github.com/OpenListTeam/OpenList/v4/pkg/utils/random"
	"github.com/go-resty/resty/v2"
	jsoniter "github.com/json-iterator/go"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

//func (d *Cloud189) login() error {
//	url := "https://cloud.189.cn/api/portal/loginUrl.action?redirectURL=https%3A%2F%2Fcloud.189.cn%2Fmain.action"
//	b := ""
//	lt := ""
//	ltText := regexp.MustCompile(`lt = "(.+?)"`)
//	var res *resty.Response
//	var err error
//	for i := 0; i < 3; i++ {
//		res, err = d.client.R().Get(url)
//		if err != nil {
//			return err
//		}
//		// 已经登陆
//		if res.RawResponse.Request.URL.String() == "https://cloud.189.cn/web/main" {
//			return nil
//		}
//		b = res.String()
//		ltTextArr := ltText.FindStringSubmatch(b)
//		if len(ltTextArr) > 0 {
//			lt = ltTextArr[1]
//			break
//		} else {
//			<-time.After(time.Second)
//		}
//	}
//	if lt == "" {
//		return fmt.Errorf("get page: %s \nstatus: %d \nrequest url: %s\nredirect url: %s",
//			b, res.StatusCode(), res.RawResponse.Request.URL.String(), res.Header().Get("location"))
//	}
//	captchaToken := regexp.MustCompile(`captchaToken' value='(.+?)'`).FindStringSubmatch(b)[1]
//	returnUrl := regexp.MustCompile(`returnUrl = '(.+?)'`).FindStringSubmatch(b)[1]
//	paramId := regexp.MustCompile(`paramId = "(.+?)"`).FindStringSubmatch(b)[1]
//	//reqId := regexp.MustCompile(`reqId = "(.+?)"`).FindStringSubmatch(b)[1]
//	jRsakey := regexp.MustCompile(`j_rsaKey" value="(\S+)"`).FindStringSubmatch(b)[1]
//	vCodeID := regexp.MustCompile(`picCaptcha\.do\?token\=([A-Za-z0-9\&\=]+)`).FindStringSubmatch(b)[1]
//	vCodeRS := ""
//	if vCodeID != "" {
//		// need ValidateCode
//		log.Debugf("try to identify verification codes")
//		timeStamp := strconv.FormatInt(time.Now().UnixNano()/1e6, 10)
//		u := "https://open.e.189.cn/api/logbox/oauth2/picCaptcha.do?token=" + vCodeID + timeStamp
//		imgRes, err := d.client.R().SetHeaders(map[string]string{
//			"User-Agent":     "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:74.0) Gecko/20100101 Firefox/76.0",
//			"Referer":        "https://open.e.189.cn/api/logbox/oauth2/unifyAccountLogin.do",
//			"Sec-Fetch-Dest": "image",
//			"Sec-Fetch-Mode": "no-cors",
//			"Sec-Fetch-Site": "same-origin",
//		}).Get(u)
//		if err != nil {
//			return err
//		}
//		// Enter the verification code manually
//		//err = message.GetMessenger().WaitSend(message.Message{
//		//	Type:    "image",
//		//	Content: "data:image/png;base64," + base64.StdEncoding.EncodeToString(imgRes.Body()),
//		//}, 10)
//		//if err != nil {
//		//	return err
//		//}
//		//vCodeRS, err = message.GetMessenger().WaitReceive(30)
//		// use ocr api
//		vRes, err := base.RestyClient.R().SetMultipartField(
//			"image", "validateCode.png", "image/png", bytes.NewReader(imgRes.Body())).
//			Post(setting.GetStr(conf.OcrApi))
//		if err != nil {
//			return err
//		}
//		if jsoniter.Get(vRes.Body(), "status").ToInt() != 200 {
//			return errors.New("ocr error:" + jsoniter.Get(vRes.Body(), "msg").ToString())
//		}
//		vCodeRS = jsoniter.Get(vRes.Body(), "result").ToString()
//		log.Debugln("code: ", vCodeRS)
//	}
//	userRsa := RsaEncode([]byte(d.Username), jRsakey, true)
//	passwordRsa := RsaEncode([]byte(d.Password), jRsakey, true)
//	url = "https://open.e.189.cn/api/logbox/oauth2/loginSubmit.do"
//	var loginResp LoginResp
//	res, err = d.client.R().
//		SetHeaders(map[string]string{
//			"lt":         lt,
//			"User-Agent": base.UserAgentNT,
//			"Referer":    "https://open.e.189.cn/",
//			"accept":     "application/json;charset=UTF-8",
//		}).SetFormData(map[string]string{
//		"appKey":       "cloud",
//		"accountType":  "01",
//		"userName":     "{RSA}" + userRsa,
//		"password":     "{RSA}" + passwordRsa,
//		"validateCode": vCodeRS,
//		"captchaToken": captchaToken,
//		"returnUrl":    returnUrl,
//		"mailSuffix":   "@pan.cn",
//		"paramId":      paramId,
//		"clientType":   "10010",
//		"dynamicCheck": "FALSE",
//		"cb_SaveName":  "1",
//		"isOauth2":     "false",
//	}).Post(url)
//	if err != nil {
//		return err
//	}
//	err = utils.Json.Unmarshal(res.Body(), &loginResp)
//	if err != nil {
//		log.Error(err.Error())
//		return err
//	}
//	if loginResp.Result != 0 {
//		return fmt.Errorf(loginResp.Msg)
//	}
//	_, err = d.client.R().Get(loginResp.ToUrl)
//	return err
//}

func (d *Cloud189) request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	var e Error
	req := d.client.R().SetError(&e).
		SetHeader("Accept", "application/json;charset=UTF-8").
		SetQueryParams(map[string]string{
			"noCache": random(),
		})
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	// log.Debug(res.String())
	if e.ErrorCode != "" {
		if e.ErrorCode == "InvalidSessionKey" {
			err = d.newLogin()
			if err != nil {
				return nil, err
			}
			return d.request(url, method, callback, resp)
		}
	}
	if jsoniter.Get(res.Body(), "res_code").ToInt() != 0 {
		err = errors.New(jsoniter.Get(res.Body(), "res_message").ToString())
	}
	return res.Body(), err
}

func (d *Cloud189) getFiles(fileId string) ([]model.Obj, error) {
	res := make([]model.Obj, 0)
	pageNum := 1
	for {
		var resp Files
		_, err := d.request("https://cloud.189.cn/api/open/file/listFiles.action", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(map[string]string{
				//"noCache":    random(),
				"pageSize":   "60",
				"pageNum":    strconv.Itoa(pageNum),
				"mediaType":  "0",
				"folderId":   fileId,
				"iconOption": "5",
				"orderBy":    "lastOpTime", // account.OrderBy
				"descending": "true",       // account.OrderDirection
			})
		}, &resp)
		if err != nil {
			return nil, err
		}
		if resp.FileListAO.Count == 0 {
			break
		}
		for _, folder := range resp.FileListAO.FolderList {
			lastOpTime := utils.MustParseCNTime(folder.LastOpTime)
			res = append(res, &model.Object{
				ID:       strconv.FormatInt(folder.Id, 10),
				Name:     folder.Name,
				Modified: lastOpTime,
				IsFolder: true,
			})
		}
		for _, file := range resp.FileListAO.FileList {
			lastOpTime := utils.MustParseCNTime(file.LastOpTime)
			res = append(res, &model.ObjThumb{
				Object: model.Object{
					ID:       strconv.FormatInt(file.Id, 10),
					Name:     file.Name,
					Modified: lastOpTime,
					Size:     file.Size,
				},
				Thumbnail: model.Thumbnail{Thumbnail: file.Icon.SmallUrl},
			})
		}
		pageNum++
	}
	return res, nil
}

func (d *Cloud189) oldUpload(dstDir model.Obj, file model.FileStreamer) error {
	res, err := d.client.R().SetMultipartFormData(map[string]string{
		"parentId":   dstDir.GetID(),
		"sessionKey": "??",
		"opertype":   "1",
		"fname":      file.GetName(),
	}).SetMultipartField("Filedata", file.GetName(), file.GetMimetype(), file).Post("https://hb02.upload.cloud.189.cn/v1/DCIWebUploadAction")
	if err != nil {
		return err
	}
	if utils.Json.Get(res.Body(), "MD5").ToString() != "" {
		return nil
	}
	log.Debugf(res.String())
	return errors.New(res.String())
}

func (d *Cloud189) getSessionKey() (string, error) {
	resp, err := d.request("https://cloud.189.cn/v2/getUserBriefInfo.action", http.MethodGet, nil, nil)
	if err != nil {
		return "", err
	}
	sessionKey := utils.Json.Get(resp, "sessionKey").ToString()
	return sessionKey, nil
}

func (d *Cloud189) getResKey() (string, string, error) {
	now := time.Now().UnixMilli()
	if d.rsa.Expire > now {
		return d.rsa.PubKey, d.rsa.PkId, nil
	}
	resp, err := d.request("https://cloud.189.cn/api/security/generateRsaKey.action", http.MethodGet, nil, nil)
	if err != nil {
		return "", "", err
	}
	pubKey, pkId := utils.Json.Get(resp, "pubKey").ToString(), utils.Json.Get(resp, "pkId").ToString()
	d.rsa.PubKey, d.rsa.PkId = pubKey, pkId
	d.rsa.Expire = utils.Json.Get(resp, "expire").ToInt64()
	return pubKey, pkId, nil
}

func (d *Cloud189) uploadRequest(uri string, form map[string]string, resp interface{}) ([]byte, error) {
	c := strconv.FormatInt(time.Now().UnixMilli(), 10)
	r := Random("xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx")
	l := Random("xxxxxxxxxxxx4xxxyxxxxxxxxxxxxxxx")
	l = l[0 : 16+int(16*myrand.Rand.Float32())]

	e := qs(form)
	data := AesEncrypt([]byte(e), []byte(l[0:16]))
	h := hex.EncodeToString(data)

	sessionKey := d.sessionKey
	signature := hmacSha1(fmt.Sprintf("SessionKey=%s&Operate=GET&RequestURI=%s&Date=%s&params=%s", sessionKey, uri, c, h), l)

	pubKey, pkId, err := d.getResKey()
	if err != nil {
		return nil, err
	}
	b := RsaEncode([]byte(l), pubKey, false)
	req := d.client.R().SetHeaders(map[string]string{
		"accept":         "application/json;charset=UTF-8",
		"SessionKey":     sessionKey,
		"Signature":      signature,
		"X-Request-Date": c,
		"X-Request-ID":   r,
		"EncryptionText": b,
		"PkId":           pkId,
	})
	if resp != nil {
		req.SetResult(resp)
	}
	res, err := req.Get("https://upload.cloud.189.cn" + uri + "?params=" + h)
	if err != nil {
		return nil, err
	}
	data = res.Body()
	if utils.Json.Get(data, "code").ToString() != "SUCCESS" {
		return nil, errors.New(uri + "---" + jsoniter.Get(data, "msg").ToString())
	}
	return data, nil
}

func (d *Cloud189) newUpload(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) error {
	sessionKey, err := d.getSessionKey()
	if err != nil {
		return err
	}
	d.sessionKey = sessionKey
	const DEFAULT int64 = 10485760
	fileSize := file.GetSize()
	count := int64(math.Ceil(float64(fileSize) / float64(DEFAULT)))

	// 先计算文件完整MD5和分片MD5，用于秒传判断
	fileMd5Hex := file.GetHash().GetHash(utils.MD5)
	sliceMd5Hex := ""
	md5s := make([]string, 0)

	if len(fileMd5Hex) < utils.MD5.Width {
		// 没有MD5，先缓存流并同时计算文件MD5和分片MD5
		fileMd5Hash := md5.New()
		sliceMd5Hash := md5.New()
		var finish int64
		cache, err := file.CacheFullAndWriter(nil, io.MultiWriter(fileMd5Hash, &sliceHashWriter{
			hash:      sliceMd5Hash,
			md5s:      &md5s,
			sliceSize: DEFAULT,
			finish:    &finish,
			fileSize:  fileSize,
			up:        up,
			ctx:       ctx,
		}))
		if err != nil {
			return err
		}
		// 处理最后一个分片的MD5
		if finish%DEFAULT != 0 || finish == 0 {
			md5s = append(md5s, strings.ToUpper(hex.EncodeToString(sliceMd5Hash.Sum(nil))))
		}
		fileMd5Hex = hex.EncodeToString(fileMd5Hash.Sum(nil))

		// seek回起始位置，供后续上传使用
		if _, err := cache.Seek(0, io.SeekStart); err != nil {
			return err
		}
	}

	// 计算sliceMd5
	if fileSize > DEFAULT && len(md5s) > 0 {
		sliceMd5Hex = utils.GetMD5EncodeStr(strings.Join(md5s, "\n"))
	} else {
		sliceMd5Hex = fileMd5Hex
	}

	// 带fileMd5调用initMultiUpload，支持秒传
	initParams := map[string]string{
		"parentFolderId": dstDir.GetID(),
		"fileName":       encode(file.GetName()),
		"fileSize":       strconv.FormatInt(fileSize, 10),
		"sliceSize":      strconv.FormatInt(DEFAULT, 10),
		"fileMd5":        fileMd5Hex,
		"sliceMd5":       sliceMd5Hex,
	}

	res, err := d.uploadRequest("/person/initMultiUpload", initParams, nil)
	if err != nil {
		return err
	}
	uploadFileId := jsoniter.Get(res, "data", "uploadFileId").ToString()
	fileDataExists := jsoniter.Get(res, "data", "fileDataExists").ToInt()

	// 秒传成功，直接提交
	if fileDataExists == 1 {
		_, err = d.uploadRequest("/person/commitMultiUploadFile", map[string]string{
			"uploadFileId": uploadFileId,
			"fileMd5":      fileMd5Hex,
			"sliceMd5":     sliceMd5Hex,
			"lazyCheck":    "1",
			"opertype":     "3",
		}, nil)
		return err
	}

	// 非秒传，需要上传分片
	var finish int64 = 0
	var i int64
	var byteSize int64
	for i = 1; i <= count; i++ {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		byteSize = fileSize - finish
		if DEFAULT < byteSize {
			byteSize = DEFAULT
		}
		byteData := make([]byte, byteSize)
		n, err := io.ReadFull(file, byteData)
		if err != nil {
			return err
		}
		finish += int64(n)
		md5Bytes := getMd5(byteData)
		md5Base64 := base64.StdEncoding.EncodeToString(md5Bytes)
		var resp UploadUrlsResp
		res, err = d.uploadRequest("/person/getMultiUploadUrls", map[string]string{
			"partInfo":     fmt.Sprintf("%s-%s", strconv.FormatInt(i, 10), md5Base64),
			"uploadFileId": uploadFileId,
		}, &resp)
		if err != nil {
			return err
		}
		uploadData := resp.UploadUrls["partNumber_"+strconv.FormatInt(i, 10)]
		log.Debugf("uploadData: %+v", uploadData)
		requestURL := uploadData.RequestURL
		uploadHeaders := strings.Split(decodeURIComponent(uploadData.RequestHeader), "&")
		req, err := http.NewRequestWithContext(ctx, http.MethodPut, requestURL, driver.NewLimitedUploadStream(ctx, bytes.NewReader(byteData)))
		if err != nil {
			return err
		}
		for _, v := range uploadHeaders {
			i := strings.Index(v, "=")
			req.Header.Set(v[0:i], v[i+1:])
		}
		r, err := base.HttpClient.Do(req)
		if err != nil {
			return err
		}
		log.Debugf("%+v %+v", r, r.Request.Header)
		_ = r.Body.Close()
		up(50 + float64(i)*50/float64(count))
	}
	res, err = d.uploadRequest("/person/commitMultiUploadFile", map[string]string{
		"uploadFileId": uploadFileId,
		"fileMd5":      fileMd5Hex,
		"sliceMd5":     sliceMd5Hex,
		"lazyCheck":    "1",
		"opertype":     "3",
	}, nil)
	return err
}

func (d *Cloud189) getCapacityInfo(ctx context.Context) (*CapacityResp, error) {
	var resp CapacityResp
	_, err := d.request("https://cloud.189.cn/api/portal/getUserSizeInfo.action", http.MethodGet, func(req *resty.Request) {
		req.SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}

// sliceHashWriter 在写入过程中按分片大小自动切分并计算每个分片的MD5，
// 同时支持进度回调和取消检查。
type sliceHashWriter struct {
	hash      io.Writer // 当前分片的MD5 hash
	md5s      *[]string // 收集每个分片的MD5十六进制字符串
	sliceSize int64     // 分片大小
	finish    *int64    // 已写入的总字节数
	fileSize  int64     // 文件总大小
	up        driver.UpdateProgress
	ctx       context.Context
}

func (w *sliceHashWriter) Write(p []byte) (int, error) {
	if utils.IsCanceled(w.ctx) {
		return 0, w.ctx.Err()
	}
	total := len(p)
	written := 0
	for written < total {
		// 当前分片还能写入的字节数
		sliceRemain := w.sliceSize - (*w.finish % w.sliceSize)
		toWrite := int64(total - written)
		if toWrite > sliceRemain {
			toWrite = sliceRemain
		}
		n, err := w.hash.Write(p[written : written+int(toWrite)])
		if err != nil {
			return written, err
		}
		written += n
		*w.finish += int64(n)

		// 当前分片写满，记录MD5并重置
		if *w.finish%w.sliceSize == 0 {
			if h, ok := w.hash.(interface{ Sum([]byte) []byte }); ok {
				*w.md5s = append(*w.md5s, strings.ToUpper(hex.EncodeToString(h.Sum(nil))))
			}
			if resetter, ok := w.hash.(interface{ Reset() }); ok {
				resetter.Reset()
			}
		}
	}
	// 报告进度（缓存阶段占50%）
	if w.fileSize > 0 && w.up != nil {
		w.up(float64(*w.finish) / float64(w.fileSize) * 50)
	}
	return total, nil
}
