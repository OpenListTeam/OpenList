package google_drive

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/avast/retry-go"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/go-resty/resty/v2"
	"github.com/golang-jwt/jwt/v4"
	log "github.com/sirupsen/logrus"
)

// do others that not defined in Driver interface

// Google Drive API field constants
const (
	// File list query fields
	FilesListFields = "files(id,name,mimeType,size,modifiedTime,createdTime,thumbnailLink,shortcutDetails,md5Checksum,sha1Checksum,sha256Checksum),nextPageToken"
	// Single file query fields
	FileInfoFields = "id,name,mimeType,size,md5Checksum,sha1Checksum,sha256Checksum"
)

type googleDriveServiceAccount struct {
	// Type                    string `json:"type"`
	// ProjectID               string `json:"project_id"`
	// PrivateKeyID            string `json:"private_key_id"`
	PrivateKey  string `json:"private_key"`
	ClientEMail string `json:"client_email"`
	// ClientID                string `json:"client_id"`
	// AuthURI                 string `json:"auth_uri"`
	TokenURI string `json:"token_uri"`
	// AuthProviderX509CertURL string `json:"auth_provider_x509_cert_url"`
	// ClientX509CertURL       string `json:"client_x509_cert_url"`
}

func (d *GoogleDrive) refreshToken() error {
	// 使用在线API刷新Token，无需ClientID和ClientSecret
	if d.UseOnlineAPI && len(d.APIAddress) > 0 {
		u := d.APIAddress
		var resp struct {
			RefreshToken string `json:"refresh_token"`
			AccessToken  string `json:"access_token"`
			ErrorMessage string `json:"text"`
		}
		_, err := base.RestyClient.R().
			SetResult(&resp).
			SetQueryParams(map[string]string{
				"refresh_ui": d.RefreshToken,
				"server_use": "true",
				"driver_txt": "googleui_go",
			}).
			Get(u)
		if err != nil {
			return err
		}
		if resp.RefreshToken == "" || resp.AccessToken == "" {
			if resp.ErrorMessage != "" {
				return fmt.Errorf("failed to refresh token: %s", resp.ErrorMessage)
			}
			return fmt.Errorf("empty token returned from official API, a wrong refresh token may have been used")
		}
		d.AccessToken = resp.AccessToken
		d.RefreshToken = resp.RefreshToken
		op.MustSaveDriverStorage(d)
		return nil
	}
	// 使用本地客户端的情况下检查是否为空
	if d.ClientID == "" || d.ClientSecret == "" {
		return fmt.Errorf("empty ClientID or ClientSecret")
	}
	// 走原有的刷新逻辑

	// googleDriveServiceAccountFile gdsaFile
	gdsaFile, gdsaFileErr := os.Stat(d.RefreshToken)
	if gdsaFileErr == nil {
		gdsaFileThis := d.RefreshToken
		if gdsaFile.IsDir() {
			if len(d.ServiceAccountFileList) <= 0 {
				gdsaReadDir, gdsaDirErr := os.ReadDir(d.RefreshToken)
				if gdsaDirErr != nil {
					log.Error("read dir fail")
					return gdsaDirErr
				}
				var gdsaFileList []string
				for _, fi := range gdsaReadDir {
					if !fi.IsDir() {
						match, _ := regexp.MatchString("^.*\\.json$", fi.Name())
						if !match {
							continue
						}
						gdsaDirText := d.RefreshToken
						if d.RefreshToken[len(d.RefreshToken)-1:] != "/" {
							gdsaDirText = d.RefreshToken + "/"
						}
						gdsaFileList = append(gdsaFileList, gdsaDirText+fi.Name())
					}
				}
				d.ServiceAccountFileList = gdsaFileList
				gdsaFileThis = d.ServiceAccountFileList[d.ServiceAccountFile]
				d.ServiceAccountFile++
			} else {
				if d.ServiceAccountFile < len(d.ServiceAccountFileList) {
					d.ServiceAccountFile++
				} else {
					d.ServiceAccountFile = 0
				}
				gdsaFileThis = d.ServiceAccountFileList[d.ServiceAccountFile]
			}
		}

		gdsaFileThisContent, err := os.ReadFile(gdsaFileThis)
		if err != nil {
			return err
		}

		// Now let's unmarshal the data into `payload`
		var jsonData googleDriveServiceAccount
		err = utils.Json.Unmarshal(gdsaFileThisContent, &jsonData)
		if err != nil {
			return err
		}

		gdsaScope := "https://www.googleapis.com/auth/drive https://www.googleapis.com/auth/drive.appdata https://www.googleapis.com/auth/drive.file https://www.googleapis.com/auth/drive.metadata https://www.googleapis.com/auth/drive.metadata.readonly https://www.googleapis.com/auth/drive.readonly https://www.googleapis.com/auth/drive.scripts"

		timeNow := time.Now()
		var timeStart int64 = timeNow.Unix()
		var timeEnd int64 = timeNow.Add(time.Minute * 60).Unix()

		// load private key from string
		privateKeyPem, _ := pem.Decode([]byte(jsonData.PrivateKey))
		privateKey, _ := x509.ParsePKCS8PrivateKey(privateKeyPem.Bytes)

		jwtToken := jwt.NewWithClaims(jwt.SigningMethodRS256,
			jwt.MapClaims{
				"iss":   jsonData.ClientEMail,
				"scope": gdsaScope,
				"aud":   jsonData.TokenURI,
				"exp":   timeEnd,
				"iat":   timeStart,
			})
		assertion, err := jwtToken.SignedString(privateKey)
		if err != nil {
			return err
		}

		var resp base.TokenResp
		var e TokenError
		res, err := base.RestyClient.R().SetResult(&resp).SetError(&e).
			SetFormData(map[string]string{
				"assertion":  assertion,
				"grant_type": "urn:ietf:params:oauth:grant-type:jwt-bearer",
			}).Post(jsonData.TokenURI)
		if err != nil {
			return err
		}
		log.Debug(res.String())
		if e.Error != "" {
			return fmt.Errorf(e.Error)
		}
		d.AccessToken = resp.AccessToken
		return nil
	} else if os.IsExist(gdsaFileErr) {
		return gdsaFileErr
	}
	url := "https://www.googleapis.com/oauth2/v4/token"
	var resp base.TokenResp
	var e TokenError
	res, err := base.RestyClient.R().SetResult(&resp).SetError(&e).
		SetFormData(map[string]string{
			"client_id":     d.ClientID,
			"client_secret": d.ClientSecret,
			"refresh_token": d.RefreshToken,
			"grant_type":    "refresh_token",
		}).Post(url)
	if err != nil {
		return err
	}
	log.Debug(res.String())
	if e.Error != "" {
		return fmt.Errorf(e.Error)
	}
	d.AccessToken = resp.AccessToken
	return nil
}

