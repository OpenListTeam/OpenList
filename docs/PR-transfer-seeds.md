# 可移植传输种子（Transfer Seeds）——跨云盘零传输搬运

| 项目 | 内容 |
|---|---|
| **分支** | `feat/advanced-transfer-seeds` |
| **目标分支** | `main` |
| **合并基点** | `2d51c9ab` |
| **改动规模** | 45 个文件，+5310 / −308 |
| **类型** | 新特性（Feature） |
| **破坏性变更** | 无 |

---

## 一、摘要

本 PR 为 OpenList 引入**可移植传输种子体系**，把「文件身份」与「文件内容」解耦。

用一个描述性的种子文件（几百字节，只含哈希与来源）替代文件本体，让接收端依据种子元数据，在目标云盘上直接**秒传**（hash-driven rapid upload / CAS）或通过分享链接转存，实现跨云盘搬运的**零字节传输**。

同时提供三种容器格式（OSS / torrent / CAS）互相转换、哈希能力探测、四段式降级链，以及 12 个云盘驱动的秒传实现。

**核心收益**：跨云盘搬运大文件由「下载 N GB + 上传 N GB」降级为「传递几百字节元数据」。

---

## 二、背景与动机

### 2.1 现状痛点

OpenList 长期存在结构性痛点：**跨存储搬运大文件代价极高**。

用户想把云盘 A 上的 10GB 视频放到云盘 B，当前只能完整下载再完整上传：
- 耗时取决于上行带宽（家用宽带上行 10MB/s 时约 17 分钟）
- 消耗双倍流量
- 中转机器磁盘需要容纳完整文件

### 2.2 可用的技术基础

绝大多数主流云盘支持 **哈希秒传（CAS, Content-Addressable Storage）**：客户端提交文件哈希，服务端发现该数据已存在，直接「挂载」出新文件对象，**零字节传输**。

障碍在于：
1. 各云盘的秒传哈希算法、分片规则、协议字段**互不相同**；
2. 没有任何标准格式可以描述「一个文件是什么、它的哈希是什么、去哪能拿到它」。

### 2.3 现有实现的局限

`main` 上已有 torrent 相关能力，但存在明确局限：

| 局限 | 说明 |
|---|---|
| 秒传逻辑硬编码 | 仅服务 189pc 一种 CAS 流程，其他云盘无法复用 |
| 格式单一 | 只有 torrent，无法表达 OSS/CAS 语义 |
| 无能力探测 | 调用方需硬编码「哪个驱动支持哪种哈希」 |
| 无降级链 | 秒传失败即失败，不会尝试其他路径 |
| 无来源体系 | 种子不含「去哪拿内容」，无法跨实例传递 |

### 2.4 设计目标

1. **可移植**：种子脱离生成实例后仍可用；
2. **可扩展**：新增云盘只需实现接口，不改编排逻辑；
3. **可降级**：秒传 → PutURL → 离线下载，逐级回退；
4. **零传输优先**：纯哈希驱动完全跳过内容下载。

---

## 三、修改方案

### 3.1 整体架构

```
┌──────────────────────────────────────────────────────────────┐
│ 表现层  server/handles/torrent.go                     +1524  │
│   REST API · 权限校验 · 限流 · 来源校验(SSRF) · 路径编排      │
├──────────────────────────────────────────────────────────────┤
│ 编排层  internal/fs/seed_generate.go                   +603  │
│   单遍流式读取 → 哈希矩阵 → 多容器编码 → 分享自动创建/回滚    │
├──────────────────────────────────────────────────────────────┤
│ 格式层  pkg/torrent/                                  +1241  │
│   三容器统一契约：OSS(JSON) / torrent(bencode) / CAS(base64) │
│   互相可转换 + 严格校验 + 恶意输入防护                        │
├──────────────────────────────────────────────────────────────┤
│ 驱动层  internal/driver/driver.go  + 12 个驱动 +88~126       │
│   SeedRapidUploader 能力接口 + 各云盘协议实现                │
└──────────────────────────────────────────────────────────────┘
```

### 3.2 三容器统一契约

`pkg/torrent/torrent.go` 定义规范模型 `Seed`，三种容器通过 `EncodeSeed` / `DecodeSeed` 互转：

