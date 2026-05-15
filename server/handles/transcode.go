package handles

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/fs"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/internal/sign"
	"github.com/OpenListTeam/OpenList/v4/internal/transcode"
	"github.com/OpenListTeam/OpenList/v4/server/common"
	"github.com/gin-gonic/gin"
)

// ============================================================
// 播放端：/api/transcode/play  发起或复用一个转码任务
// ============================================================

// TranscodePlay 入口：POST /api/fs/transcode  body: {path}
//
// 行为：
//  1. 检查 transcode_enabled
//  2. fs.Get 获取文件并跑 Decide
//  3. 不需要转码 → 返回 {transcode:false, direct: true}
//  4. 需要转码 → 创建 Job 入队，返回 {transcode:true, job_id, master_url}
func TranscodePlay(c *gin.Context) {
	if !transcode.IsEnabled() {
		common.SuccessResp(c, gin.H{"transcode": false, "reason": "disabled"})
		return
	}
	var req struct {
		Path string `json:"path" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	obj, err := fs.Get(c.Request.Context(), req.Path, &fs.GetArgs{NoLog: true})
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if obj.IsDir() {
		common.ErrorStrResp(c, "path is a directory", 400)
		return
	}
	dec := transcode.Decide(transcode.DecideRequest{
		FileName: obj.GetName(),
		FileSize: obj.GetSize(),
	})
	if !dec.NeedTranscode {
		common.SuccessResp(c, gin.H{
			"transcode": false,
			"reason":    dec.Reason,
		})
		return
	}

	mgr := transcode.Default()
	mgr.Start()

	// 【内存优化】优先尝试解析为本地文件路径，让 ffmpeg 直接读取本地文件，
	// 完全绕过 HTTP /d 代理路径——避免 net.Downloader 给每个 range 请求分配
	// 高达 MaxBufferLimit (默认系统内存 5%) 的大缓冲。这是降低 Go 进程内存
	// 占用最有效的方法。如果不是本地驱动则退回到 HTTP 签名 URL。
	apiURL := common.GetApiUrlFromRequest(c.Request)
	var sourceURL string
	if localFile, ok := resolveLocalFilePath(req.Path); ok {
		// ffmpeg 接受本地路径作为输入，无需 file:// 前缀
		sourceURL = localFile
		fmt.Printf("[transcode] using local file path for ffmpeg: %s\n", localFile)
	} else {
		// 远程驱动：构造签名 HTTP URL
		signedPath := sign.Sign(req.Path)
		sourceURL = fmt.Sprintf("%s/d%s?sign=%s", apiURL, encodePath(req.Path), signedPath)
	}

	// 创建 Job
	job := transcode.NewJob()
	job.Path = req.Path
	job.SourceURL = sourceURL
	// 【关键】探测视频总时长（异步会让首响应变慢，但只有一次，必须等待）
	// 总时长用于生成完整的 playlist，避免 HLS.js 时长显示错误和拖动跳跃问题
	probe, perr := transcode.ProbeSource(sourceURL)
	if perr != nil {
		// 探测失败不阻塞流程，但记录日志；后续 playlist 会缺少总时长信息
		fmt.Printf("[transcode] probe source failed: %v\n", perr)
		probe = transcode.SourceProbe{Size: obj.GetSize()}
	} else if probe.Size == 0 {
		probe.Size = obj.GetSize()
	}
	job.Probe = probe
	profile := transcode.BuildProfile()
	job.Profiles = []transcode.Profile{profile}
	job.Output = transcode.BuildOutputSpec()
	timeoutMin := setting.GetInt(conf.TranscodeJobTimeoutMin, 120)
	job.Deadline = time.Now().Add(time.Duration(timeoutMin) * time.Minute).Unix()
	mgr.Scheduler.Submit(job)

	masterURL := fmt.Sprintf("%s/tc/%s/%s/master.m3u8", apiURL, job.ID, job.CallbackToken)
	common.SuccessResp(c, gin.H{
		"transcode":  true,
		"job_id":     job.ID,
		"master_url": masterURL,
		"profile":    profile.Name,
	})
}

func encodePath(p string) string {
	// 与 down.go 保持一致：保留 '/' 不转义
	parts := strings.Split(p, "/")
	for i, s := range parts {
		parts[i] = url.PathEscape(s)
	}
	return strings.Join(parts, "/")
}

// resolveLocalFilePath 尝试把 OpenList 路径解析为宿主机文件系统的实际路径。
// 仅对本地驱动 (Local) 有效，其他驱动（云盘等）返回 false。
//
// 这是内存优化的关键路径：本地文件场景下让 ffmpeg 直接读文件，可以完全绕过
// HTTP /d 代理路径上的 net.Downloader 大块缓冲（默认每个 range 请求最高
// 占用 MaxBufferLimit = 系统内存 5%，多 chunk 并发 + 多 range 累计可达数 GB）。
func resolveLocalFilePath(rawPath string) (string, bool) {
	storage, actualPath, err := op.GetStorageAndActualPath(rawPath)
	if err != nil || storage == nil {
		return "", false
	}
	if storage.Config().Name != "Local" {
		return "", false
	}
	rooter, ok := storage.(driver.IRootPath)
	if !ok {
		return "", false
	}
	root := rooter.GetRootPath()
	if root == "" {
		return "", false
	}
	full := filepath.Join(root, actualPath)
	// 只接受真实存在的文件，避免传给 ffmpeg 一个无效路径
	fi, err := os.Stat(full)
	if err != nil || fi.IsDir() {
		return "", false
	}
	return full, true
}

// ============================================================
// 播放端：/tc/:job/:token/...  Master/Variant playlist & 切片
// ============================================================

// TCMaster GET /tc/:job/:token/master.m3u8
func TCMaster(c *gin.Context) {
	jobID := c.Param("job")
	token := c.Param("token")
	job, ok := transcode.Default().Scheduler.Get(jobID)
	if !ok {
		c.String(http.StatusNotFound, "job not found")
		return
	}
	if job.CallbackToken != token {
		c.String(http.StatusForbidden, "bad token")
		return
	}
	if len(job.Profiles) == 0 {
		c.String(http.StatusInternalServerError, "no profiles")
		return
	}
	apiURL := common.GetApiUrlFromRequest(c.Request)
	baseURL := fmt.Sprintf("%s/tc/%s/%s/", apiURL, jobID, token)
	body := transcode.Default().Cache.MasterPlaylist(job.Profiles, baseURL)
	job.Touch()
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	// Master playlist 内容在 job 生命周期内不变，设置较长缓存
	c.Header("Cache-Control", "public, max-age=3600")
	c.String(http.StatusOK, body)
}

// TCPlaylist GET /tc/:job/:token/:profile/playlist.m3u8
// 等待首切片就绪后返回；阻塞最多 30s
func TCPlaylist(c *gin.Context) {
	jobID := c.Param("job")
	token := c.Param("token")
	profile := c.Param("profile")
	mgr := transcode.Default()
	job, ok := mgr.Scheduler.Get(jobID)
	if !ok {
		c.String(http.StatusNotFound, "job not found")
		return
	}
	if job.CallbackToken != token {
		c.String(http.StatusForbidden, "bad token")
		return
	}
	// 【关键】如果 job 之前因 idle 被 cancel，这里重新激活：复用 jobID/token 重新入队
	// 让 worker 再次启动 ffmpeg 转码，避免播放器看到 404 后无法恢复
	if st := job.GetStatus(); st == transcode.JobCancelled || st == transcode.JobFinished || st == transcode.JobFailed {
		if _, ok := mgr.Scheduler.Reactivate(jobID); ok {
			fmt.Printf("[transcode] reactivate job %s on playlist request (was %s)\n", jobID, st)
		}
	}
	job.Touch()
	// 等首切片
	select {
	case <-job.WaitReady():
	case <-time.After(30 * time.Second):
		c.String(http.StatusGatewayTimeout, "timeout waiting first segment")
		return
	}
	if job.Status == transcode.JobFailed {
		c.String(http.StatusInternalServerError, "transcode failed: "+job.Error)
		return
	}
	apiURL := common.GetApiUrlFromRequest(c.Request)
	baseURL := fmt.Sprintf("%s/tc/%s/%s/%s/", apiURL, jobID, token, profile)
	isFinished := job.Status == transcode.JobFinished
	// 【关键】传入总时长，让 BuildPlaylist 能够生成完整切片列表（VOD 模式），
	// 这样 HLS.js 就知道视频真实总时长，避免时长显示错误和拖动跳跃问题。
	body := transcode.Default().Cache.BuildPlaylist(jobID, profile, job.Output.SegmentDuration,
		isFinished, baseURL, job.Probe.Duration)
	c.Header("Content-Type", "application/vnd.apple.mpegurl")
	if isFinished || job.Probe.Duration > 0 {
		// VOD 模式：playlist 完整不变，长缓存
		c.Header("Cache-Control", "public, max-age=3600")
	} else {
		// 没有总时长时退回 EVENT 模式：短缓存
		c.Header("Cache-Control", "public, max-age=2")
	}
	c.String(http.StatusOK, body)
}

// TCSegment GET /tc/:job/:token/:profile/seg-:seq.ts
// 切片就绪则直接返回；否则等待最多 60s
func TCSegment(c *gin.Context) {
	jobID := c.Param("job")
	token := c.Param("token")
	profile := c.Param("profile")
	segName := c.Param("seg")
	mgr := transcode.Default()
	job, ok := mgr.Scheduler.Get(jobID)
	if !ok {
		c.String(http.StatusNotFound, "job not found")
		return
	}
	if job.CallbackToken != token {
		c.String(http.StatusForbidden, "bad token")
		return
	}
	// 【关键】job 之前因 idle 被 cancel 时，复用同一 jobID/token 重新入队启动转码。
	// 这样浏览器持有的旧 master.m3u8 + 切片 URL 仍然有效，暂停再继续也不会 404。
	if st := job.GetStatus(); st == transcode.JobCancelled || st == transcode.JobFinished || st == transcode.JobFailed {
		if _, ok := mgr.Scheduler.Reactivate(jobID); ok {
			fmt.Printf("[transcode] reactivate job %s on segment request (was %s)\n", jobID, st)
		}
	}
	// seg-N.ts
	name := strings.TrimSuffix(strings.TrimPrefix(segName, "seg-"), ".ts")
	seq, err := strconv.Atoi(name)
	if err != nil {
		c.String(http.StatusBadRequest, "bad segment name")
		return
	}
	cache := mgr.Cache
	// 【智能调度】触发对应 chunk 的 ffmpeg 启动（如已运行则只更新 LastAccess）
	// 这是用户拖动进度条到任意位置时能快速响应的关键
	if mgr.Chunks != nil {
		chunkIdx := mgr.Chunks.EnsureChunkRunningForSeg(jobID, profile, seq)
		fmt.Printf("[tc-segment] req seq=%d chunk=%d job=%s\n", seq, chunkIdx, jobID)
	}
	info, err := cache.GetSegment(jobID, profile, seq)
	if err != nil {
		// 等待
		waitStart := time.Now()
		info, err = cache.WaitForSegment(jobID, profile, seq, 60*time.Second)
		if err != nil {
			fmt.Printf("[tc-segment] req seq=%d FAILED after %.1fs: %v\n", seq, time.Since(waitStart).Seconds(), err)
			c.String(http.StatusNotFound, "segment not available: "+err.Error())
			return
		}
		fmt.Printf("[tc-segment] req seq=%d ready after %.1fs\n", seq, time.Since(waitStart).Seconds())
	}
	job.Touch()
	// 切片内容不可变，设置强缓存避免浏览器/HLS.js 反复请求同一切片
	c.Header("Cache-Control", "public, max-age=86400, immutable")
	// 【内存优化】流式发送切片文件，避免 gin c.File() 将整个切片读入内存
	// 注意：不能使用 http.ServeFile，它会自动嗅探 Content-Type 并处理 Range 请求，
	// 与 gin 已写入的响应头冲突，导致 HLS.js 收到异常数据触发 bufferAppendError。
	f, err := os.Open(info.Path)
	if err != nil {
		c.String(http.StatusInternalServerError, "open segment: "+err.Error())
		return
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		c.String(http.StatusInternalServerError, "stat segment: "+err.Error())
		return
	}
	c.DataFromReader(http.StatusOK, fi.Size(), "video/mp2t", f, nil)
}

// ============================================================
// Worker 协议端
// ============================================================

// WorkerRegister POST /api/transcode/worker/register
func WorkerRegister(c *gin.Context) {
	if !transcode.IsEnabled() {
		common.ErrorStrResp(c, "transcode disabled", 403)
		return
	}
	if !checkWorkerSecret(c) {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	var req transcode.RegisterRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	w := transcode.Default().Registry.Register(&req, false)
	common.SuccessResp(c, transcode.RegisterResponse{
		WorkerID:          w.ID,
		HeartbeatInterval: 10,
		ClaimStrategy:     "pull",
		ProtocolVersion:   transcode.ProtocolVersion,
	})
}

// WorkerHeartbeat POST /api/transcode/worker/heartbeat
func WorkerHeartbeat(c *gin.Context) {
	if !checkWorkerSecret(c) {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	var req transcode.HeartbeatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	mgr := transcode.Default()
	if _, ok := mgr.Registry.Heartbeat(&req); !ok {
		common.SuccessResp(c, transcode.HeartbeatResponse{OK: false, Kick: true})
		return
	}
	cancellations := mgr.Scheduler.PopCancellations(req.WorkerID)
	common.SuccessResp(c, transcode.HeartbeatResponse{OK: true, Cancel: cancellations})
}

// WorkerClaim POST /api/transcode/worker/claim
func WorkerClaim(c *gin.Context) {
	if !checkWorkerSecret(c) {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	var req transcode.ClaimRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	wait := time.Duration(req.Wait) * time.Second
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	jobs := transcode.Default().Scheduler.Claim(req.WorkerID, req.Slots, wait)
	common.SuccessResp(c, transcode.ClaimResponse{Jobs: jobs})
}

// WorkerSegmentUpload PUT /api/transcode/worker/segment?job=&profile=&seq=&duration=&final=
func WorkerSegmentUpload(c *gin.Context) {
	jobID := c.Query("job")
	profile := c.Query("profile")
	seq, _ := strconv.Atoi(c.Query("seq"))
	dur, _ := strconv.ParseFloat(c.Query("duration"), 64)
	isFinal, _ := strconv.ParseBool(c.Query("final"))
	job, ok := transcode.Default().Scheduler.Get(jobID)
	if !ok {
		common.ErrorStrResp(c, "job not found", 404)
		return
	}
	// 校验 callback_token (Bearer)
	auth := c.GetHeader("Authorization")
	expect := "Bearer " + job.CallbackToken
	if auth != expect {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	defer c.Request.Body.Close()
	_, err := transcode.Default().Cache.PutSegment(jobID, profile, seq, dur, c.Request.Body, isFinal)
	if err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	if seq == 0 {
		job.MarkReady()
	}
	c.Status(http.StatusNoContent)
}

// WorkerJobFinish POST /api/transcode/worker/job/finish
func WorkerJobFinish(c *gin.Context) {
	var req transcode.FinishRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	job, ok := transcode.Default().Scheduler.Get(req.JobID)
	if !ok {
		common.ErrorStrResp(c, "job not found", 404)
		return
	}
	auth := c.GetHeader("Authorization")
	expect := "Bearer " + job.CallbackToken
	if auth != expect {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	if err := transcode.Default().Scheduler.Finish(&req); err != nil {
		common.ErrorResp(c, err, 500)
		return
	}
	common.SuccessResp(c)
}

// ListTranscodeWorkers GET /api/admin/transcode/workers  (admin)
func ListTranscodeWorkers(c *gin.Context) {
	common.SuccessResp(c, transcode.Default().Registry.List())
}

// ListTranscodeJobs GET /api/admin/transcode/jobs (admin)
func ListTranscodeJobs(c *gin.Context) {
	common.SuccessResp(c, transcode.Default().Scheduler.ListJobs())
}

// CancelTranscodeJob POST /api/admin/transcode/cancel
func CancelTranscodeJob(c *gin.Context) {
	var req struct {
		JobID string `json:"job_id" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		common.ErrorResp(c, err, 400)
		return
	}
	transcode.Default().Scheduler.Cancel(req.JobID)
	common.SuccessResp(c)
}

func checkWorkerSecret(c *gin.Context) bool {
	auth := c.GetHeader("Authorization")
	auth = strings.TrimPrefix(auth, "Bearer ")
	return transcode.VerifyWorkerSecret(auth)
}
