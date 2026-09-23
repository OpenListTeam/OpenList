package mediafire

/*
Package mediafire
Author: Da3zKi7<da3zki7@duck.com>
Date: 2025-09-11

D@' 3z K!7 - The King Of Cracking

Modifications by ILoveScratch2<ilovescratch@foxmail.com>
Date: 2025-09-21

Date: 2025-09-26
Final opts by @Suyunjing @j2rong4cn @KirCute @Da3zKi7
*/

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/cron"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
	"golang.org/x/time/rate"
)

type Mediafire struct {
	model.Storage
	Addition

	cron *cron.Cron

	// mu guards SessionToken and Cookie: request goroutines read the token
	// while the renewal cron may refresh it in the background.
	mu sync.RWMutex

	actionToken string
	limiter     *rate.Limiter

	appBase    string
	apiBase    string
	hostBase   string
	maxRetries int

	secChUa         string
	secChUaPlatform string
	userAgent       string
}

// sessionToken returns the current session token for request building.
func (d *Mediafire) sessionToken() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.SessionToken
}

// cookie returns the current login cookie for request building.
func (d *Mediafire) cookie() string {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.Cookie
}

// setSessionToken stores a refreshed token (and login cookie when provided)
// and persists them, safe to call from the renewal cron goroutine. Note that
// framework-driven re-Init (admin storage update) rewrites the addition
// outside this lock; the framework serializes those via Drop.
func (d *Mediafire) setSessionToken(token, cookie string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if cookie != "" {
		d.Cookie = cookie
	}
	d.SessionToken = token
	op.MustSaveDriverStorage(d)
}

func (d *Mediafire) Config() driver.Config {
	return config
}

func (d *Mediafire) GetAddition() driver.Additional {
	return &d.Addition
}

// Init initializes the MediaFire driver with session token and cookie validation
func (d *Mediafire) Init(ctx context.Context) error {
	if d.cookie() == "" {
		return fmt.Errorf("Init :: [MediaFire] {critical} missing Cookie")
	}

	// Setup rate limiter if rate limit is configured
	if d.LimitRate > 0 {
		d.limiter = rate.NewLimiter(rate.Limit(d.LimitRate), 1)
	}

	// Obtain a session token: mint from the login cookie first, fall back to
	// renewing the stored token. A transient failure here must not skip the
	// scheduling below — the renewal cron self-heals on its next tick.
	if _, err := d.getSessionToken(ctx); err != nil {
		if renewErr := d.renewToken(ctx); renewErr != nil {
			log.Warnf("mediafire[%s]: could not obtain a session token (mint: %v, renew: %v); will retry on the renewal cron",
				d.MountPath, err, renewErr)
		}
	}

	// MediaFire session tokens expire in ~10 minutes, so they must be
	// renewed periodically no matter how the first token was obtained.
	num := rand.Intn(4) + 6

	d.cron = cron.NewCron(time.Minute * time.Duration(num))
	d.cron.Do(func() {
		// Renew while the token is still valid; if it already expired,
		// mint a fresh one from the login cookie.
		if err := d.renewToken(context.Background()); err != nil {
			if _, mintErr := d.getSessionToken(context.Background()); mintErr != nil {
				log.Warnf("mediafire[%s]: session token renewal failed: %v", d.MountPath, mintErr)
			}
		}
	})

	return nil
}

// Drop cleans up driver resources
func (d *Mediafire) Drop(ctx context.Context) error {
	// Stop the cron first so no in-flight renewal can repopulate the
	// action token after it is cleared, then clear under the lock so a
	// concurrent upload worker cannot read a stale value.
	if d.cron != nil {
		d.cron.Stop()
		d.cron = nil
	}
	d.mu.Lock()
	d.actionToken = ""
	d.mu.Unlock()
	return nil
}

// List retrieves files and folders from the specified directory
func (d *Mediafire) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(ctx, dir.GetID())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return d.fileToObj(src), nil
	})
}