func (d *GoogleDrive) request(url string, method string, callback base.ReqCallback, resp interface{}) ([]byte, error) {
	req := base.RestyClient.R()
	req.SetHeader("Authorization", "Bearer "+d.AccessToken)
	req.SetQueryParam("includeItemsFromAllDrives", "true")
	req.SetQueryParam("supportsAllDrives", "true")
	if callback != nil {
		callback(req)
	}
	if resp != nil {
		req.SetResult(resp)
	}
	var e Error
	req.SetError(&e)
	res, err := req.Execute(method, url)
	if err != nil {
		return nil, err
	}
	if e.Error.Code != 0 {
		if e.Error.Code == 401 {
			err = d.refreshToken()
			if err != nil {
				return nil, err
			}
			return d.request(url, method, callback, resp)
		}
		return nil, fmt.Errorf("%s: %v", e.Error.Message, e.Error.Errors)
	}
	return res.Body(), nil
}

func (d *GoogleDrive) getFiles(id string) ([]File, error) {
	pageToken := "first"
	res := make([]File, 0)
	for pageToken != "" {
		if pageToken == "first" {
			pageToken = ""
		}
		var resp Files
		orderBy := "folder,name,modifiedTime desc"
		if d.OrderBy != "" {
			orderBy = d.OrderBy + " " + d.OrderDirection
		}
		query := map[string]string{
			"orderBy":  orderBy,
			"fields":   FilesListFields,
			"pageSize": "1000",
			"q":        fmt.Sprintf("'%s' in parents and trashed = false", id),
			//"includeItemsFromAllDrives": "true",
			//"supportsAllDrives":         "true",
			"pageToken": pageToken,
		}
		_, err := d.request("https://www.googleapis.com/drive/v3/files", http.MethodGet, func(req *resty.Request) {
			req.SetQueryParams(query)
		}, &resp)
		if err != nil {
			return nil, err
		}
		pageToken = resp.NextPageToken

		// Batch process shortcuts, API calls only for file shortcuts
		shortcutTargetIds := make([]string, 0)
		shortcutIndices := make([]int, 0)

		// Collect target IDs of all file shortcuts (skip folder shortcuts)
		for i := range resp.Files {
			if resp.Files[i].MimeType == "application/vnd.google-apps.shortcut" &&
				resp.Files[i].ShortcutDetails.TargetId != "" &&
				resp.Files[i].ShortcutDetails.TargetMimeType != "application/vnd.google-apps.folder" {
				shortcutTargetIds = append(shortcutTargetIds, resp.Files[i].ShortcutDetails.TargetId)
				shortcutIndices = append(shortcutIndices, i)
			}
		}

		// Batch get target file info (only for file shortcuts)
		if len(shortcutTargetIds) > 0 {
			targetFiles := d.batchGetTargetFilesInfo(shortcutTargetIds)
			// Update shortcut file info
			for j, targetId := range shortcutTargetIds {
				if targetFile, exists := targetFiles[targetId]; exists {
					fileIndex := shortcutIndices[j]
					if targetFile.Size != "" {
						resp.Files[fileIndex].Size = targetFile.Size
					}
					if targetFile.MD5Checksum != "" {
						resp.Files[fileIndex].MD5Checksum = targetFile.MD5Checksum
					}
					if targetFile.SHA1Checksum != "" {
						resp.Files[fileIndex].SHA1Checksum = targetFile.SHA1Checksum
					}
					if targetFile.SHA256Checksum != "" {
						resp.Files[fileIndex].SHA256Checksum = targetFile.SHA256Checksum
					}
				}
			}
		}

		res = append(res, resp.Files...)
	}
	return res, nil
}

