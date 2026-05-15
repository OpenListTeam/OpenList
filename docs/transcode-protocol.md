# OpenList 转码协议 v1（自研）

> 用于 OpenList Master 与远程 FFmpeg 转码 Worker 之间的通信。
> 协议本身、所有依赖、推荐编码 (H.264/AV1) 均为开源免费，可商用。

## 总览

```
Player ──► Master(/api/transcode/play) ──► Scheduler.Submit
Worker ──► Master(/api/transcode/worker/register)             # 启动一次
Worker ──► Master(/api/transcode/worker/heartbeat)            # 周期 10s
Worker ──► Master(/api/transcode/worker/claim)                # 长轮询拉任务
Worker ──► Master(/api/transcode/worker/segment)              # 流式回推切片
Worker ──► Master(/api/transcode/worker/job/finish)           # 上报完成
Player ──► Master(/tc/{job}/{token}/master.m3u8)              # 拉取播放列表
Player ──► Master(/tc/{job}/{token}/{profile}/seg-N.ts)       # 拉取切片
```

## 通用约定
- 所有请求体均为 JSON。
- Worker 与 Master 间通过两类凭据鉴权：
  - **shared_secret**（管理端 `transcode_worker_secret` 设置项）：用于 register / heartbeat / claim
  - **callback_token**（Master 在 job 中下发，一次性）：用于 segment / job/finish
- Header 形式：`Authorization: Bearer <token>`
- 返回包统一使用 OpenList 通用响应：`{"code":200,"message":"success","data":{...}}`

## 端点详细

### POST /api/transcode/worker/register
> 鉴权：Bearer = shared_secret

请求：
```json
{
  "name": "gpu-node-01",
  "version": "1.0.0",
  "capacity": 4,
  "hwaccel": ["nvenc"],
  "codecs_decode": ["h264","hevc","av1","vp9"],
  "codecs_encode": ["h264","hevc"],
  "max_resolution": "3840x2160",
  "tags": ["linux","amd64"]
}
```

响应：
```json
{
  "code":200,"message":"success",
  "data": {
    "worker_id":"wk_xxx",
    "heartbeat_interval":10,
    "claim_strategy":"pull",
    "protocol_version":"v1"
  }
}
```

### POST /api/transcode/worker/heartbeat
> 鉴权：Bearer = shared_secret
> 间隔：register 返回的 `heartbeat_interval`；服务端 30s 未收到则剔除 worker

请求：
```json
{ "worker_id":"wk_xxx", "load":0.4, "running":["job_a"], "free_slots":3 }
```

响应：
```json
{ "code":200,"message":"success","data":{"ok":true,"kick":false,"cancel":["job_x"]}}
```
- `kick=true`：服务端要求 Worker 主动退出
- `cancel`：需要 Worker 立刻终止的 job_ids（异步取消通道）

### POST /api/transcode/worker/claim
> 鉴权：Bearer = shared_secret
> 长轮询：当无任务时，服务端最多挂起 `wait` 秒（最大 30）

请求：
```json
{ "worker_id":"wk_xxx", "slots":1, "wait":20 }
```

响应：
```json
{ "code":200,"message":"success","data":{"jobs":[{
  "id":"job_xxx",
  "path":"/movies/x.mkv",
  "source_url":"https://example.com/d/movies/x.mkv?sign=...",
  "profiles":[{
    "name":"1080p","video_codec":"h264","video_bitrate":"4000k",
    "scale":"1920:-2","audio_codec":"aac","audio_bitrate":"160k",
    "hwaccel":"nvenc"
  }],
  "output":{"format":"hls","segment_duration":6},
  "callback_token":"tk_xxx",
  "deadline":1731601234
}]}}
```

### PUT /api/transcode/worker/segment
> 鉴权：Bearer = callback_token
> Query 参数：
> - `job` 任务 ID
> - `profile` profile 名（如 `1080p`）
> - `seq` 切片序号（从 0 开始）
> - `duration` 该切片秒数
> - `final` 是否为最后一片（true/false）
>
> Body：原始 MPEG-TS 二进制（`Content-Type: video/mp2t`）

响应：`204 No Content`

### POST /api/transcode/worker/job/finish
> 鉴权：Bearer = callback_token

请求：
```json
{
  "job_id":"job_xxx",
  "status":"finished",
  "error":"",
  "stats":{"elapsed":312.4,"fps":120,"speed":2.1,"bytes_out":890123456}
}
```
status 取值：`finished` / `failed` / `cancelled`

## 播放端

### POST /api/fs/transcode/play
> 由前端播放器调用，用户登录态可见
> 请求：`{"path":"/movies/x.mkv"}`
>
> 响应（无需转码）：
> `{"transcode":false,"reason":"size below threshold"}`
>
> 响应（已下发转码任务）：
> `{"transcode":true,"job_id":"job_xx","master_url":"https://.../tc/.../master.m3u8","profile":"1080p"}`

### GET /tc/:job/:token/master.m3u8
返回多档位主 m3u8。

### GET /tc/:job/:token/:profile/playlist.m3u8
阻塞等待首切片就绪（最多 30s）后返回 HLS 播放列表，未结束时不带 `#EXT-X-ENDLIST`。

### GET /tc/:job/:token/:profile/seg-N.ts
返回 N 号切片；如果尚未生成，最多等待 60s。

## 启动远程 Worker

```bash
# 用 Master 配置中的 transcode_worker_secret 作为密钥
go build -o openlist-worker ./cmd/worker

./openlist-worker \
  -master http://openlist.example.com:5244 \
  -secret <transcode_worker_secret> \
  -capacity 2 \
  -hwaccel nvenc \
  -ffmpeg /usr/bin/ffmpeg \
  -workdir /tmp/openlist-worker
```

## License 说明
- 协议：自研，可自由使用
- FFmpeg：使用系统包提供的 LGPL 版本即可（含 libx264/libx265，libx264 是 GPL，建议在 Worker 镜像中遵循 GPL 公开/动态链接合规）
- 推荐编码：H.264 互联网分发免授权 / AV1 永久免版税
