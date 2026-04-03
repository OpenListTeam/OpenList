# Security Policy

## Supported Versions

Only the latest stable release receives security patches. We strongly recommend always keeping OpenList up to date.

| Version              | Supported          |
| -------------------- | ------------------ |
| Latest stable (v4.x) | :white_check_mark: |
| Older versions       | :x:                |

## Reporting a Vulnerability

**Please do NOT report security vulnerabilities through public GitHub Issues.**

If you discover a security vulnerability in OpenList, please report it responsibly by using one of the following channels:

- **GitHub Private Security Advisory** (preferred): [Submit here](https://github.com/OpenListTeam/OpenList/security/advisories/new)
- **Telegram**: Contact a maintainer privately via [@OpenListTeam](https://t.me/OpenListTeam)

When reporting, please include as much of the following as possible:

- A description of the vulnerability and its potential impact
- The affected version(s)
- Step-by-step instructions to reproduce the issue
- Any proof-of-concept code or screenshots (if applicable)
- Suggested mitigation or fix (optional but appreciated)

## Security Best Practices for Users

To keep your OpenList instance secure:

- Always update to the latest release.
- Use a strong, unique admin password and change it after first login.
- Enable HTTPS (TLS) for your deployment — do **not** expose OpenList over plain HTTP on the public internet.
- Limit exposed ports using a reverse proxy (e.g., Nginx, Caddy).
- Set up access controls and avoid enabling guest access unless necessary.
- Regularly review mounted storage permissions and revoke unused API tokens.
- When using Docker, avoid running the container as root if possible.

## Acknowledgments

We sincerely thank all security researchers and community members who responsibly disclose vulnerabilities and help make OpenList safer for everyone.

---

# 安全政策

## 支持的版本

我们仅对最新稳定版本提供安全补丁。强烈建议始终保持 OpenList 为最新版本。

| 版本               | 是否支持           |
| ------------------ | ------------------ |
| 最新稳定版（v4.x） | :white_check_mark: |
| 旧版本             | :x:                |

## 报告漏洞

**请勿通过公开的 GitHub Issues 报告安全漏洞。**

如果您在 OpenList 中发现安全漏洞，请通过以下渠道之一负责任地进行报告：

- **GitHub 私密安全公告**（推荐）：[点击提交](https://github.com/OpenListTeam/OpenList/security/advisories/new)
- **Telegram**：通过 [@OpenListTeam](https://t.me/OpenListTeam) 私信联系维护者

报告时，请尽量提供以下信息：

- 漏洞描述及其潜在影响
- 受影响的版本
- 复现问题的详细步骤
- 概念验证代码或截图（如有）
- 建议的缓解措施或修复方案（可选，但非常欢迎）

## 用户安全最佳实践

为保障您的 OpenList 实例安全：

- 始终更新至最新版本。
- 使用强且唯一的管理员密码，并在首次登录后立即修改。
- 为您的部署启用 HTTPS（TLS）—— **请勿**在公网上以明文 HTTP 方式暴露 OpenList。
- 使用反向代理（如 Nginx、Caddy）限制对外暴露的端口。
- 配置访问控制，非必要情况下不要开启访客访问。
- 定期检查已挂载存储的权限，并撤销未使用的 API 令牌。
- 使用 Docker 部署时，尽可能避免以 root 用户运行容器。

## 致谢

我们衷心感谢所有负责任地披露漏洞、帮助 OpenList 变得更加安全的安全研究人员和社区成员。