| 容器 | 格式 | 适用场景 | 特点 |
|---|---|---|---|
| **OSS** | JSON | OpenList 原生交换 | 字段最全，人类可读 |
| **torrent** | bencode | BT 生态兼容 | 可被任意 BT 客户端识别 |
| **CAS** | base64(JSON) | 兼容历史客户端 | 保留 5 个 legacy 字段 |

**关键设计：扩展信息放在 `info` 字典之外。**

```
torrent
├── info          ← 标准 BT 字段（piece length / pieces / name / length）
│                   参与 info_hash 计算
├── x-openlist    ← 本特性扩展（哈希矩阵、来源、CAS 元数据）
└── x-cas         ← CAS 扩展
```

自产 torrent 的 **`info_hash` 与纯 BT 种子完全一致**，不污染标准字段，可被标准客户端正常解析与做种。

### 3.3 哈希矩阵与单遍扫描

各云盘认不同哈希：

| 云盘 | 整文件哈希 | 分片 |
|---|---|---|
| 189 / 189pc / 189_tv | MD5 | 分片 MD5 |
| 115 | SHA1 + 128KB 前导 SHA1 | — |
| 123 / 123_open | MD5(Etag) / SHA1 | — |
| 百度网盘 | MD5 | — |
| 阿里云盘 | SHA1 | — |
| 夸克 | MD5 + SHA1 | — |
| PikPak / 迅雷系 | GCID | — |

`SeedHashMatrix` 允许按需勾选，`HashWriter` 在一次 `io.Copy` 中**并发算出全部 8 类哈希**（整文件 4 种 + 分片 4 种），避免对大文件反复读盘。

### 3.4 能力探测与四段降级链

驱动通过接口声明能力，而非硬编码驱动清单：

```go
type SeedRapidUploader interface {
    RapidUploadByHashes(ctx, dstDir model.Obj, req *SeedRapidUploadRequest, overwrite bool) (model.Obj, error)
    RapidHashAlgos() []utils.HashType
    RapidHashNeedsPieces() bool
}
```

`saveSeedFilesToPath` 按序尝试：

```
① 秒传（种子哈希 ∩ 驱动支持的算法）
     ↓ 不可用 / 失败
② PutURL（服务端直传）
     ↓ 不可用
③ 离线下载（需 CanAddOfflineDownloadTasks 权限 + 已配置离线工具）
     ↓ 不可用
④ unavailable（返回明确原因，不静默失败）
```

每个文件独立决策，结果逐条返回，部分成功不影响其余。

### 3.5 延迟内容源

部分云盘的秒传协议仍需读一小段内容：

- **115**：文件头 128KB 的 SHA1（`pre_hash`）
- **阿里云盘 / 夸克**：按 `proof_range` 读取的 `proof_code`

因此 `seedContentOpener` 返回**惰性闭包**，只有真正需要的驱动才触发网络拉取。纯哈希驱动（如 189pc）完全跳过下载，实现真正的零传输。

### 3.6 种子来源与匿名性（重要设计约束）

种子中嵌入的下载来源有两种：

| 类型 | URL 形态 | 取内容是否需要身份 |
|---|---|---|
| `openlist-direct` | `{site}/d/{path}` | **否** |
| `openlist-share` | `{site}/sd/{shareID}` | **否** |

**这是刻意的设计约束，不是缺陷**：

1. 种子的意义在于**可移植**——用户把种子发给别人、或贴到别的实例上，对方实例不可能持有本实例的会话凭据。
2. 因此嵌入的来源 URL **必须对匿名请求可用**。这也是为什么：
   - `directSourceAvailable()` 在 `sign_all` 开启时**拒绝**嵌入 direct 来源（签名 URL 携带身份且会过期）；
   - 开启全局签名时，必须退回**分享**（`/sd/` 支持匿名访问）。
3. **绝不能**在拉取种子内容时附加 `Authorization` / Cookie / `sign` 参数——加了就把种子绑死在生成它的实例上，彻底破坏可移植性。

> 该约束直接决定了安全修复方案：**只能做「限制能去哪」，不能做「带上身份」**。

### 3.7 无副作用回滚

生成种子时若要求嵌入分享链接，系统会自动调用 `CreateSharing`。若后续步骤失败，通过 `defer` + `keepCreatedShares` 标志回收已创建的分享 ID，避免在用户账号里遗留垃圾分享。

