package share

import (
	"sort"
	"unicode"
	"unicode/utf8"
)

// FontCharset 是成品包需要内嵌的字符集：包里所有会**以文本形式显示**的字符。
//
// 为什么要在 Go 侧算这个集合，而不是取 charset.txt 的全 URO：
//
//	charset.txt 是渲染镜像的字集边界，它必须覆盖「任何 PPT 的任何文字」，所以是 2 万码位、
//	母版 11 MB。而成品包只服务**一份**讲稿，实际出现的字符通常不到三百个。两者的正确
//	取值差异不是 10%，是三个数量级——实测同一字体裁到 200 字是 95 KB，裁到全 URO 是 11 MB。
//
// 取字集的三个来源必须合起来，少一个就出方框：
//
//  1. 标题：写在 index.html 的 <title> 与 <h1>，以及清单里。
//  2. 字幕：字幕是 DOM 文本（逐字高亮要按字符切 span），不是烧在图里的像素。
//  3. 播放器脚本里的全部非 ASCII 字符：播放/暂停/全屏/加载失败这些 UI 文案编译进
//     app.js，硬编码一份字表意味着 app.js 每次加一句提示就要人记得改两处——那是
//     注定会漏的维护面。直接扫字节，多收几个字符只多几 KB，漏一个就是一个方框。
//
// 刻意不做字形层面的取舍：拉丁字母、标点、数字一律全收。它们占比小，且
// 「这份讲稿恰好没用逗号」这种判断一旦出错，代价是可见的排版缺陷。
type FontCharset struct {
	// Runes 是升序去重的码位。
	Runes []rune
	// Sources 记录各来源贡献的字符数，用于排查「字为什么这么多/这么少」。
	Sources map[string]int
}

// 包内固定文案里出现的字符：**只**包含 index.html 模板里写死的那些。
//
// 播放器的按钮与状态文案（播放/暂停/重播/上一页/…）不在这里——它们编译进 app.js，
// 由 CollectFontCharset 直接扫字节收进来。这里列一遍会制造一份「影子字表」：
// 它与 app.js 实际内容各写各的，谁先过时都不会有人发现。第一版就是这么写的，
// 凭空多收了「加载失败 / 退出全屏 / 静音」这些播放器根本没有的文案，
// 结果是字集比实际大 9 个字，子集被迫带上永远用不到的字形。
//
// 代价是模板改了文案要同步这里，所以 TestTemplateTextCoveredByCharset 会渲染真实
// index.html 逐字比对，TestBundleFixedTextHasNoPhantomCharacters 反过来查「多收」。
const bundleFixedText = "当前环境未启用 JavaScript，无法播放音视频。下面按页列出全部画面与讲稿：" +
	"音频为同目录 字幕为 第 页 讲解视频 （本页无讲稿）"

// CollectFontCharset 汇总成品包需要的全部字符。
//
// subtitles 传字幕文本（而非 cue 结构）：调用方本来就持有 VTT 原文或 Timeline，
// 本函数不该为了取 Text 字段而依赖 media 包。
func CollectFontCharset(title string, subtitles []string, playerJS []byte) FontCharset {
	out := FontCharset{Sources: map[string]int{}}
	seen := make(map[rune]struct{}, 512)

	add := func(source, s string) int {
		n := 0
		for _, r := range s {
			if !fontWanted(r) {
				continue
			}
			if _, ok := seen[r]; ok {
				continue
			}
			seen[r] = struct{}{}
			out.Runes = append(out.Runes, r)
			n++
		}
		out.Sources[source] += n
		return n
	}

	add("title", title)
	for _, s := range subtitles {
		add("subtitles", s)
	}
	add("player", string(playerJS))
	add("fixed", bundleFixedText)

	sort.Slice(out.Runes, func(i, j int) bool { return out.Runes[i] < out.Runes[j] })
	return out
}

// fontWanted 判断一个字符是否该进字集。
//
// 排除控制字符与代理区：它们不可能有字形，喂给子集器只会让「缺字」断言假红。
// 保留换行以外的空白（含 U+3000 全角空格）：空格不出轮廓，但决定词距与断行，
// 子集器必须把它们留在 cmap 里——charset.txt 的注释记过这个坑。
func fontWanted(r rune) bool {
	if r == utf8.RuneError {
		return false
	}
	if r == '\n' || r == '\r' || r == '\t' {
		return false
	}
	if r < 0x20 || (r >= 0x7F && r < 0xA0) {
		return false
	}
	if r >= 0xD800 && r <= 0xDFFF {
		return false
	}
	return unicode.IsPrint(r) || unicode.IsSpace(r)
}

// Codepoints 返回 uint32 形式的码位，供 HTTP 与子集脚本使用。
func (c FontCharset) Codepoints() []uint32 {
	cp := make([]uint32, len(c.Runes))
	for i, r := range c.Runes {
		cp[i] = uint32(r)
	}
	return cp
}

// String 返回字集文本，用于喂给子集器或做摘要。
func (c FontCharset) String() string {
	return string(c.Runes)
}
