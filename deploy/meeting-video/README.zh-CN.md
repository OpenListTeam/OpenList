# 会议视频下载工具

独立工具名为“会议视频下载工具”，出现在既有离线下载工具列表中。
输入飞书妙记分享链接、腾讯会议分享链接或 MP4/m3u8 媒体直链；Cookie 可选。
有 Cookie 就按该会话解析；没有 Cookie 就实际尝试匿名访问，无法解析或下载时明确失败。

## 工作方式

按站点解析真实媒体地址 → HTTP 下载 MP4 或并发下载 HLS 分片 → 无损封装为 MP4 → 核对轨道、时长 → 现有 OpenList 离线转存。
浏览器只用来观察播放器的媒体请求，取得地址后立即关闭；不等待视频播放完。
不推 RTMP、不使用 `-re`、不重新编码音视频。
下载速度受文件大小、源站限速和服务器带宽影响，不能保证固定倍速。

支持已结束的 HLS 点播，缺失分片会失败，不跳过后生成残缺文件；不支持直播、DRM 或任意网站解析。
平台分享页解析需用实际账号权限验收，Cookie 不能替代下载权限。平台改版或只提供预览时可能失败。

## 构建与运行

先构建包含新前端的 Linux 后端 `bin/openlist-linux-amd64`，然后在源码根目录执行：

```sh
docker build -f deploy/meeting-video/Dockerfile -t openlist-meeting-video:local .
```

构建阶段安装 Node、Chromium、playwright-core、FFmpeg 和 Python 下载依赖。
用户 ID、数据卷、端口、已有存储和中文前端挂载继续使用原部署配置。
若原部署配置了自定义 `dist_dir`，必须同时更新那份前端文件，否则看不到 Cookie 输入框。

自行安装可通过环境变量配置运行路径：

| 环境变量 | 默认值 |
| --- | --- |
| OPENLIST_MEETING_NODE | node |
| OPENLIST_MEETING_BROWSER | /usr/bin/chromium-browser |
| OPENLIST_MEETING_MODULE_DIR | /opt/openlist/browser |
| OPENLIST_MEETING_PYTHON | /opt/openlist/meeting/bin/python3 |
| OPENLIST_MEETING_FFMPEG | ffmpeg |
| OPENLIST_MEETING_FFPROBE | ffprobe |

非默认 UID 运行时应提供可写的 HOME，供 Chromium 创建运行配置。
工具启动时检测依赖；依赖不全不会在可用工具列表里显示，后台初始化日志会给出原因。

## 凭据与任务

Cookie 只在本次请求使用，以任务 ID 绑定的 AES-GCM 密文保存在既有任务队列。
密钥位于数据卷 `meeting-video/cookie.key`，需要与队列一起备份；密钥丢失后重新提交任务。
真实媒体 URL 和 Cookie 不传入外部程序参数。下载器只连接进程内回环代理，代理按来源限制授权头并拒绝公网源跳转内网。

继续使用现有下载/转存队列配置。需要重启恢复时开启两者的 `task_persistant`。
不完整下载重试会重新解析、重新下载，避免把过期链接或变化的媒体续接到旧文件。
已经校验完成的缓存重试先核对 SHA-256；不重复下载完整缓存。
仅支持“保留缓存”及“上传成功后删除”，避免转存失败时丢失已下载视频。
