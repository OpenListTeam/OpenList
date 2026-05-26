// Package static —— 虚拟主机 Web Hosting 模式下针对 Markdown 的服务端预览渲染。
//
// 设计原则：
//  1. 不引入任何新的第三方依赖。Markdown 渲染交给浏览器端 marked.js（CDN）。
//  2. 渲染有 size 上限（默认 5MB），防止从云端读取超大文件造成 OOM。
//  3. Markdown 原文嵌入到 <script type="text/markdown"> 中传给前端，避免 XSS：
//     即使内容含 <script>、</script>，也会因 type 非 JS 而不被执行；同时对 </script
//     做 <\/ 转义防止 script 标签被切断。
//
// 关于 .mhtml：Chrome 在网络场景下，响应头为 multipart/related 且无 Content-Disposition: attachment
// 时，会调用内置 MHTML 渲染器原生预览。无需服务端解析。
package static

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/stream"
	"github.com/OpenListTeam/OpenList/v4/pkg/http_range"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
)

// renderMaxBytes 单个被渲染文件允许的最大字节数。
// 超过此值视为非预期文件，回退为原始下载（不再尝试服务端解析）。
const renderMaxBytes int64 = 5 * 1024 * 1024

// readLinkAll 通过 Link 把整个文件读入内存，受 maxBytes 限制。
// 适用于 Markdown 这种小文本文件场景。
func readLinkAll(ctx context.Context, link *model.Link, declaredSize int64, maxBytes int64) ([]byte, error) {
	size := link.ContentLength
	if size <= 0 {
		size = declaredSize
	}
	if size > 0 && size > maxBytes {
		return nil, fmt.Errorf("file too large for render: size=%d max=%d", size, maxBytes)
	}

	rr, err := stream.GetRangeReaderFromLink(size, link)
	if err != nil {
		return nil, fmt.Errorf("get range reader: %w", err)
	}

	// 当 size 未知时使用全量范围（Length=-1 由底层处理）
	rng := http_range.Range{Start: 0, Length: -1}
	if size > 0 {
		rng = http_range.Range{Start: 0, Length: size}
	}
	rc, err := rr.RangeRead(ctx, rng)
	if err != nil {
		return nil, fmt.Errorf("range read: %w", err)
	}
	defer rc.Close()

	// 用 LimitReader 兜底防止 size 申报为 0 但内容超大
	limited := io.LimitReader(rc, maxBytes+1)
	buf, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read all: %w", err)
	}
	if int64(len(buf)) > maxBytes {
		return nil, fmt.Errorf("file exceeds max bytes during read: max=%d", maxBytes)
	}
	return buf, nil
}

// markdownPreviewTpl 是 Markdown 全屏预览的 HTML 模板。
// 使用 marked.js + highlight.js + DOMPurify 在浏览器端渲染，样式参考 GitHub Markdown CSS。
// 占位符：
//
//	{{TITLE}}     <title> 标签内容（页面文件名，已 HTML 转义）
//	{{MD_BODY}}   原始 Markdown 文本（已对 </ 做 <\/ 转义防止 script 标签提前闭合）
const markdownPreviewTpl = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>{{TITLE}}</title>
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/github-markdown-css@5/github-markdown.min.css">
<link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/highlight.js@11/styles/github.min.css">
<style>
  html,body{margin:0;padding:0;background:#fff}
  .markdown-body{box-sizing:border-box;min-width:200px;max-width:980px;margin:0 auto;padding:32px 45px}
  @media (max-width:767px){.markdown-body{padding:16px}}
  .md-loading{padding:32px 45px;color:#888;font-family:system-ui,sans-serif}
</style>
</head>
<body>
<article class="markdown-body" id="md-target"><p class="md-loading">Rendering Markdown...</p></article>
<script id="md-source" type="text/markdown">{{MD_BODY}}</script>
<script src="https://cdn.jsdelivr.net/npm/marked@11/marked.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/marked-highlight@2/lib/index.umd.js"></script>
<script src="https://cdn.jsdelivr.net/npm/highlight.js@11/lib/core.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/highlight.js@11/lib/common.min.js"></script>
<script src="https://cdn.jsdelivr.net/npm/dompurify@3/dist/purify.min.js"></script>
<script>
(function(){
  try{
    var src = document.getElementById('md-source').textContent || '';
    if (window.marked && window.markedHighlight && window.hljs) {
      window.marked.use(window.markedHighlight.markedHighlight({
        langPrefix: 'hljs language-',
        highlight: function(code, lang){
          try { return (lang && hljs.getLanguage(lang)) ? hljs.highlight(code,{language:lang}).value : hljs.highlightAuto(code).value; }
          catch(_) { return code; }
        }
      }));
    }
    var html = (window.marked ? window.marked.parse(src) : '').toString();
    if (window.DOMPurify) { html = window.DOMPurify.sanitize(html); }
    document.getElementById('md-target').innerHTML = html;
  }catch(e){
    document.getElementById('md-target').textContent = String(e);
  }
})();
</script>
</body>
</html>`

// renderMarkdownPreview 把 Markdown 原文包装为完整的 HTML 预览页。
// 注意：原文必须做 </ 转义，避免在 <script type=text/markdown> 中提前闭合。
func renderMarkdownPreview(filename string, mdSource []byte) []byte {
	// HTML-escape title（防止文件名注入 HTML）
	title := htmlEscape(filename)

	// 对 </ 做转义。<script type="text/markdown"> 块在 HTML 解析阶段
	// 会按"原始文本"处理，唯一会让它结束的是 </script 这种序列（不区分大小写）。
	// 替换为 <\/，浏览器仍会按原字符显示，但不会触发 script 闭合。
	body := bytes.ReplaceAll(mdSource, []byte("</"), []byte(`<\/`))

	out := strings.NewReplacer(
		"{{TITLE}}", title,
		"{{MD_BODY}}", string(body),
	).Replace(markdownPreviewTpl)
	return []byte(out)
}

// htmlEscape 简单的 HTML 转义，仅处理在文本/属性场景需要的几个字符。
func htmlEscape(s string) string {
	r := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
		"'", "&#39;",
	)
	return r.Replace(s)
}

// writeRenderedHTML 把渲染后的 HTML 作为 200 响应写出。
func writeRenderedHTML(w http.ResponseWriter, html []byte) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Disposition", "inline")
	// 已是完整的同步内容，禁用缓存以方便用户排查
	h.Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(html); err != nil {
		utils.Log.Debugf("[VirtualHost] writeRenderedHTML: %v", err)
	}
}