### 3.8 上传时自动生成旁挂种子（Sidecar）

`server/handles/fsup.go` 新增可选能力：上传文件时同步计算哈希并写入旁挂种子文件。

```go
seedHasher := torrent.NewHashWriter(seedPieceSize(c), seedPieceSize(c), 0)
uploadReader = io.TeeReader(c.Request.Body, seedHasher)   // 边上传边算哈希，零额外 IO
...
writeUploadSeedSidecar(c, dir, name, size, seedHasher)
```

策略判定优先级（`shouldGenerateUploadSeed`）：

```
请求头 X-Seed-Sidecars / X-Generate-Seed
   ↓ 未指定
存储级 storage.seed_policy
   ↓ inherit
全局 seed_auto_generate_policy
```

命中条件为 `on/true/1` **且** 已配置 `seed_format_policies`。

> 注：异步任务模式（`as_task=true`）与旁挂生成互斥，会返回 400 明确报错。

### 3.9 安全加固（评审后修复）

评审中发现并修复以下问题，详见 §六。

| 级别 | 问题 | 修复 |
|---|---|---|
| 严重 | SSRF：重定向绕过来源白名单 | 逐跳校验 + 限跳数 + 禁降级 |
| 严重 | 内容拉取全量缓冲 1GB + 丢弃 ctx | 流式 + `NewRequestWithContext` |
| 主要 | Host 比对未规范化端口 | `sameSeedHost` 按 hostname+有效端口 |
| 主要 | 多文件种子秒传提交矛盾数据 | 显式拒绝 + nil 保护 |
| 次要 | sliceMd5 规则分散 5 处 | 收敛为 `SliceMD5FromPieces` |
| 次要 | bencode 上限不自洽 | 统一到 `DefaultMaxSeedSize` |

---

## 四、改动文件

### 4.1 新增文件（15 个）

| 文件 | 行数 | 说明 |
|---|---|---|
| `internal/fs/seed_generate.go` | +603 | 种子生成编排（单遍扫描、分享回滚） |
| `internal/driver/seed_stream.go` | +72 | 仅哈希的 `FileStreamer` 实现 |
| `pkg/torrent/torrent.go` | +1045 | 三容器契约与转换（大改动） |
| `server/handles/torrent_seed_test.go` | +259 | 来源校验与 SSRF 回归测试 |
| `pkg/torrent/seed_test.go` | +160 | 种子功能测试 |
| `pkg/torrent/seed_security_test.go` | +156 | 恶意输入与一致性测试 |
| `drivers/115/seed_rapid.go` | +74 | 115 秒传（SHA1 + pre_hash） |
| `drivers/123/seed_rapid.go` | +73 | 123 云盘秒传（MD5） |
| `drivers/quark_open/seed_rapid.go` | +60 | 夸克秒传（MD5+SHA1+proof） |
| `drivers/pikpak/seed_rapid.go` | +58 | PikPak 秒传（GCID） |
| `drivers/thunder/seed_rapid.go` | +56 | 迅雷秒传（GCID） |
| `drivers/thunder_browser/seed_rapid.go` | +55 | 迅雷浏览器秒传 |
| `drivers/thunderx/seed_rapid.go` | +55 | 迅雷X秒传 |
| `drivers/123_open/seed_rapid.go` | +55 | 123 开放平台秒传 |
| `drivers/189_tv/seed_rapid.go` | +35 | 189 电视秒传 |

其余驱动实现：`drivers/189pc/seed_rapid.go`、`drivers/baidu_netdisk/seed_rapid.go`、`drivers/aliyundrive_open/seed_rapid.go` 亦为新增。

### 4.2 核心修改（按模块）

**接口与错误定义**

| 文件 | 改动 | 说明 |
|---|---|---|
| `internal/driver/driver.go` | +52 | `SeedRapidUploadRequest` 结构体 + `SeedRapidUploader` 接口 |
| `internal/errs/driver.go` | +12 | `ErrUnavailableHash` / `ErrEmptyHash` / `ErrHashMismatch` / `ErrRapidUploadFailed` |
| `internal/model/storage.go` | +1 | `Storage.SeedPolicy` 字段（默认 `inherit`） |
| `internal/op/driver.go` | +8 | 存储配置项 `seed_policy`（inherit/on/off） |

