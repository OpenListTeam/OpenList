package baidu_netdisk

import (
	"bytes"
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	stdpath "path"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/drivers/base"
	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/errs"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/net"
	"github.com/OpenListTeam/OpenList/v4/pkg/errgroup"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/avast/retry-go"
	log "github.com/sirupsen/logrus"
)

type BaiduNetdisk struct {
	model.Storage
	Addition

	uploadThread int
	vipType      int // 会员类型，0普通用户(4G/4M)、1普通会员(10G/16M)、2超级会员(20G/32M)
}

var ErrUploadIDExpired = errors.New("uploadid expired")

func (d *BaiduNetdisk) Config() driver.Config {
	return config
}

func (d *BaiduNetdisk) GetAddition() driver.Additional {
	return &d.Addition
}

func (d *BaiduNetdisk) Init(ctx context.Context) error {
	d.uploadThread, _ = strconv.Atoi(d.UploadThread)
	if d.uploadThread < 1 {
		d.uploadThread, d.UploadThread = 1, "1"
	} else if d.uploadThread > 32 {
		d.uploadThread, d.UploadThread = 32, "32"
	}

	if _, err := url.Parse(d.UploadAPI); d.UploadAPI == "" || err != nil {
		d.UploadAPI = UPLOAD_FALLBACK_API
	}

	res, err := d.get("/xpan/nas", map[string]string{
		"method": "uinfo",
	}, nil)
	log.Debugf("[baidu_netdisk] get uinfo: %s", string(res))
	if err != nil {
		return err
	}
	d.vipType = utils.Json.Get(res, "vip_type").ToInt()
	return nil
}

func (d *BaiduNetdisk) Drop(ctx context.Context) error {
	return nil
}

func (d *BaiduNetdisk) List(ctx context.Context, dir model.Obj, args model.ListArgs) ([]model.Obj, error) {
	files, err := d.getFiles(dir.GetPath())
	if err != nil {
		return nil, err
	}
	return utils.SliceConvert(files, func(src File) (model.Obj, error) {
		return fileToObj(src), nil
	})
}

func (d *BaiduNetdisk) Link(ctx context.Context, file model.Obj, args model.LinkArgs) (*model.Link, error) {
	switch d.DownloadAPI {
	case "crack":
		return d.linkCrack(file, args)
	case "crack_video":
		return d.linkCrackVideo(file, args)
	}
	return d.linkOfficial(file, args)
}

func (d *BaiduNetdisk) MakeDir(ctx context.Context, parentDir model.Obj, dirName string) (model.Obj, error) {
	var newDir File
	_, err := d.create(stdpath.Join(parentDir.GetPath(), dirName), 0, 1, "", "", &newDir, 0, 0)
	if err != nil {
		return nil, err
	}
	return fileToObj(newDir), nil
}

func (d *BaiduNetdisk) Move(ctx context.Context, srcObj, dstDir model.Obj) (model.Obj, error) {
	data := []base.Json{
		{
			"path":    srcObj.GetPath(),
			"dest":    dstDir.GetPath(),
			"newname": srcObj.GetName(),
		},
	}
	_, err := d.manage("move", data)
	if err != nil {
		return nil, err
	}
	if srcObj, ok := srcObj.(*model.ObjThumb); ok {
		srcObj.SetPath(stdpath.Join(dstDir.GetPath(), srcObj.GetName()))
		srcObj.Modified = time.Now()
		return srcObj, nil
	}
	return nil, nil
}

func (d *BaiduNetdisk) Rename(ctx context.Context, srcObj model.Obj, newName string) (model.Obj, error) {
	data := []base.Json{
		{
			"path":    srcObj.GetPath(),
			"newname": newName,
		},
	}
	_, err := d.manage("rename", data)
	if err != nil {
		return nil, err
	}

	if srcObj, ok := srcObj.(*model.ObjThumb); ok {
		srcObj.SetPath(stdpath.Join(stdpath.Dir(srcObj.GetPath()), newName))
		srcObj.Name = newName
		srcObj.Modified = time.Now()
		return srcObj, nil
	}
	return nil, nil
}