// Link generates a direct download link for the specified file
func (d *Mediafire) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	downloadUrl, err := d.getDirectDownloadLink(ctx, file.GetID())
	if err != nil {
		return nil, err
	}

	res, err := base.NoRedirectClient.R().SetDoNotParseResponse(true).SetContext(ctx).Head(downloadUrl)
	if err != nil {
		return nil, err
	}
	defer func() {
		_ = res.RawBody().Close()
	}()

	if res.StatusCode() == 302 {
		downloadUrl = res.Header().Get("location")
	}

	return &model.Link{
		URL: downloadUrl,
		Header: http.Header{
			"Origin":             []string{d.appBase},
			"Referer":            []string{d.appBase + "/"},
			"sec-ch-ua":          []string{d.secChUa},
			"sec-ch-ua-platform": []string{d.secChUaPlatform},
			"User-Agent":         []string{d.userAgent},
		},
	}, nil
}

// MakeDir creates a new folder in the specified parent directory
func (d *Mediafire) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	data := map[string]string{
		"session_token":   d.sessionToken(),
		"response_format": "json",
		"parent_key":      parentDir.GetID(),
		"foldername":      dirName,
	}

	var resp MediafireFolderCreateResponse
	_, err := d.postForm(ctx, "/folder/create.php", data, &resp)
	if err != nil {
		return nil, err
	}

	if err := checkAPIResult(resp.Response.Result); err != nil {
		return nil, err
	}

	created, _ := time.Parse("2006-01-02T15:04:05Z", resp.Response.CreatedUTC)

	return &model.Object{
		ID:       resp.Response.FolderKey,
		Name:     resp.Response.Name,
		Size:     0,
		Modified: created,
		Ctime:    created,
		IsFolder: true,
	}, nil
}

// Move relocates a file or folder to a different parent directory
func (d *Mediafire) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	var data map[string]string
	var endpoint string

	if srcObj.IsDir() {

		endpoint = "/folder/move.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"folder_key_src":  srcObj.GetID(),
			"folder_key_dst":  dstDir.GetID(),
		}
	} else {

		endpoint = "/file/move.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"quick_key":       srcObj.GetID(),
			"folder_key":      dstDir.GetID(),
		}
	}

	var resp MediafireMoveResponse
	_, err := d.postForm(ctx, endpoint, data, &resp)
	if err != nil {
		return nil, err
	}

	if err := checkAPIResult(resp.Response.Result); err != nil {
		return nil, err
	}

	return srcObj, nil
}

// Rename changes the name of a file or folder
func (d *Mediafire) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	var data map[string]string
	var endpoint string

	if srcObj.IsDir() {

		endpoint = "/folder/update.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"folder_key":      srcObj.GetID(),
			"foldername":      newName,
		}
	} else {

		endpoint = "/file/update.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"quick_key":       srcObj.GetID(),
			"filename":        newName,
		}
	}

	var resp MediafireRenameResponse
	_, err := d.postForm(ctx, endpoint, data, &resp)
	if err != nil {
		return nil, err
	}

	if err := checkAPIResult(resp.Response.Result); err != nil {
		return nil, err
	}

	return &model.Object{
		ID:       srcObj.GetID(),
		Name:     newName,
		Size:     srcObj.GetSize(),
		Modified: srcObj.ModTime(),
		Ctime:    srcObj.CreateTime(),
		IsFolder: srcObj.IsDir(),
	}, nil
}

// Copy creates a duplicate of a file or folder in the specified destination directory
func (d *Mediafire) Copy(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	var data map[string]string
	var endpoint string

	if srcObj.IsDir() {

		endpoint = "/folder/copy.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"folder_key_src":  srcObj.GetID(),
			"folder_key_dst":  dstDir.GetID(),
		}
	} else {

		endpoint = "/file/copy.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"quick_key":       srcObj.GetID(),
			"folder_key":      dstDir.GetID(),
		}
	}

	var resp MediafireCopyResponse
	_, err := d.postForm(ctx, endpoint, data, &resp)
	if err != nil {
		return nil, err
	}

	if err := checkAPIResult(resp.Response.Result); err != nil {
		return nil, err
	}

	var newID string
	if srcObj.IsDir() {
		if len(resp.Response.NewFolderKeys) > 0 {
			newID = resp.Response.NewFolderKeys[0]
		}
	} else {
		if len(resp.Response.NewQuickKeys) > 0 {
			newID = resp.Response.NewQuickKeys[0]
		}
	}

	return &model.Object{
		ID:       newID,
		Name:     srcObj.GetName(),
		Size:     srcObj.GetSize(),
		Modified: srcObj.ModTime(),
		Ctime:    srcObj.CreateTime(),
		IsFolder: srcObj.IsDir(),
	}, nil
}