**格式层**

| 文件 | 改动 | 说明 |
|---|---|---|
| `pkg/torrent/torrent.go` | +1045 | 三容器契约、转换、校验、诊断 |
| `pkg/torrent/hash_writer.go` | +151 | 8 路并发哈希、`SliceMD5FromPieces` 统一 |
| `pkg/torrent/bencode.go` | +45 | 解析加固、上限收敛 |
| `pkg/torrent/generate.go` | +41 | 生成侧适配 |

**表现层**

| 文件 | 改动 | 说明 |
|---|---|---|
| `server/handles/torrent.go` | +1524 | 种子 API、来源校验、SSRF 防护、降级编排 |
| `server/handles/fsup.go` | +148 | 上传旁挂种子生成（`io.TeeReader`） |
| `server/handles/fsmanage.go` | +107 | 右键菜单种子相关操作 |
| `server/router.go` | +13 | 注册 `/api/fs/seed/*` 路由组 |

**驱动层**

| 文件 | 改动 | 说明 |
|---|---|---|
| `drivers/189pc/utils.go` | +96 | `rapidUploadByCAS` 三步流程 |
| `drivers/189pc/torrent.go` | +126 | CAS 信息注入 |
| `drivers/189/torrent.go` | +13 | sliceMd5 统一 |
| `drivers/115_open/driver.go` | +11 | 路径查找时正确处理「未找到」 |
| `drivers/quark_open/types.go` | +2 | `HashInfo` 填充 |
| `drivers/misskey/util.go` | +1 | `HashInfo` 填充 |

**其他**

| 文件 | 改动 | 说明 |
|---|---|---|
| `internal/bootstrap/data/setting.go` | +8 | 8 个新配置项默认值 |
| `internal/conf/const.go` | +10 | 配置常量定义 |
| `internal/bootstrap/task.go` | +1 | `SeedGenerateTaskManager` 注册 |
| `.github/workflows/*` | +210/−101 | PR 标题检查、Issue 评论工作流 |
| `.github/ISSUE_TEMPLATE/*` | +64/−0 | Issue 模板调整 |

---

## 五、配置变化

### 5.1 新增全局配置项

| Key | 类型 | 默认值 | 可见性 | 说明 |
|---|---|---|---|---|
| `seed_site_url` | string | `""` | PRIVATE | 生成来源链接所用的公开站点地址。**为空时无法嵌入来源** |
| `seed_default_matrix` | text(JSON) | `{"md5":{"whole":true,"pieces":false},"sha1":{"whole":true,"pieces":false},"sha256":{"whole":true,"pieces":false}}` | PRIVATE | 默认哈希矩阵 |
| `seed_format_policies` | text(JSON) | `{"oss":"off","torrent":"off","cas":"off"}` | PRIVATE | 各容器启用策略 |
| `seed_default_format` | select | `oss` | PRIVATE | 默认容器格式（`oss,torrent,cas`） |
| `seed_single_direct_preview` | bool | `false` | **PUBLIC** | 单文件直链预览 |
| `seed_cas_direct_access` | bool | `false` | **PUBLIC** | 打开单文件 CAS 种子时立即秒传到同目录并预览 |
| `seed_auto_generate_policy` | select | `off` | PRIVATE | 全局上传旁挂策略（`off,on`） |
| `seed_default_trackers` | text | `""` | PRIVATE | 生成 torrent 时的默认 tracker（每行一个） |

> `seed_single_direct_preview` 与 `seed_cas_direct_access` 标记为 `PUBLIC`，因为前端需要读取以决定 UI 行为；其余为 `PRIVATE`，仅管理员可控。

### 5.2 新增存储级配置

新增 `Storage.SeedPolicy` 字段（GORM 默认值 `inherit`），并在驱动配置界面暴露：

```go
items = append(items, driver.Item{
    Name:     "seed_policy",
    Type:     conf.TypeSelect,
    Options:  "inherit,on,off",
    Default:  "inherit",
    Required: true,
    Help:     "Override automatic transfer-seed generation for this storage",
})
```

**策略优先级**：

