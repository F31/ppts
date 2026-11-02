package share

import (
	"errors"
	"fmt"
	"strings"

	"github.com/F31/ppts/internal/fonts"
)

// FontCoverage 是「成品包内嵌字体是否覆盖包内全部文本」的独立结论（V15）。
//
// 为什么在校验侧重新算一遍字集，而不是读清单里的 fontMissing：
//
//	fontMissing 是被测系统自己写的结论。打包器漏收一个文本来源时，它的字集就少几个字，
//	覆盖率当然「100%」——账本和账一起错。这里从**包内实际文件**重建字集
//	（标题取自清单、字幕解析 VTT、播放器文案读 app.js），与打包器无关。
type FontCoverage struct {
	// Embedded 表示包里是否内嵌了字体（清单声明 + 文件存在）。
	Embedded bool
	// Family 是清单声明的族名。
	Family string
	// FontBytes 是字体文件体积。
	FontBytes int
	// CharsetSize 是重建出的字集大小。
	CharsetSize int
	// Missing 是字体里没有字形的字符。
	Missing []rune
	// DeclaredMissing 是清单自己报的缺字（与被测结论对照用）。
	DeclaredMissing []string
}

// OK 判断覆盖率是否达标：内嵌了字体且没有缺字。
//
// 没内嵌字体时返回 false 而不是 true——门禁不该为「没做的事」打勾。
// 调用方需要区分「不适用」时看 Embedded。
func (c FontCoverage) OK() bool { return c.Embedded && len(c.Missing) == 0 }

// CheckEmbeddedFont 校验成品包内嵌字体的覆盖率。
func CheckEmbeddedFont(manifest *Manifest, files map[string][]byte) (FontCoverage, error) {
	rep := FontCoverage{}
	if manifest == nil {
		return rep, errors.New("share: font coverage: nil manifest")
	}
	if len(manifest.Fonts) == 0 {
		return rep, nil
	}
	rep.Family = manifest.Fonts[0]
	rep.DeclaredMissing = manifest.FontMissing
	data, ok := files[fontEntryPath]
	if !ok {
		return rep, fmt.Errorf("share: font coverage: 清单声明了字体 %q，包里却没有 %s",
			rep.Family, fontEntryPath)
	}
	if _, ok := files[FileFontCSS]; !ok {
		return rep, fmt.Errorf("share: font coverage: 有字体文件却没有 %s，浏览器不会加载它", FileFontCSS)
	}
	rep.Embedded = true
	rep.FontBytes = len(data)

	cov, err := fonts.ReadFontCoverage(data)
	if err != nil {
		return rep, fmt.Errorf("share: font coverage: %w", err)
	}
	vtt, ok := files[FileSubtitlesVTT]
	if !ok {
		return rep, fmt.Errorf("share: font coverage: 包内没有 %s，无法重建字集", FileSubtitlesVTT)
	}
	charset := CollectFontCharset(manifest.Title, vttTexts(vtt), files[FileAppJS])
	rep.CharsetSize = len(charset.Runes)
	rep.Missing = cov.Missing(charset.Runes)
	return rep, nil
}

// vttTexts 从 VTT 里取字幕文本。
//
// 只认「不是 WEBVTT 头、不是时间轴、不是 NOTE/STYLE 块」的行：VTT 的文本行就是
// 其余所有行。不做完整的 WebVTT 解析——这里只需要字符集合，而错误的解析方式
// （例如漏掉带 markup 的行）会让字集偏小，进而让覆盖率虚高。所以宁可多收：
// 时间轴以外的每一行都算文本，包括空的（空行不贡献字符）。
func vttTexts(data []byte) []string {
	var out []string
	skipBlock := false
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
			skipBlock = false
			continue
		case line == "WEBVTT" || strings.HasPrefix(line, "WEBVTT "):
			continue
		case strings.HasPrefix(line, "NOTE"), strings.HasPrefix(line, "STYLE"), strings.HasPrefix(line, "REGION"):
			skipBlock = true
			continue
		case strings.Contains(line, "-->"):
			// 时间轴行的下一行起是文本。
			continue
		case skipBlock:
			continue
		}
		// 纯数字行是 cue 序号（VTT 里可选）。
		if isAllDigits(line) {
			continue
		}
		// 去掉行内 markup（<b>、<c.foo>、<00:00:01.500> 这类时间戳标签）。
		out = append(out, stripVTTTags(line))
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func stripVTTTags(s string) string {
	var b strings.Builder
	depth := 0
	for _, r := range s {
		switch r {
		case '<':
			depth++
		case '>':
			if depth > 0 {
				depth--
			}
		default:
			if depth == 0 {
				b.WriteRune(r)
			}
		}
	}
	return b.String()
}
