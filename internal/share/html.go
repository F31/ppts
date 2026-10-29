package share

import (
	"bytes"
	"html/template"
	"strings"

	"github.com/F31/ppts/internal/media"
)

// indexCSP 是成品页的内容安全策略。
//
// 关键点：必须显式允许 `file:` scheme。双击打开时页面源是 file://，Chrome/Safari 下
// `'self'` 对 file 资源并不总是匹配，只写 'self' 会导致 app.js/app.css 被拦、页面白屏——
// 而"双击能播"是这个产物唯一的存在理由。`file:` 在 http 托管下不会被用到（浏览器本身
// 禁止 http 页面加载 file 资源），所以不放宽任何远程来源。
//
// 注意这里**没有** `'unsafe-inline'`：产物里不应有内联脚本。这带来一个测试上的后果——
// 任何「往 index.html 插一段 <script> 探针」的做法都会被 CSP 拒绝，门禁必须改用同目录
// 外部 .js 文件（design ADR-1）。
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
<noscript><div class="ppts-noscript">
<div class="ppts-noscript-inner">
<h1 class="ppts-noscript-title">{{.Title}}</h1>
<p class="ppts-noscript-note">当前环境未启用 JavaScript，无法播放音视频。下面按页列出全部画面与讲稿：音频为同目录 <code>audio/mix.wav</code>，字幕为 <code>subtitles.vtt</code>。</p>
{{range .Pages}}<figure class="ppts-noscript-page">
<img class="ppts-noscript-image" src="{{.Image}}" alt="第 {{.Index}} 页">
<figcaption class="ppts-noscript-text">{{.Text}}</figcaption>
</figure>
{{end}}</div>
</div></noscript>
</div>
<script src="manifest.js"></script>
<script src="subtitles.js"></script>
<script src="app.js"></script>
</body>
</html>
`))

// noscriptPage 是禁 JS 时的一页：图 + 该页讲稿。
//
// 刻意不加 `loading="lazy"`：懒加载在禁 JS 时仍然生效，未进入视口的图不会请求，
// 于是 `naturalWidth` 为 0——门禁的「每张图都真的加载了」判据会假红，而人眼翻到
// 那一页时又要等一次请求。一次全出更符合「至少能读」的定位。
type noscriptPage struct {
	Index int
	Image string
	Text  string
}

type indexData struct {
	Title string
	// CSPAttr 是完整的属性片段（含 http-equiv 与 content）。
	// 用 template.HTMLAttr 而非拼进 content="..."：后者的单引号会被转义成 &#39;，
	// 虽然浏览器解析后等价，但产物不该是这种难读的样子。CSP 是编译期常量，无注入面。
	CSPAttr template.HTMLAttr
	Pages   []noscriptPage
}

// renderIndexHTML 生成成品首页。
//
// 标题来自 PPT，作为**不可信输入**一律经 html/template 转义（design D7）：
// text/template 只用于拼常量结构，二者不混用。
//
// pages / subtitles 只用于 <noscript> 分支：禁用 JS 时没有任何脚本能补内容，
// 「封面 + 逐页静态图 + 讲稿」必须在打包时写死（design ADR-4）。
func renderIndexHTML(title string, pages []SlideEntry, subtitles []media.SubtitleCue) ([]byte, error) {
	if title == "" {
		title = "讲解视频"
	}
	text := make(map[string][]string, len(subtitles))
	for _, cue := range subtitles {
		if body := strings.TrimSpace(cue.Text); body != "" {
			text[cue.SlideID] = append(text[cue.SlideID], body)
		}
	}
	items := make([]noscriptPage, 0, len(pages))
	for i, page := range pages {
		body := strings.Join(text[page.SlideID], " ")
		if body == "" {
			body = "（本页无讲稿）"
		}
		items = append(items, noscriptPage{Index: i + 1, Image: page.Image, Text: body})
	}

	var buf bytes.Buffer
	attr := template.HTMLAttr(`http-equiv="Content-Security-Policy" content="` + indexCSP + `"`)
	data := indexData{Title: title, CSPAttr: attr, Pages: items}
	if err := indexTemplate.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