func (d *BaiduNetdisk) Copy(ctx context.Context, srcObj, dstDir model.Obj) error {
	data := []base.Json{
		{
			"path":    srcObj.GetPath(),
			"dest":    dstDir.GetPath(),
			"newname": srcObj.GetName(),
		},
	}
	_, err := d.manage("copy", data)
	return err
}

func (d *BaiduNetdisk) Remove(ctx context.Context, obj model.Obj) error {
	data := []string{obj.GetPath()}
	_, err := d.manage("delete", data)
	return err
}

func (d *BaiduNetdisk) PutRapid(ctx context.Context, dstDir model.Obj, stream model.FileStreamer) (model.Obj, error) {
	contentMd5 := stream.GetHash().GetHash(utils.MD5)
	if len(contentMd5) < utils.MD5.Width {
		return nil, errors.New("invalid hash")
	}

	streamSize := stream.GetSize()
	path := stdpath.Join(dstDir.GetPath(), stream.GetName())
	mtime := stream.ModTime().Unix()
	ctime := stream.CreateTime().Unix()
	blockList, _ := utils.Json.MarshalToString([]string{contentMd5})

	var newFile File
	_, err := d.create(path, streamSize, 0, "", blockList, &newFile, mtime, ctime)
	if err != nil {
		return nil, err
	}
	// 修复时间，具体原因见 Put 方法注释的 **注意**
	newFile.Ctime = stream.CreateTime().Unix()
	newFile.Mtime = stream.ModTime().Unix()
	return fileToObj(newFile), nil
}