// Remove deletes a file or folder permanently
func (d *Mediafire) Remove(ctx context.Context, obj model.Obj) error {
	var data map[string]string
	var endpoint string

	if obj.IsDir() {

		endpoint = "/folder/delete.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"folder_key":      obj.GetID(),
		}
	} else {

		endpoint = "/file/delete.php"
		data = map[string]string{
			"session_token":   d.sessionToken(),
			"response_format": "json",
			"quick_key":       obj.GetID(),
		}
	}

	var resp MediafireRemoveResponse
	_, err := d.postForm(ctx, endpoint, data, &resp)
	if err != nil {
		return err
	}

	return checkAPIResult(resp.Response.Result)
}

// Put uploads a file to the specified directory with support for resumable upload and quick upload
func (d *Mediafire) Put(ctx context.Context, dstDir model.Obj, file model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	fileHash := file.GetHash().GetHash(utils.SHA256)
	var err error

	// Try to use existing hash first, cache only if necessary
	if len(fileHash) != utils.SHA256.Width {
		_, fileHash, err = stream.CacheFullAndHash(file, &up, utils.SHA256)
		if err != nil {
			return nil, err
		}
	}

	checkResp, err := d.uploadCheck(ctx, file.GetName(), file.GetSize(), fileHash, dstDir.GetID())
	if err != nil {
		return nil, err
	}

	if checkResp.Response.HashExists == "yes" && checkResp.Response.InAccount == "yes" {
		up(100.0)
		existingFile, err := d.getExistingFileInfo(ctx, fileHash, file.GetName(), dstDir.GetID())
		if err == nil && existingFile != nil {
			// File exists, return existing file info
			return &model.Object{
				ID:   existingFile.GetID(),
				Name: file.GetName(),
				Size: file.GetSize(),
			}, nil
		}
		// If getExistingFileInfo fails, log and continue with normal upload
		// This ensures upload doesn't fail due to search issues
	}

	var pollKey string

	if checkResp.Response.ResumableUpload.AllUnitsReady != "yes" {
		pollKey, err = d.uploadUnits(ctx, file, checkResp, file.GetName(), fileHash, dstDir.GetID(), up)
		if err != nil {
			return nil, err
		}
	} else {
		pollKey = checkResp.Response.ResumableUpload.UploadKey
	}
	defer up(100.0)

	pollResp, err := d.pollUpload(ctx, pollKey)
	if err != nil {
		return nil, err
	}

	return &model.Object{
		ID:   pollResp.Response.Doupload.QuickKey,
		Name: file.GetName(),
		Size: file.GetSize(),
	}, nil
}

func (d *Mediafire) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	data := map[string]string{
		"session_token":   d.sessionToken(),
		"response_format": "json",
	}
	var resp MediafireUserInfoResponse
	_, err := d.postForm(ctx, "/user/get_info.php", data, &resp)
	if err != nil {
		return nil, err
	}
	used, err := strconv.ParseInt(resp.Response.UserInfo.UsedStorageSize, 10, 64)
	if err != nil {
		return nil, err
	}
	total, err := strconv.ParseInt(resp.Response.UserInfo.StorageLimit, 10, 64)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{
		DiskUsage: model.DiskUsage{
			TotalSpace: total,
			UsedSpace:  used,
		},
	}, nil
}

var _ driver.Driver = (*Mediafire)(nil)
