package handles

import (
	"crypto/subtle"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// 探测阶段的独立并发上限。
//
// 转码配额（每用户 3 个活跃任务）只约束「入队」，而配额判定发生在探测之后：
// 并发请求可能同时通过配额预检、各起一个最长 30s 的 ffprobe。这里给探测单独
// 设一道闸，保证这段窗口内的进程数有界。每用户上限取任务配额值，正常并发播放
// 不会被误伤（用户有 3 个任务时预检已经先拒绝了）。
const (
	maxConcurrentProbes = 8
	maxProbesPerUser    = transcode.MaxActiveJobsPerUser
)

type probeLimiter struct {
	mu     sync.Mutex
	active int
	users  map[string]int
}

var probeSlots = &probeLimiter{users: make(map[string]int)}

func (p *probeLimiter) acquire(owner string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active >= maxConcurrentProbes || p.users[owner] >= maxProbesPerUser {
		return false
	}
	p.active++
	p.users[owner]++
	return true
}

func (p *probeLimiter) release(owner string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.active > 0 {
		p.active--
	}
	if n := p.users[owner]; n <= 1 {
		delete(p.users, owner)
	} else {
		p.users[owner] = n - 1
	}
}

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
	// 该路由挂在 Auth(true)（允许被禁用的访客通过）之下，必须在这里自行拒绝，
	// 否则关闭访客后匿名请求仍能触发 ffprobe/ffmpeg 并占用转码缓存。
	user := mediaUser(c)
	if user == nil || (user.IsGuest() && user.Disabled) {
		common.ErrorStrResp(c, "Guest user is disabled, login please", 401)
		return
	}
	if !CanUserAccessPath(user, req.Path) {
		common.ErrorStrResp(c, "path is not accessible", 403)
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

	// apiURL 仅用于返回给浏览器的 master_url（需要跟随请求 Host 才能被客户端访问）。
	apiURL := common.GetApiUrlFromRequest(c.Request)

	// 配额键：普通用户按 ID，访客按 IP——访客共用一个账号，
	// 否则一个匿名访客就能占满全部匿名转码配额。
	ownerID := "user:" + strconv.FormatUint(uint64(user.ID), 10)
	if user.IsGuest() {
		ownerID = "guest:" + c.ClientIP()
	}

	// 同一用户重复请求同一文件：直接复用进行中的任务，避免重复 ffprobe 与重复转码。
	if exist, ok := mgr.Scheduler.FindReusable(ownerID, req.Path); ok {
		respondTranscodeJob(c, apiURL, exist)
		return
	}

	// 【内存优化】优先尝试解析为本地文件路径，让 ffmpeg 直接读取本地文件，
	// 完全绕过 HTTP /d 代理路径——避免 net.Downloader 给每个 range 请求分配
	// 高达 MaxBufferLimit (默认系统内存 5%) 的大缓冲。这是降低 Go 进程内存
	// 占用最有效的方法。如果不是本地驱动则退回到 HTTP 签名 URL。
	var sourceURL string
	if localFile, ok := resolveLocalFilePath(req.Path); ok {
		// ffmpeg 接受本地路径作为输入，无需 file:// 前缀
		sourceURL = localFile
		fmt.Printf("[transcode] using local file path for ffmpeg: %s\n", localFile)
	} else {
		// 远程驱动：构造签名 HTTP URL。
		// 这个 URL 会被服务端 ffmpeg 直接访问，绝不能使用 Host / X-Forwarded-Host：
		// 未配置 site_url 时攻击者可借此让服务器去拉取任意地址（SSRF）。
		base := trustedInternalBaseURL()
		if base == "" {
			// 仅 HTTPS 监听且未配置 site_url 时无可用基址；
			// 若继续拼出 "/d/..." 会被当成绝对本地路径，ffmpeg 只会报文件不存在。
			common.ErrorStrResp(
				c,
				"transcode needs site_url (or an HTTP port) to fetch the source",
				503,
			)
			return
		}
		signedPath := sign.Sign(req.Path)
		sourceURL = fmt.Sprintf("%s/d%s?sign=%s", base, encodePath(req.Path), signedPath)
	}
	if !transcode.ValidSourceInput(sourceURL) {
		common.ErrorStrResp(c, "unsupported transcode source", 400)
		return
	}

	// 创建 Job
	job := transcode.NewJob()
	job.Path = req.Path
	job.OwnerID = ownerID
	job.SourceURL = sourceURL
	// 配额预检：ProbeSource 会起一个最长 30s 的 ffprobe 子进程，
	// 不能等探测完才告诉用户「已有 3 个活跃任务」。最终判定仍由 SubmitLimited 原子完成。
	if err := mgr.Scheduler.Capacity(ownerID); err != nil {
		common.ErrorResp(c, err, 429)
		return
	}
	if !probeSlots.acquire(ownerID) {
		common.ErrorStrResp(c, "too many concurrent transcode requests", 429)
		return
	}
	// 【关键】探测视频总时长（异步会让首响应变慢，但只有一次，必须等待）
	// 总时长用于生成完整的 playlist，避免 HLS.js 时长显示错误和拖动跳跃问题
	probe, perr := func() (transcode.SourceProbe, error) {
		// 用 defer 释放：探测内部 panic 时也不能把槽位永久占住
		defer probeSlots.release(ownerID)
		return transcode.ProbeSource(sourceURL)
	}()
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
	submitted, _, err := mgr.Scheduler.SubmitLimited(job)
	if err != nil {
		// 队列已满 / 用户配额超限：返回 429，前端可降级为直链播放
		common.ErrorResp(c, err, 429)
		return
	}
	respondTranscodeJob(c, apiURL, submitted)
}

// respondTranscodeJob 返回转码任务的播放信息
func respondTranscodeJob(c *gin.Context, apiURL string, job *transcode.Job) {
	profileName := ""
	if len(job.Profiles) > 0 {
		profileName = job.Profiles[0].Name
	}
	masterURL := fmt.Sprintf("%s/tc/%s/%s/master.m3u8", apiURL, job.ID, job.PlayToken)
	common.SuccessResp(c, gin.H{
		"transcode":  true,
		"job_id":     job.ID,
		"master_url": masterURL,
		"profile":    profileName,
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

// trustedInternalBaseURL 返回供服务端 ffmpeg 访问本站 /d 接口的可信基址，不读取任何请求头。
// 优先使用管理员配置的 site_url；未配置时回环到本机实际监听的 HTTP 端口。
// 两者都不可用（例如仅监听 Unix socket）时返回空串，调用方应拒绝转码。
func trustedInternalBaseURL() string {
	if site := strings.TrimSuffix(conf.Conf.SiteURL, "/"); strings.HasPrefix(site, "http") {
		return site
	}
	if p := conf.Conf.Scheme.HttpPort; p > 0 {
		return fmt.Sprintf("http://127.0.0.1:%d", p)
	}
	return ""
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
	if !tokenMatches(job.PlayToken, token) {
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
	if !tokenMatches(job.PlayToken, token) {
		c.String(http.StatusForbidden, "bad token")
		return
	}
	if !transcode.ValidProfileName(profile) {
		c.String(http.StatusBadRequest, "bad profile")
		return
	}
	// 【关键】如果 job 之前因 idle 被 cancel，这里重新激活：复用 jobID/token 重新入队
	// 让 worker 再次启动 ffmpeg 转码，避免播放器看到 404 后无法恢复。
	// 配额/队列已满时不再起新的 ffmpeg，但 playlist 与已缓存的切片仍应可读：
	// 这里只记下 quotaBlocked，交给切片请求在「缓存也没有」时才返回 429。
	quotaBlocked := false
	if st := job.GetStatus(); st == transcode.JobCancelled || st == transcode.JobFinished || st == transcode.JobFailed {
		if _, rerr := mgr.Scheduler.Reactivate(jobID); rerr == nil {
			fmt.Printf("[transcode] reactivate job %s on playlist request (was %s)\n", jobID, st)
		} else if errors.Is(rerr, transcode.ErrUserQuota) || errors.Is(rerr, transcode.ErrQueueFull) {
			quotaBlocked = true
			fmt.Printf("[transcode] reactivate job %s on playlist request rejected (%v), serving from cache only\n", jobID, rerr)
		}
	}
	job.Touch()
	// 等首切片；配额已满时无法启动新的转码，没必要干等 30s
	if !quotaBlocked {
		select {
		case <-job.WaitReady():
		case <-time.After(30 * time.Second):
			c.String(http.StatusGatewayTimeout, "timeout waiting first segment")
			return
		}
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
	if !tokenMatches(job.PlayToken, token) {
		c.String(http.StatusForbidden, "bad token")
		return
	}
	if !transcode.ValidProfileName(profile) {
		c.String(http.StatusBadRequest, "bad profile")
		return
	}
	// 【关键】job 之前因 idle 被 cancel 时，复用同一 jobID/token 重新入队启动转码。
	// 这样浏览器持有的旧 master.m3u8 + 切片 URL 仍然有效，暂停再继续也不会 404。
	// 配额/队列已满时不重新起 ffmpeg，但已缓存的切片依然要能命中（回看已转完的视频）。
	quotaBlocked := false
	if st := job.GetStatus(); st == transcode.JobCancelled || st == transcode.JobFinished || st == transcode.JobFailed {
		if _, rerr := mgr.Scheduler.Reactivate(jobID); rerr == nil {
			fmt.Printf("[transcode] reactivate job %s on segment request (was %s)\n", jobID, st)
		} else if errors.Is(rerr, transcode.ErrUserQuota) || errors.Is(rerr, transcode.ErrQueueFull) {
			quotaBlocked = true
			fmt.Printf("[transcode] reactivate job %s on segment request rejected (%v), serving from cache only\n", jobID, rerr)
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
	// 这是用户拖动进度条到任意位置时能快速响应的关键。
	// 配额已满时不能触发新的 ffmpeg，否则「限制并发」就没有意义了。
	if mgr.Chunks != nil && !quotaBlocked {
		chunkIdx := mgr.Chunks.EnsureChunkRunningForSeg(jobID, profile, seq)
		fmt.Printf("[tc-segment] req seq=%d chunk=%d job=%s\n", seq, chunkIdx, jobID)
	}
	info, err := cache.GetSegment(jobID, profile, seq)
	if err != nil {
		if quotaBlocked {
			// 配额已满且该切片不在缓存里：只能拒绝，让前端降级为直链播放
			c.String(http.StatusTooManyRequests, "too many active transcode jobs")
			return
		}
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
	// 校验 callback_token (Bearer)，使用恒定时间比较避免时序侧信道
	auth := c.GetHeader("Authorization")
	expect := "Bearer " + job.CallbackToken
	if subtle.ConstantTimeCompare([]byte(auth), []byte(expect)) != 1 {
		common.ErrorStrResp(c, "unauthorized", 401)
		return
	}
	// profile 会被拼进缓存目录路径，必须是白名单字符，防止 ../ 路径穿越
	if !transcode.ValidProfileName(profile) {
		common.ErrorStrResp(c, "invalid profile", 400)
		return
	}
	// 只接受任务声明过的档位，避免持有 callback_token 的一方往任意档位目录写数据
	if !jobHasProfile(job, profile) {
		common.ErrorStrResp(c, "profile not declared for this job", 400)
		return
	}
	defer c.Request.Body.Close()
	_, err := transcode.Default().Cache.PutSegment(jobID, profile, seq, dur, c.Request.Body, isFinal)
	if err != nil {
		switch err {
		case transcode.ErrInvalidProfile, transcode.ErrInvalidJobID, transcode.ErrInvalidSegment:
			common.ErrorResp(c, err, 400)
		case transcode.ErrSegmentTooLarge:
			common.ErrorResp(c, err, 413)
		default:
			common.ErrorResp(c, err, 500)
		}
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
	if !tokenMatches("Bearer "+job.CallbackToken, auth) {
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

// jobHasProfile 判断 profile 是否属于任务声明的输出档位
func jobHasProfile(job *transcode.Job, profile string) bool {
	if job == nil {
		return false
	}
	for _, p := range job.Profiles {
		if p.Name == profile {
			return true
		}
	}
	return false
}

// tokenMatches 恒定时间比较凭据，避免按字节比较造成的时序侧信道
func tokenMatches(expected, got string) bool {
	if expected == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(expected), []byte(got)) == 1
}

func checkWorkerSecret(c *gin.Context) bool {
	auth := c.GetHeader("Authorization")
	auth = strings.TrimPrefix(auth, "Bearer ")
	return transcode.VerifyWorkerSecret(auth)
}