// Put
//
// **注意**: 截至 2024/04/20 百度云盘 api 接口返回的时间永远是当前时间，而不是文件时间。
// 而实际上云盘存储的时间是文件时间，所以此处需要覆盖时间，保证缓存与云盘的数据一致
func (d *BaiduNetdisk) Put(ctx context.Context, dstDir model.Obj, stream model.FileStreamer, up driver.UpdateProgress) (model.Obj, error) {
	// 百度网盘不允许上传空文件
	if stream.GetSize() < 1 {
		return nil, ErrBaiduEmptyFilesNotAllowed
	}

	// rapid upload
	if newObj, err := d.PutRapid(ctx, dstDir, stream); err == nil {
		return newObj, nil
	}

	var (
		cache = stream.GetFile()
		tmpF  *os.File
		err   error
	)
	if cache == nil {
		tmpF, err = os.CreateTemp(conf.Conf.TempDir, "file-*")
		if err != nil {
			return nil, err
		}
		defer func() {
			_ = tmpF.Close()
			_ = os.Remove(tmpF.Name())
		}()
		cache = tmpF
	}

	streamSize := stream.GetSize()
	sliceSize := d.getSliceSize(streamSize)
	count := 1
	if streamSize > sliceSize {
		count = int((streamSize + sliceSize - 1) / sliceSize)
	}
	lastBlockSize := streamSize % sliceSize
	if lastBlockSize == 0 {
		lastBlockSize = sliceSize
	}

	// cal md5 for first 256k data
	const SliceSize int64 = 256 * utils.KB
	blockList := make([]string, 0, count)
	byteSize := sliceSize
	fileMd5H := md5.New()
	sliceMd5H := md5.New()
	sliceMd5H2 := md5.New()
	slicemd5H2Write := utils.LimitWriter(sliceMd5H2, SliceSize)
	writers := []io.Writer{fileMd5H, sliceMd5H, slicemd5H2Write}
	if tmpF != nil {
		writers = append(writers, tmpF)
	}
	written := int64(0)

	for i := 1; i <= count; i++ {
		if utils.IsCanceled(ctx) {
			return nil, ctx.Err()
		}
		if i == count {
			byteSize = lastBlockSize
		}
		n, err := utils.CopyWithBufferN(io.MultiWriter(writers...), stream, byteSize)
		written += n
		if err != nil && err != io.EOF {
			return nil, err
		}
		blockList = append(blockList, hex.EncodeToString(sliceMd5H.Sum(nil)))
		sliceMd5H.Reset()
	}
	if tmpF != nil {
		if written != streamSize {
			return nil, errs.NewErr(err, "CreateTempFile failed, size mismatch: %d != %d ", written, streamSize)
		}
		_, err = tmpF.Seek(0, io.SeekStart)
		if err != nil {
			return nil, errs.NewErr(err, "CreateTempFile failed, can't seek to 0 ")
		}
	}
	contentMd5 := hex.EncodeToString(fileMd5H.Sum(nil))
	sliceMd5 := hex.EncodeToString(sliceMd5H2.Sum(nil))
	blockListStr, _ := utils.Json.MarshalToString(blockList)
	path := stdpath.Join(dstDir.GetPath(), stream.GetName())
	mtime := stream.ModTime().Unix()
	ctime := stream.CreateTime().Unix()

	// step.1 尝试读取已保存进度
	precreateResp, ok := base.GetUploadProgress[*PrecreateResp](d, d.AccessToken, contentMd5)
	if !ok {
		// 没有进度，走预上传
		precreateResp, err = d.precreate(ctx, path, streamSize, blockListStr, contentMd5, sliceMd5, ctime, mtime)
		if err != nil {
			return nil, err
		}
		if precreateResp.ReturnType == 2 {
			// rapid upload, since got md5 match from baidu server
			// 修复时间，具体原因见 Put 方法注释的 **注意**
			precreateResp.File.Ctime = ctime
			precreateResp.File.Mtime = mtime
			return fileToObj(precreateResp.File), nil
		}
	}
	ensureUploadURL := func() {
		if precreateResp.UploadURL != "" {
			return
		}
		precreateResp.UploadURL = d.getUploadUrl(path, precreateResp.Uploadid)
	}

	// step.2 上传分片
uploadLoop:
	for range 2 {
		// 获取上传域名
		ensureUploadURL()
		// 并发上传
		threadG, upCtx := errgroup.NewGroupWithContext(ctx, d.uploadThread,
			retry.Attempts(UPLOAD_RETRY_COUNT),
			retry.Delay(UPLOAD_RETRY_WAIT_TIME),
			retry.MaxDelay(UPLOAD_RETRY_MAX_WAIT_TIME),
			retry.DelayType(retry.BackOffDelay),
			retry.RetryIf(func(err error) bool {
				return !errors.Is(err, ErrUploadIDExpired)
			}),
			retry.LastErrorOnly(true))

		totalParts := len(precreateResp.BlockList)

		for i, partseq := range precreateResp.BlockList {
			if utils.IsCanceled(upCtx) {
				break
			}
			if partseq < 0 {
				continue
			}
			i, partseq := i, partseq
			offset, size := int64(partseq)*sliceSize, sliceSize
			if partseq+1 == count {
				size = lastBlockSize
			}
			threadG.Go(func(ctx context.Context) error {
				params := map[string]string{
					"method":       "upload",
					"access_token": d.AccessToken,
					"type":         "tmpfile",
					"path":         path,
					"uploadid":     precreateResp.Uploadid,
					"partseq":      strconv.Itoa(partseq),
				}
				section := io.NewSectionReader(cache, offset, size)
				err := d.uploadSlice(ctx, precreateResp.UploadURL, params, stream.GetName(), section)
				if err != nil {
					return err
				}
				precreateResp.BlockList[i] = -1
				progress := float64(threadG.Success()+1) * 100 / float64(totalParts+1)
				up(progress)
				return nil
			})
		}

		err = threadG.Wait()
		if err == nil {
			break uploadLoop
		}

		// 保存进度（所有错误都会保存）
		precreateResp.BlockList = utils.SliceFilter(precreateResp.BlockList, func(s int) bool { return s >= 0 })
		base.SaveUploadProgress(d, precreateResp, d.AccessToken, contentMd5)

		if errors.Is(err, context.Canceled) {
			return nil, err
		}
		if errors.Is(err, ErrUploadIDExpired) {
			log.Warn("[baidu_netdisk] uploadid expired, will restart from scratch")
			// 重新 precreate（所有分片都要重传）
			newPre, err2 := d.precreate(ctx, path, streamSize, blockListStr, "", "", ctime, mtime)
			if err2 != nil {
				return nil, err2
			}
			if newPre.ReturnType == 2 {
				return fileToObj(newPre.File), nil
			}
			precreateResp = newPre
			precreateResp.UploadURL = ""
			// 覆盖掉旧的进度
			base.SaveUploadProgress(d, precreateResp, d.AccessToken, contentMd5)
			continue uploadLoop
		}
		return nil, err
	}
	defer up(100)

	// step.3 创建文件
	var newFile File
	_, err = d.create(path, streamSize, 0, precreateResp.Uploadid, blockListStr, &newFile, mtime, ctime)
	if err != nil {
		return nil, err
	}
	// 修复时间，具体原因见 Put 方法注释的 **注意**
	newFile.Ctime = ctime
	newFile.Mtime = mtime
	// 上传成功清理进度
	base.SaveUploadProgress(d, nil, d.AccessToken, contentMd5)
	return fileToObj(newFile), nil
}

