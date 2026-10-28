# OpenList Synology SPK

将 [OpenList](https://github.com/openlistteam/openlist) 打包为 Synology DSM 7 套件（SPK）。

## 自动构建

推送 `v*` 格式的 Git 标签即可触发自动构建并发布到 GitHub Releases：

```bash
git tag v4.2.9
git push origin v4.2.9
```

GitHub Actions 会自动：
1. 从 [OpenList Releases](https://github.com/openlistteam/openlist/releases) 下载对应版本的二进制文件
2. 为两种架构（x64 / arm64）分别构建 SPK 安装包
3. 发布到本仓库的 GitHub Releases 页面

## 手动触发

在 GitHub 仓库 → Actions → Build SPK Release → Run workflow，输入版本号即可。

## 安装

1. 从 Releases 页面下载对应架构的 SPK 文件
2. DSM 套件中心 → 手动安装 → 上传 SPK
3. 安装后通过 `http://<NAS-IP>:5244` 访问

## 支持的架构

| SPK 文件 | 适用型号 | CPU |
|----------|---------|-----|
| `openlist_x64_*.spk` | DS224+, DS920+, DS1522+ 等 | Intel/AMD x86_64 |
| `openlist_arm64_*.spk` | DS423+, DS623slim 等 | ARM64 |

## 目录结构

```
├── .github/workflows/build-spk.yml  # GitHub Actions 工作流
├── build-spk.sh                     # SPK 构建脚本
├── scripts/                         # DSM 套件生命周期脚本
│   ├── installer                    # spksrc 标准安装器
│   ├── functions                    # 辅助函数
│   ├── service-setup                # OpenList 服务配置
│   ├── start-stop-status            # 启停状态管理
│   └── preinst/postinst/...         # 生命周期钩子
├── conf/                            # DSM 权限配置
│   ├── privilege                    # 运行权限（非 root）
│   └── resource                     # 资源声明
├── etc/                             # OpenList 默认配置
│   └── config.json                  # 端口、数据库路径等
├── ui/                              # DSM 应用入口
│   ├── config                       # 应用 URL 定义
│   └── images/                      # 图标（8 种尺寸）
├── icons/                           # SPK 级别图标
│   ├── PACKAGE_ICON.PNG             # 72×72
│   └── PACKAGE_ICON_256.PNG         # 256×256
└── WIZARD_UIFILES/                  # 卸载向导
    └── uninstall_uifile             # 卸载选项（中文）
```

## 本地构建

```bash
# 下载 OpenList 二进制
curl -sL https://github.com/openlistteam/openlist/releases/download/v4.2.9/openlist-linux-amd64.tar.gz | tar xz

# 构建 SPK
./build-spk.sh 4.2.9 x64 ./openlist
```
