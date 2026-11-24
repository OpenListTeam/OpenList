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

## Security Response Scope (Applicable to 4.x)

This policy applies only to version 4.x.

In 4.x, privilege isolation between authenticated users (including administrators and regular users) is not currently considered a security boundary that this project commits to maintaining.

In principle, only security vulnerabilities that can be exploited without valid authentication (including authentication bypass vulnerabilities) fall within the scope of this project's dedicated security response process.

Privilege isolation issues that require valid authentication to exploit should preferably be submitted and fixed through regular Pull Requests rather than handled through Security Advisories.

This policy only defines the scope of the project's security response and does not imply that such issues are not security vulnerabilities. For exceptional cases that may have severe security implications, the maintenance team reserves the right to initiate the security response process at its discretion.

This policy does not retroactively affect previously accepted security vulnerability reports.

This project is currently preparing version 5.x. In the future, we may redefine the security boundaries between authenticated users by refactoring the user and permission systems and adjust the security response scope accordingly.

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

## 安全响应范围 (适用于 4.x)

本政策仅适用于 4.x 版本。

在 4.x 中，已认证用户 (包括管理员和普通用户) 之间的权限隔离暂不属于本项目承诺维护的安全边界。

只有无需有效身份认证即可利用的安全漏洞 (包括身份认证绕过漏洞)，原则上才纳入本项目的专门安全响应流程。

需要有效身份认证才能利用的权限隔离问题，应优先通过普通 Pull Request 提交和修复，而非通过安全公告 (Security Advisory) 处理。

本政策仅限定项目维护团队的安全响应范围，不代表此类问题不构成安全漏洞。对于可能造成严重安全影响的特殊情况，维护团队保留酌情启动安全响应流程的权利。

本政策不追溯影响此前已正式接受的安全漏洞报告。

本项目正在准备 5.x 版本，未来可能通过重构用户及权限系统，重新定义已认证用户之间的安全边界，并相应调整安全响应范围。

## 致谢

我们衷心感谢所有负责任地披露漏洞、帮助 OpenList 变得更加安全的安全研究人员和社区成员。