// precreate 执行预上传操作，支持首次上传和 uploadid 过期重试
func (d *BaiduNetdisk) precreate(ctx context.Context, path string, streamSize int64, blockListStr, contentMd5, sliceMd5 string, ctime, mtime int64) (*PrecreateResp, error) {
	params := map[string]string{"method": "precreate"}
	form := map[string]string{
		"path":       path,
		"size":       strconv.FormatInt(streamSize, 10),
		"isdir":      "0",
		"autoinit":   "1",
		"rtype":      "3",
		"block_list": blockListStr,
	}

	// 只有在首次上传时才包含 content-md5 和 slice-md5
	if contentMd5 != "" && sliceMd5 != "" {
		form["content-md5"] = contentMd5
		form["slice-md5"] = sliceMd5
	}

	joinTime(form, ctime, mtime)

	var precreateResp PrecreateResp
	_, err := d.postForm("/xpan/file", params, form, &precreateResp)
	if err != nil {
		return nil, err
	}

	// 修复时间，具体原因见 Put 方法注释的 **注意**
	if precreateResp.ReturnType == 2 {
		precreateResp.File.Ctime = ctime
		precreateResp.File.Mtime = mtime
	}

	return &precreateResp, nil
}

func (d *BaiduNetdisk) uploadSlice(ctx context.Context, uploadUrl string, params map[string]string, fileName string, file *io.SectionReader) error {
	b := bytes.NewBuffer(make([]byte, 0, bytes.MinRead))
	mw := multipart.NewWriter(b)
	_, err := mw.CreateFormFile("file", fileName)
	if err != nil {
		return err
	}
	headSize := b.Len()
	err = mw.Close()
	if err != nil {
		return err
	}
	head := bytes.NewReader(b.Bytes()[:headSize])
	tail := bytes.NewReader(b.Bytes()[headSize:])
	rateLimitedRd := driver.NewLimitedUploadStream(ctx, io.MultiReader(head, file, tail))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, uploadUrl+"/rest/2.0/pcs/superfile2", rateLimitedRd)
	if err != nil {
		return err
	}
	query := req.URL.Query()
	for k, v := range params {
		query.Set(k, v)
	}
	req.URL.RawQuery = query.Encode()
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.ContentLength = int64(b.Len()) + file.Size()

	client := net.NewHttpClient()
	if d.UploadSliceTimeout > 0 {
		client.Timeout = time.Second * time.Duration(d.UploadSliceTimeout)
	} else {
		client.Timeout = DEFAULT_UPLOAD_SLICE_TIMEOUT
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b.Reset()
	_, err = b.ReadFrom(resp.Body)
	if err != nil {
		return err
	}
	body := b.Bytes()
	respStr := string(body)
	log.Debugln(respStr)
	lower := strings.ToLower(respStr)
	// 合并 uploadid 过期检测逻辑
	if strings.Contains(lower, "uploadid") &&
		(strings.Contains(lower, "invalid") || strings.Contains(lower, "expired") || strings.Contains(lower, "not found")) {
		return ErrUploadIDExpired
	}

	errCode := utils.Json.Get(body, "error_code").ToInt()
	errNo := utils.Json.Get(body, "errno").ToInt()
	if errCode != 0 || errNo != 0 {
		return errs.NewErr(errs.StreamIncomplete, "error uploading to baidu, response=%s", respStr)
	}
	return nil
}

func (d *BaiduNetdisk) GetDetails(ctx context.Context) (*model.StorageDetails, error) {
	du, err := d.quota(ctx)
	if err != nil {
		return nil, err
	}
	return &model.StorageDetails{DiskUsage: du}, nil
}

var _ driver.Driver = (*BaiduNetdisk)(nil)
