// Package static —— 虚拟主机访问码（Share Pwd）门禁。
//
// 当 sharing.Pwd != "" 时，所有通过域名访问 sharing 内容（无论 Web Hosting
// 还是路径重映射模式）都必须先通过密码校验：
//   - 提取顺序（择一即可）：?pwd= 查询参数 → X-Share-Pwd 请求头 → cookie share_pwd_<id>
//   - 校验通过后，会写一个 HttpOnly cookie（Path=/，SameSite=Lax），后续访问免输入；
//   - 校验未通过：GET 请求返回内嵌的密码输入页（401），POST application/x-www-form-urlencoded
//     的 ?pwd 字段会被识别为提交（302 回原 URL）。
//
// 安全细节：
//   - 密码比较使用 crypto/subtle.ConstantTimeCompare 防止时序侧信道；
//   - cookie 名按 sharing.ID 隔离，避免不同分享串扰；
//   - cookie 的 Secure 属性在 HTTPS 请求下自动开启。
package static

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	"github.com/gin-gonic/gin"
)

// sharePwdCookieName 返回当前 sharing 对应的 cookie 名。
// 名字按 ID 隔离避免一个浏览器同时访问多个不同分享时互相串扰。
func sharePwdCookieName(sharingID string) string {
	// 仅保留字母数字与下划线，确保 cookie name 合法
	var b strings.Builder
	b.Grow(len("share_pwd_") + len(sharingID))
	b.WriteString("share_pwd_")
	for i := 0; i < len(sharingID); i++ {
		c := sharingID[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '_', c == '-':
			b.WriteByte(c)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// extractSharePwd 按优先级从请求中提取访问码。
func extractSharePwd(c *gin.Context, sharingID string) string {
	if v := c.Query("pwd"); v != "" {
		return v
	}
	if v := c.GetHeader("X-Share-Pwd"); v != "" {
		return v
	}
	if v, err := c.Cookie(sharePwdCookieName(sharingID)); err == nil && v != "" {
		return v
	}
	return ""
}

// constantTimeEqual 时序安全字符串比较。
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// verifySharePwd 判断当前请求是否通过 sharing 访问码校验。
// 若 sharing.Pwd 为空（未设置访问码），永远通过。
func verifySharePwd(c *gin.Context, sharing *model.Sharing) bool {
	if sharing == nil || sharing.SharingDB == nil || sharing.Pwd == "" {
		return true
	}
	got := extractSharePwd(c, sharing.ID)
	if got == "" {
		return false
	}
	return constantTimeEqual(got, sharing.Pwd)
}

// setSharePwdCookie 把校验通过的密码写入 cookie，便于后续请求免输入。
func setSharePwdCookie(c *gin.Context, sharingID, pwd string) {
	secure := c.Request.TLS != nil ||
		strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
	cookie := &http.Cookie{
		Name:     sharePwdCookieName(sharingID),
		Value:    pwd,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   secure,
		// 不设 MaxAge：作为会话 cookie，浏览器关闭后失效，降低长期暴露风险
	}
	http.SetCookie(c.Writer, cookie)
}

// handleSharePwdGate 在虚拟主机请求进入 sharing 处理前完成密码门禁。
// 返回 true 表示已放行，调用方继续后续处理；
// 返回 false 表示当前请求已被该函数完整处理（密码页/重定向/方法不允许），调用方应直接 return。
func handleSharePwdGate(c *gin.Context, sharing *model.Sharing) bool {
	if sharing == nil || sharing.SharingDB == nil || sharing.Pwd == "" {
		return true
	}

	// POST application/x-www-form-urlencoded：识别为表单提交
	if c.Request.Method == http.MethodPost {
		ct := c.GetHeader("Content-Type")
		if i := strings.Index(ct, ";"); i >= 0 {
			ct = ct[:i]
		}
		if strings.EqualFold(strings.TrimSpace(ct), "application/x-www-form-urlencoded") {
			pwd := c.PostForm("pwd")
			if pwd != "" && constantTimeEqual(pwd, sharing.Pwd) {
				setSharePwdCookie(c, sharing.ID, pwd)
				// 提交成功：302 回当前路径（去掉 ?pwd=）
				redirect := c.Request.URL.Path
				if redirect == "" {
					redirect = "/"
				}
				c.Redirect(http.StatusFound, redirect)
				return false
			}
			// 密码错误：返回错误页
			writeSharePwdPage(c, sharing.ID, true)
			return false
		}
		// 其他 POST：405
		c.Status(http.StatusMethodNotAllowed)
		return false
	}

	// GET / HEAD：先看是否已携带正确凭据
	if verifySharePwd(c, sharing) {
		// 若是通过 ?pwd= 提交的，顺手写入 cookie 并清掉 query 中的 pwd 重定向，
		// 否则刷新或转发链接会一直暴露密码。
		if pwd := c.Query("pwd"); pwd != "" {
			setSharePwdCookie(c, sharing.ID, pwd)
			cleaned := stripPwdQuery(c.Request.URL.RawQuery)
			target := c.Request.URL.Path
			if cleaned != "" {
				target += "?" + cleaned
			}
			c.Redirect(http.StatusFound, target)
			return false
		}
		return true
	}

	// 未通过：返回密码输入页
	writeSharePwdPage(c, sharing.ID, false)
	return false
}

// stripPwdQuery 从 raw query 中剔除 pwd 参数，保留其他参数原序。
func stripPwdQuery(raw string) string {
	if raw == "" {
		return ""
	}
	parts := strings.Split(raw, "&")
	out := parts[:0]
	for _, p := range parts {
		if p == "" {
			continue
		}
		key := p
		if i := strings.Index(p, "="); i >= 0 {
			key = p[:i]
		}
		if key == "pwd" {
			continue
		}
		out = append(out, p)
	}
	return strings.Join(out, "&")
}

// sharePwdPageTpl 是访问码输入页的内嵌模板。
// 占位符：
//
//	{{ERROR_BLOCK}}  当密码错误时插入的提示块（HTML 片段，无需转义）
const sharePwdPageTpl = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>需要访问码</title>
<style>
  html,body{height:100%;margin:0;font-family:-apple-system,BlinkMacSystemFont,"Segoe UI",Roboto,"Helvetica Neue",Arial,"PingFang SC","Microsoft YaHei",sans-serif;background:#f5f7fa;color:#222}
  .wrap{min-height:100%;display:flex;align-items:center;justify-content:center;padding:24px;box-sizing:border-box}
  .card{width:100%;max-width:380px;background:#fff;border-radius:12px;box-shadow:0 8px 24px rgba(0,0,0,.08);padding:32px 28px}
  h1{margin:0 0 8px;font-size:20px}
  p.sub{margin:0 0 20px;color:#666;font-size:14px}
  label{display:block;margin-bottom:6px;font-size:13px;color:#444}
  input[type=password]{width:100%;box-sizing:border-box;padding:10px 12px;border:1px solid #d0d7de;border-radius:8px;font-size:15px;outline:none;transition:border-color .15s}
  input[type=password]:focus{border-color:#1f6feb;box-shadow:0 0 0 3px rgba(31,111,235,.15)}
  button{margin-top:16px;width:100%;padding:10px 16px;background:#1f6feb;color:#fff;border:0;border-radius:8px;font-size:15px;cursor:pointer;transition:background .15s}
  button:hover{background:#1a5fd0}
  .err{margin:0 0 16px;padding:10px 12px;background:#ffeef0;border:1px solid #ffd0d4;border-radius:8px;color:#a40e26;font-size:13px}
</style>
</head>
<body>
<div class="wrap">
  <form class="card" method="post" action="" enctype="application/x-www-form-urlencoded" autocomplete="off">
    <h1>需要访问码</h1>
    <p class="sub">该分享已设置访问码，请输入后继续。</p>
    {{ERROR_BLOCK}}
    <label for="pwd">访问码</label>
    <input id="pwd" name="pwd" type="password" autofocus required>
    <button type="submit">进入</button>
  </form>
</div>
</body>
</html>`

// writeSharePwdPage 把密码输入页写出。wrong=true 时附带"密码错误"提示块。
// 状态码：未输入时 401（请求需要凭据），密码错误时 403（已提供但无效）。
func writeSharePwdPage(c *gin.Context, sharingID string, wrong bool) {
	_ = sharingID // 当前模板表单 action="" 会回到当前 URL，不需要在页面里嵌入 ID
	errBlock := ""
	status := http.StatusUnauthorized
	if wrong {
		errBlock = `<p class="err">访问码错误，请重试。</p>`
		status = http.StatusForbidden
	}
	html := strings.Replace(sharePwdPageTpl, "{{ERROR_BLOCK}}", errBlock, 1)

	h := c.Writer.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Robots-Tag", "noindex")
	c.Status(status)
	if _, err := c.Writer.WriteString(html); err != nil {
		utils.Log.Debugf("[VirtualHost] writeSharePwdPage: %v", err)
	}
	c.Writer.Flush()
}