// getTargetFileInfo gets target file details for shortcuts
func (d *GoogleDrive) getTargetFileInfo(targetId string) (File, error) {
	var targetFile File
	url := fmt.Sprintf("https://www.googleapis.com/drive/v3/files/%s", targetId)
	query := map[string]string{
		"fields": FileInfoFields,
	}
	_, err := d.request(url, http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(query)
	}, &targetFile)
	if err != nil {
		return File{}, err
	}
	return targetFile, nil
}

// batchGetTargetFilesInfo batch gets target file info, sequential processing to avoid concurrency complexity
func (d *GoogleDrive) batchGetTargetFilesInfo(targetIds []string) map[string]File {
	if len(targetIds) == 0 {
		return make(map[string]File)
	}

	result := make(map[string]File)
	// Sequential processing to avoid concurrency complexity
	for _, targetId := range targetIds {
		file, err := d.getTargetFileInfo(targetId)
		if err == nil {
			result[targetId] = file
		}
	}
	return result
}

func (d *GoogleDrive) chunkUpload(ctx context.Context, file model.FileStreamer, url string, up driver.UpdateProgress) error {
	defaultChunkSize := d.ChunkSize * 1024 * 1024
	ss, err := stream.NewStreamSectionReader(file, int(defaultChunkSize), &up)
	if err != nil {
		return err
	}

	var offset int64 = 0
	url += "?includeItemsFromAllDrives=true&supportsAllDrives=true"
	for offset < file.GetSize() {
		if utils.IsCanceled(ctx) {
			return ctx.Err()
		}
		chunkSize := min(file.GetSize()-offset, defaultChunkSize)
		reader, err := ss.GetSectionReader(offset, chunkSize)
		if err != nil {
			return err
		}
		limitedReader := driver.NewLimitedUploadStream(ctx, reader)
		err = retry.Do(func() error {
			reader.Seek(0, io.SeekStart)
			req, err := http.NewRequestWithContext(ctx, http.MethodPut, url, limitedReader)
			if err != nil {
				return err
			}
			req.Header = map[string][]string{
				"Authorization":  {"Bearer " + d.AccessToken},
				"Content-Length": {strconv.FormatInt(chunkSize, 10)},
				"Content-Range":  {fmt.Sprintf("bytes %d-%d/%d", offset, offset+chunkSize-1, file.GetSize())},
			}
			res, err := base.HttpClient.Do(req)
			if err != nil {
				return err
			}
			defer res.Body.Close()
			bytes, _ := io.ReadAll(res.Body)
			var e Error
			utils.Json.Unmarshal(bytes, &e)
			if e.Error.Code != 0 {
				if e.Error.Code == 401 {
					err = d.refreshToken()
					if err != nil {
						return err
					}
				}
				return fmt.Errorf("%s: %v", e.Error.Message, e.Error.Errors)
			}
			up(float64(offset+chunkSize) / float64(file.GetSize()) * 100)
			return nil
		},
			retry.Context(ctx),
			retry.Attempts(3),
			retry.DelayType(retry.BackOffDelay),
			retry.Delay(time.Second))
		ss.FreeSectionReader(reader)
		if err != nil {
			return err
		}
		offset += chunkSize
	}
	return nil
}

func (d *GoogleDrive) getAbout(ctx context.Context) (*AboutResp, error) {
	query := map[string]string{
		"fields": "storageQuota",
	}
	var resp AboutResp
	_, err := d.request("https://www.googleapis.com/drive/v3/about", http.MethodGet, func(req *resty.Request) {
		req.SetQueryParams(query)
		req.SetContext(ctx)
	}, &resp)
	if err != nil {
		return nil, err
	}
	return &resp, nil
}