```
请求头 X-Seed-Sidecars / X-Generate-Seed（最高）
   ↓
存储级 seed_policy（on / off 时终止）
   ↓ inherit
全局 seed_auto_generate_policy（最低）
```

### 5.3 新增 API 路由

统一挂载于 `/api/fs/seed`，**legacy 的 `/api/fs/torrent/parse|rapid_upload|generate` 保持兼容**：

| 方法 | 路径 | Handler | 说明 |
|---|---|---|---|
| POST | `/api/fs/seed/parse` | `ParseSeed` | 解析种子（任意容器） |
| POST | `/api/fs/seed/upload_parse` | `UploadSeedAndParse` | 上传种子文件并解析 |
| POST | `/api/fs/seed/generate` | `GenerateSeedForPaths` | 按路径生成种子 |
| POST | `/api/fs/seed/convert` | `ConvertSeed` | 容器互转 |
| POST | `/api/fs/seed/diagnose` | `DiagnoseSeed` | 诊断可转换性 |
| POST | `/api/fs/seed/capabilities` | `SeedCapabilities` | 查询存储的秒传能力 |
| POST | `/api/fs/seed/rapid_upload` | `QuickSaveSeed` | 秒传转存 |
| POST | `/api/fs/seed/offline_download` | `QuickSaveSeed` | 离线下载转存 |
| POST | `/api/fs/seed/quick_save` | `QuickSaveSeed` | 快速转存（含降级链） |
| POST | `/api/fs/seed/update` | `UpdateSeed` | 更新种子（重算哈希） |
| POST | `/api/fs/seed/update_channels` | `UpdateSeedChannels` | 更新种子来源渠道 |

### 5.4 新增请求头

| Header | 取值 | 说明 |
|---|---|---|
| `X-Seed-Sidecars` | 非空即启用 | 上传时强制生成旁挂种子 |
| `X-Generate-Seed` | `on`/`off`/`inherit` | 上传时指定旁挂策略 |

---

## 六、安全性评审与修复

### 6.1 【已修复·严重】SSRF：重定向绕过来源白名单

**问题**

来源校验只检查**第一跳** host：

```go
// 修复前
resp, err := http.DefaultClient.Do(req)   // 默认跟随最多 10 次重定向，不校验目标
```

`http.DefaultClient` 静默跟随重定向。攻击者可构造：

```
https://pan.example.com/d/evil              ← 通过白名单校验
        ↓ 302
http://169.254.169.254/latest/meta-data/    ← 云元数据，直达内网
```

`/seed/quick_save` 只需普通用户权限即可触发，属**可远程利用的真实漏洞**。

**修复**

引入专用 `seedSourceHTTPClient`，在 `CheckRedirect` 中对**每一跳**复用同一套校验：

```go
var seedSourceHTTPClient = &http.Client{
    Timeout: 5 * time.Minute,
    CheckRedirect: func(req *http.Request, via []*http.Request) error {
        if len(via) >= maxSeedSourceRedirects {
            return fmt.Errorf("种子来源重定向次数过多（最多 %d 次）", maxSeedSourceRedirects)
        }
        if err := validateSeedHost(req.URL); err != nil {
            return fmt.Errorf("种子来源重定向被拒绝: %w", err)
        }
        if origin := via[0].URL.Scheme; !strings.EqualFold(req.URL.Scheme, origin) {
            return fmt.Errorf("种子来源重定向不允许切换协议: %s -> %s", origin, req.URL.Scheme)
        }
        return nil
    },
}
```

校验逻辑收敛为单一实现 `validateSeedHost`，**预检与重定向复用同一规则**，杜绝「校验一次就永久信任」。

**不影响种子功能**：合法 `/d/` 与 `/sd/` 来源不会 302 到其他 host，重定向本就不是种子的正常使用方式。

### 6.2 【已修复·严重】内容拉取全量缓冲 + 丢弃请求上下文

**问题**

```go
// 修复前
req, _ := http.NewRequest(...)               // 丢弃请求 ctx
data, _ := io.ReadAll(io.LimitReader(resp.Body, maxTorrentGenFileSize+1))  // 最多缓冲 1GB
Ctx: context.Background(),                    // 再丢一次
```

1. `context.Background()` 导致用户取消请求后下载仍跑完；
2. 为读几百字节 proof 窗口而把整文件缓冲进内存，对 1GB 文件是纯浪费与 OOM 风险。

