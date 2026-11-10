import { createRequire } from "node:module"
import { pathToFileURL } from "node:url"

/** 仅为飞书妙记和腾讯会议分享页创建临时会话，不读取现有浏览器资料。 */
export function platformFor(raw) {
  const u = new URL(raw)
  if (u.protocol !== "https:" || u.username || u.password || u.port) throw new Error("INVALID_SHARE_LINK")
  if ((u.hostname === "feishu.cn" || u.hostname.endsWith(".feishu.cn")) && u.pathname.startsWith("/minutes/")) return "feishu"
  if (u.hostname === "meeting.tencent.com" && u.pathname.startsWith("/cw/")) return "tencent"
  throw new Error("UNSUPPORTED_SHARE_LINK")
}

/** 原始 Cookie 头只绑定到所属平台，不能作为全局请求头发给第三方 CDN。 */
export function cookieObjects(raw, pageURL) {
  const platform = platformFor(pageURL)
  const u = new URL(pageURL)
  if (!raw || raw.length > 32768 || /[\r\n\0]/.test(raw)) throw new Error("INVALID_COOKIE")
  return raw.replace(/^cookie:\s*/i, "").split(";").filter((part) => part.trim()).map((part) => {
    const index = part.indexOf("=")
    if (index < 1) throw new Error("INVALID_COOKIE")
    const name = part.slice(0, index).trim()
    const value = part.slice(index + 1).trim().replace(/^"(.*)"$/, "$1")
    // __Host- Cookie 必须不带 Domain；其余 Cookie 仅在平台自身域名作用域内。
    return name.startsWith("__Host-")
      ? { name, value, url: u.origin, secure: true }
      : { name, value, domain: platform === "feishu" ? ".feishu.cn" : "meeting.tencent.com", path: "/", secure: true }
  })
}

/** 排除 HLS 的初始化片段与媒体分片，不能把单个片段当作整个会议。 */
export function mediaKind(raw, type = "") {
  const u = new URL(raw)
  if (!["http:", "https:"].includes(u.protocol)) return ""
  if (/\.(?:ts|m4s|aac)(?:$|\/)/i.test(u.pathname) || /(?:^|\/)init[^/]*\.mp4$/i.test(u.pathname)) return ""
  if (/\.m3u8$/i.test(u.pathname) || /mpegurl/i.test(type)) return "hls"
  if (/\.mp4$/i.test(u.pathname)) return "mp4"
  if (/video\/mp4/i.test(type)) return "mp4"
  return ""
}

/** 从浏览器实际媒体请求取 URL/必要请求头，不猜测平台的私有 API。
 * configure 仅供测试注入请求夹具，stdin 不能传入函数或改变解析行为。
 */
export async function resolveShare(payload, configure) {
  platformFor(payload.url)
  const require = createRequire(payload.module_dir + "/package.json")
  const { chromium } = require("playwright-core")
  const browser = await chromium.launch({ executablePath: payload.browser_path, headless: true, args: ["--no-sandbox", "--autoplay-policy=no-user-gesture-required", "--disable-dev-shm-usage"] })
  try {
    const context = await browser.newContext({ serviceWorkers: "block" })
    // 空 Cookie 代表匿名访问，必须真实尝试解析，不能在这里提前拒绝。
    if (payload.cookie?.trim()) await context.addCookies(cookieObjects(payload.cookie, payload.url))
    if (configure) await configure(context)
    const candidates = new Map()
    const pending = new Set()
    context.on("response", (response) => {
      const task = (async () => {
        if (![200, 206].includes(response.status())) return
        const url = response.url()
        const kind = mediaKind(url, response.headers()["content-type"] || "")
        if (!kind) return
        const headers = await response.request().allHeaders()
        const selected = {}
        for (const [key, value] of Object.entries(headers)) {
          if (["cookie", "authorization", "user-agent", "referer", "origin"].includes(key) || (key.startsWith("x-") && !key.startsWith("x-forwarded-"))) selected[key] = value
        }
        candidates.set(url, { url, kind, headers: selected })
      })().catch(() => {})
      pending.add(task)
      task.finally(() => pending.delete(task))
    })
    const page = await context.newPage()
    await page.goto(payload.url, { waitUntil: "domcontentloaded", timeout: 30000 })
    for (let step = 0; step < 40; step++) {
      // 播放操作只触发正常播放器请求，不操作分享权限、验证码或账号设置。
      for (const frame of page.frames()) {
        await frame.locator("video").evaluateAll((videos) => videos.forEach((video) => { video.muted = true; video.play().catch(() => {}) })).catch(() => {})
      }
      if (candidates.size) { await page.waitForTimeout(500); break }
      await page.waitForTimeout(1000)
    }
    await Promise.allSettled([...pending])
    const values = [...candidates.values()]
    const candidate = values.find((item) => item.kind === "mp4") || values.find((item) => item.kind === "hls")
    if (!candidate) throw new Error("NO_MEDIA_OR_LOGIN_REQUIRED")
    // 取当前媒体域名的 Cookie，包含页面响应刷新后的值，而不是全站原始 Cookie。
    const cookies = await context.cookies(candidate.url)
    if (cookies.length) candidate.headers.cookie = cookies.map((cookie) => `${cookie.name}=${cookie.value}`).join("; ")
    return candidate
  } finally { await browser.close() }
}

/** 凭据只从 stdin 读取，避免出现在进程参数里；异常只输出固定代码。 */
if (process.argv[1] && import.meta.url === pathToFileURL(process.argv[1]).href) {
  try {
    let input = ""
    for await (const chunk of process.stdin) { input += chunk; if (input.length > 65536) throw new Error("INVALID_COOKIE") }
    const result = await resolveShare(JSON.parse(input))
    process.stdout.write(JSON.stringify(result))
  } catch (error) {
    const known = ["INVALID_SHARE_LINK", "UNSUPPORTED_SHARE_LINK", "INVALID_COOKIE", "NO_MEDIA_OR_LOGIN_REQUIRED"]
    process.stdout.write(JSON.stringify({ error: known.includes(error.message) ? error.message : "BROWSER_RESOLVE_FAILED" }))
    process.exitCode = 1
  }
}
