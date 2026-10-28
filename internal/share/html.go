package share

import (
	"bytes"
	"html/template"
)

// indexCSP 是成品页的内容安全策略。
//
// 关键点：必须显式允许 `file:` scheme。双击打开时页面源是 file://，Chrome/Safari 下
// `'self'` 对 file 资源并不总是匹配，只写 'self' 会导致 app.js/app.css 被拦、页面白屏——
// 而"双击能播"是这个产物唯一的存在理由。`file:` 在 http 托管下不会被用到（浏览器本身
// 禁止 http 页面加载 file 资源），所以不放宽任何远程来源。
const indexCSP = "default-src 'none'; img-src 'self' file: data:; media-src 'self' file:; " +
	"style-src 'self' file: 'unsafe-inline'; script-src 'self' file:; base-uri 'none'; form-action 'none'"

var indexTemplate = template.Must(template.New("index.html").Parse(`<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta {{.CSPAttr}}>
<meta name="generator" content="ppts html_bundle">
<title>{{.Title}}</title>
<link rel="stylesheet" href="app.css">
</head>
<body>
<div id="ppts-share-root">
<noscript><p class="ppts-noscript">此成品需要启用 JavaScript 才能播放。字幕与讲稿见同目录下的 subtitles.vtt 与 timeline.json。</p></noscript>
</div>
<script src="manifest.js"></script>
<script src="subtitles.js"></script>
<script src="app.js"></script>
</body>
</html>
`))

type indexData struct {
	Title string
	// CSPAttr 是完整的属性片段（含 http-equiv 与 content）。
	// 用 template.HTMLAttr 而非拼进 content="..."：后者的单引号会被转义成 &#39;，
	// 虽然浏览器解析后等价，但产物不该是这种难读的样子。CSP 是编译期常量，无注入面。
	CSPAttr template.HTMLAttr
}

// renderIndexHTML 生成成品首页。标题来自 PPT，作为**不可信输入**一律经 html/template 转义
// （design D7）：text/template 只用于拼常量结构，二者不混用。
func renderIndexHTML(title string) ([]byte, error) {
	if title == "" {
		title = "讲解视频"
	}
	var buf bytes.Buffer
	attr := template.HTMLAttr(`http-equiv="Content-Security-Policy" content="` + indexCSP + `"`)
	if err := indexTemplate.Execute(&buf, indexData{Title: title, CSPAttr: attr}); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