**修复**

- 改用 `http.NewRequestWithContext(ctx, ...)`，透传 `stream.FileStream{Ctx: ctx}`；
- 改为**流式**：`io.LimitReader` 包装响应体 + `fs.Add(resp.Body)` 保证生命周期；
- 优先用 `Content-Length` 提前判断大小合法性，超限直接拒绝，不进入流式阶段。

### 6.3 【已修复·主要】来源 Host 比对未规范化端口

**问题**：配置 `https://pan.example.com` 而来源为 `https://pan.example.com:443/...` 时被判为不同 host 而**静默跳过**，表现为「种子明明有源却提示 no usable source」。

**修复**：`sameSeedHost` 按 **hostname + 有效端口**（http→80 / https→443）比对，预检与重定向统一使用。

### 6.4 【已修复·主要】多文件种子秒传提交矛盾数据

**问题**：只取 `Files[0]` 的哈希却用 `GetTotalSize()` 作大小，向云端提交自相矛盾对象；且 `rapidReq` 可能为 nil 却被解引用，存在 panic 风险。

**修复**：多文件、或单文件但元数据大小与 torrent 长度不一致时**显式返回 nil**；两个调用点均补 nil 判断。正常路径（`saveSeedFilesToPath` 会拆成单文件）不受影响。

### 6.5 【已修复·次要】sliceMd5 规则存在 5 份实现

**问题**：同一规则分散在 `GetSliceMD5`、`BuildCASInfoFromMD5sWithCloud`、`generate.go`、`189/torrent.go`、`189pc/torrent.go`。`sliceMd5` 是云端实际比对的值，生成侧与编码侧一旦漂移会**静默退化为哈希不匹配**，极难排查。

**修复**：收敛为唯一实现 `SliceMD5FromPieces`，5 处全部复用，并加测试锁定两侧一致。

### 6.6 【已修复·次要】bencode 字符串长度上限不自洽

单条字符串允许 100MB，但整个输入已被限制在 10MB（`DefaultMaxSeedSize`）。已统一收敛，并修正原注释中错误的「fits in int32」说明。

### 6.7 明确保留的设计决策

| 决策 | 原因 |
|---|---|
| 拉取来源**不携带身份** | 种子必须可移植，见 §3.6 |
| `TorrentRapidUpload` 的 `overwrite` 固定 `true` | 秒传语义本身就是「把已存在的云端数据挂到目标目录」，等价于覆盖，做成可选无实际意义 |
| `sign_all` 开启时禁止嵌入 direct 来源 | 签名 URL 携带身份且会过期，与可移植性冲突；此时自动退化为分享 |

---

## 七、测试

### 7.1 新增测试

**`pkg/torrent/seed_security_test.go`**（+156）
- 路径穿越拒绝（`../`、绝对路径、`\x00`、`..\`）
- 文件数上限
- `SliceMD5FromPieces` 规范规则
- **`GetSliceMD5` 与 `BuildCASInfoFromMD5s` 一致性锁定**
- bencode 超大长度 / 深度炸弹 / 尾随数据拒绝
- OSS→torrent→CAS→OSS 跨格式往返一致性

**`server/handles/torrent_seed_test.go`**（+259）
- `sameSeedHost` 端口规范化（含 `pan.example.com.evil.com` 仿冒域名）
- `validateSeedHost` 拒绝云元数据 / localhost / 协议降级 / 嵌入凭据
- **重定向守卫拦截 SSRF**（核心回归测试）
- 重定向跳数上限
- 来源类型路径前缀契约
- `firstUsableSeedSource` 跳过过期与域外来源
- 多文件 / 大小不一致种子的秒传拒绝

**`pkg/torrent/seed_test.go`**（+160）
- 种子编解码基础功能

### 7.2 验证结果

```bash
$ go build ./...
# 退出码 0

$ go test ./pkg/torrent/...
ok  github.com/OpenListTeam/OpenList/v4/pkg/torrent      0.592s

