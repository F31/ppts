package share

import (
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/fonts"
	"github.com/F31/ppts/internal/media"
	"github.com/F31/ppts/internal/share/assets"
)

// 成品包内嵌字体（M6-4）。这个文件只做三件事：把字体写进包、写 @font-face、
// 校验「包里出现的每个字符，字体里都有字形」。
//
// 为什么必须内嵌：字幕是 DOM 文本（逐字高亮要按字符切 span，不能烧进图里），
// 所以它会用**打开者机器上的字体**渲染。没有内嵌字体时，「喆」「堃」「玥」这类
// 真实人名用字在精简过的系统字体里就是方框，而页面内容（PNG 快照）看起来完全正常
// ——缺陷只在字幕上出现，且只在一部分机器上出现。
//
// 为什么体积不是问题：母版是 11 MB，但**按片中字符裁**之后只有约 100 KB
// （实测 200 字 → 95 KB OTF，打进 zip 后 75 KB）。按全 URO 内嵌才是 11 MB，
// 那是把「一份讲稿」的需求当成了「一个字体库」。

const (
	// FileFontCSS 是字体声明的样式表，由 index.html 在 app.css 之前引用。
	FileFontCSS = "fonts.css"
	// fontEntryPath 是包内字体文件路径。用固定名而不是带版本号的名字：
	// 包内只有一份正文用字体，同名更便于排查（解压后一眼能对上）。
	fontEntryPath = "assets/fonts/ppts-text.otf"
	// fontFamilyName 是 CSS 里的族名。刻意不用原字体族名（Noto Sans CJK SC）：
	// 若用原名，装了同名字体的机器可能不加载内嵌文件而直接用本地版本，
	// 「这份包在任何机器上看起来一样」的前提就没了。
	fontFamilyName = "PPTS Text"
)

// FontAsset 是成品包内嵌的一份字体。
type FontAsset struct {
	// Family 是来源字体族名（写进清单，便于追溯用了哪份字体）。
	Family string
	// Format 是字体容器格式：otf | ttf。用于生成 @font-face 的 format()。
	Format string
	// Version 是母版版本（镜像构建期由 charset 摘要推导）。
	Version string
	// Data 是字体字节。
	Data []byte
}

// cssFormat 把容器格式映射成 CSS 的 format() 关键字。
//
// 映射错了的表现是「浏览器不加载字体、字幕静默回退到系统字体」——不会有报错，
// 也不会有视觉告警，所以这里对未知格式直接报错而不是猜一个默认值。
func (f FontAsset) cssFormat() (string, error) {
	switch strings.ToLower(strings.TrimSpace(f.Format)) {
	case "otf", "opentype":
		return "opentype", nil
	case "ttf", "truetype":
		return "truetype", nil
	case "woff2":
		return "woff2", nil
	case "woff":
		return "woff", nil
	default:
		return "", fmt.Errorf("share: font: 未知容器格式 %q", f.Format)
	}
}

// renderFontCSS 生成 @font-face 声明。
//
// 只声明 400 一个字重：包里只内嵌 Regular。标题上的 font-weight:600 由浏览器
// 合成加粗——多裁一份 Bold 会让字体体积翻倍，而它只影响 <noscript> 的标题与
// 几个按钮，不值得。
//
// 不写 unicode-range：内嵌字体同时接管拉丁字母，换来「同一份包在任何机器上
// 看起来一样」。若把拉丁让回 Inter，观感就取决于打开者装没装那个字体——
// 而这是一个「双击就能播」的离线产物，一致性优先。
func renderFontCSS(font FontAsset) ([]byte, error) {
	figure, err := font.cssFormat()
	if err != nil {
		return nil, err
	}
	var b strings.Builder
	b.WriteString("/* 成品包内嵌字体，由打包时按片中字符裁剪生成。 */\n")
	fmt.Fprintf(&b, "@font-face{font-family:%q;src:url(%q) format(%q);font-weight:400;font-style:normal;font-display:swap}\n",
		fontFamilyName, fontEntryPath, figure)
	return []byte(b.String()), nil
}

// checkFontCoverage 校验字体覆盖成品包里的全部文本，返回没有字形的字符。
//
// 调用点必须传**包里实际会显示的文本**：标题、字幕、播放器脚本里的 UI 文案。
// 少传一个来源，这里就会报「全覆盖」，而用户看到的是方框——门禁给出的虚假
// 安全感比没有门禁更糟。
func checkFontCoverage(font FontAsset, title string, subtitles []string, playerJS []byte) ([]rune, error) {
	cov, err := fonts.ReadFontCoverage(font.Data)
	if err != nil {
		return nil, err
	}
	charset := CollectFontCharset(title, subtitles, playerJS)
	return cov.Missing(charset.Runes), nil
}

// subtitleTexts 抽出字幕纯文本。
func subtitleTexts(in BundleInput) []string {
	return SubtitleTexts(in.Timeline)
}

// SubtitleTexts 抽出时间轴里的字幕纯文本。
//
// 导出的：调用方在请求字体子集时要算**同一套**字符集，若它自己再实现一遍
// 「怎么取字幕文本」，两处的 trim/空值处理稍有出入就会让请求的字集与校验的
// 字集不同——校验通过而包里有字没覆盖，正是这个功能最怕的失效方式。
func SubtitleTexts(tl *media.Timeline) []string {
	if tl == nil {
		return nil
	}
	out := make([]string, 0, len(tl.Subtitles))
	for _, cue := range tl.Subtitles {
		if text := strings.TrimSpace(cue.Text); text != "" {
			out = append(out, text)
		}
	}
	return out
}

// FontRequestCharset 算出成品包需要内嵌的字符集（含播放器 UI 文案）。
//
// 传的是时间轴而不是字幕切片：播放器文案来自 go:embed 的 app.js，调用方不该
// 为了拿它而 import 一个内部 assets 包，也就不会出现「某处忘了传 app.js」这种
// 静默漏字。
func FontRequestCharset(title string, tl *media.Timeline) (FontCharset, error) {
	js, err := assets.AppJS()
	if err != nil {
		return FontCharset{}, fmt.Errorf("share: embedded player script: %w", err)
	}
	return CollectFontCharset(title, SubtitleTexts(tl), js), nil
}

// runeStrings 把字符切片转成可 JSON 序列化的字符串切片。
// 每个元素是一个字符而不是拼成一整串：清单是给人看的排查材料，
// 拼成一串会让「缺了哪几个」和「缺了一个多字符的串」变得分不清。
func runeStrings(rs []rune) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = string(r)
	}
	return out
}
