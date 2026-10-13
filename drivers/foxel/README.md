# Foxel

将 [Foxel](https://github.com/DrizzleTime/Foxel) 的虚拟文件系统接入 OpenList。重新编译或使用包含此驱动的 OpenList 后，在「管理 → 存储 → 添加」中选择 **Foxel**。

## 配置

| 字段 | 说明 |
| --- | --- |
| 挂载路径 | Foxel 在 OpenList 中显示的位置，例如 /foxel。 |
| url | Foxel 服务地址，例如 https://foxel.example.com；不要添加 /api。支持反向代理路径前缀，例如 https://example.com/foxel。 |
| root_folder_path | Foxel 中的虚拟目录，例如 / 或 /cloud/documents。它不是服务器上的本地磁盘路径。 |
| username / password | Foxel 用户名和密码。同时填写时优先使用这组凭据，访问令牌过期后自动重新登录。 |
| token | 可选的 Foxel Bearer access_token，可直接填写令牌或 Bearer 加空格和令牌。仅填写令牌时，过期后需要手动更新。 |
| page_size | 每次请求的目录条数，默认 200，范围 1–500。驱动自动获取全部页，支持游标分页。 |
| link_expire | 临时下载链接有效期，单位秒，默认 3600。OpenList 缓存会提前过期。 |

Foxel 账号需要对挂载目录有相应的读取、写入和删除权限。账号密码登录使用 /api/auth/login 的表单接口；手动令牌可从该接口响应的 access_token 获取。

## 支持的操作

- 目录浏览、子目录访问及本地排序。
- 临时链接下载及范围请求；尊重 Foxel 配置的 FILE_DOMAIN。
- 原始流上传、覆盖上传、上传进度、服务器上传限速及请求取消。
- 创建目录、重命名、移动、复制和删除。
- Foxel 跨挂载复制、移动会等待任务队列完成，并返回任务失败原因。

下载使用 /api/fs/download-public/{token}/{filename}，保留 RAW 照片等文件的原始字节。Foxel 实例须包含该原始文件下载接口；旧版本请先升级。此驱动对接的是 Foxel 虚拟文件 API，无需开启 Foxel 的 WebDAV 或 S3 映射。

取消 OpenList 中的等待或中断连接，会停止本地请求和任务状态轮询。Foxel 已接受的跨挂载后台任务仍由 Foxel 执行，因为其当前 API 没有取消任务的接口。复制、移动和重命名不覆盖既有目标；上传按 OpenList 的覆盖上传语义处理。

## 开发验证

使用仓库 go.mod 中要求的 Go 工具链：

    go test ./drivers/foxel
    go test -race ./drivers/foxel
    go vet ./drivers/foxel
    go build ./drivers

自动测试使用模拟 Foxel HTTP 服务验证认证、分页、路径编码、原始文件下载、上传和异步任务行为。实际部署还需使用自己的 Foxel 实例和存储适配器验证权限及其支持的文件操作。