$ go test ./server/handles/...
ok  github.com/OpenListTeam/OpenList/v4/server/handles   2.787s
```

---

## 八、迁移方法

### 8.1 无破坏性变更

本 PR **不修改任何既有接口语义**：
- 既有 torrent 解析路径保持兼容（CAS 的 5 个 legacy 字段始终输出）；
- `/api/fs/torrent/*` 路由全部保留；
- 所有改动均为新增能力。

**升级无需数据迁移**，直接替换二进制即可。

### 8.2 启用步骤

**步骤 1：配置站点地址（必需）**

```json
{
  "key": "seed_site_url",
  "value": "https://pan.example.com"
}
```

> ⚠️ 必须是**外部可达且匿名可访问**的地址。若配置为本机 `127.0.0.1`，跨实例使用时对方无法访问；且在 SSRF 校验语义下，此举等同于把内网地址列为信任域，**请勿在生产环境这样配置**。

**步骤 2：启用所需容器格式**

```json
{
  "key": "seed_format_policies",
  "value": "{\"oss\":\"on\",\"torrent\":\"on\",\"cas\":\"off\"}"
}
```

**步骤 3（可选）：配置哈希矩阵**

```json
{
  "key": "seed_default_matrix",
  "value": "{\"md5\":{\"whole\":true,\"pieces\":true},\"sha1\":{\"whole\":true,\"pieces\":false},\"sha256\":{\"whole\":false,\"pieces\":false}}"
}
```

> 需要 189pc 系 CAS 秒传时，必须开启 `md5.pieces`。

**步骤 4（可选）：启用上传旁挂种子**

```json
[
  {"key": "seed_auto_generate_policy", "value": "on"},
  {"key": "seed_default_trackers", "value": "https://tracker.example.com/announce"}
]
```

存储级可覆盖：

```
存储配置 → seed_policy → on / off / inherit
```

**步骤 5：验证**

```bash
# 查询某存储的秒传能力
curl -X POST https://pan.example.com/api/fs/seed/capabilities \
  -H "Authorization: <token>" \
  -H "Content-Type: application/json" \
  -d '{"path": "/", "password": ""}'
```

### 8.3 数据库变更

`Storage` 表新增 `seed_policy` 列，由 GORM AutoMigrate 自动处理，默认值 `inherit`。**既有存储记录自动获得 `inherit` 语义**，行为与升级前一致。

### 8.4 前端配合

前端需读取两个 PUBLIC 配置项以决定 UI：

| Key | 用途 |
|---|---|
| `seed_single_direct_preview` | 是否展示单文件直链预览 |
| `seed_cas_direct_access` | 打开 CAS 种子时是否自动秒传预览 |

---

## 九、评审结论

| 维度 | 评级 | 说明 |
|---|---|---|
| 需求达成度 | 优 | 三容器互转、能力探测、降级链完整 |
| 架构设计 | 优 | 分层清晰，`info_hash` 隔离设计正确 |
| 安全性 | 良 | SSRF 重定向绕过与内容拉取问题已修复 |
| 健壮性 | 良 | 边界校验充分，nil 路径已补齐 |
| 可维护性 | 优 | sliceMd5 规则已收敛为单一实现 |
| 测试覆盖 | 良 | 补齐恶意输入、SSRF、格式一致性回归 |

### 待跟进（非阻塞）

1. **真实网盘端到端验证**：当前验证为编译期 + 单元测试级别。115 的 `pre_hash`、阿里云盘/夸克的 `proof_code`、`189pc` 的 CAS 三步流程，建议用真实账号各跑通一次。
2. **`ValidateSeed` 增加 torrent piece 对齐约束**：`DiagnoseConversion` 会提示「piece boundary crosses the next file」，但 `ValidateSeed` 未强制，存在中间态。
3. **`saveSeedFilesToPath` 并发度**：目前逐文件串行，大文件数场景可考虑有界并发。

---

## 十、变更统计

```
 45 files changed, 5310 insertions(+), 308 deletions(-)
```

| 分类 | 文件数 | 说明 |
|---|---|---|
| 新增驱动实现 | 12 | 各云盘秒传 |
| 新增核心模块 | 3 | torrent.go / seed_generate.go / seed_stream.go |
| 新增测试 | 3 | 安全、功能、SSRF 回归 |
| 修改（后端） | 15 | 接口、配置、路由、handler、驱动 |
| 修改（CI/模板） | 7 | workflows / ISSUE_TEMPLATE |
| 文档 | 2 | 本 PR 文档 |
