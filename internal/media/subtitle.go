package media

import (
	"errors"
	"fmt"
	"html"
	"strings"
	"unicode"
)

// RenderSRT renders subtitle cues without recalculating their offsets.
func RenderSRT(cues []SubtitleCue) ([]byte, error) {
	if err := validateSubtitleCues(cues); err != nil {
		return nil, err
	}
	var out strings.Builder
	for i, cue := range cues {
		fmt.Fprintf(&out, "%d\r\n%s --> %s\r\n%s\r\n\r\n",
			i+1, formatTimestamp(cue.StartUS, ','), formatTimestamp(cue.EndUS, ','), normalizeCueText(cue.Text))
	}
	return []byte(out.String()), nil
}

// RenderWebVTT renders browser subtitles from the same cue list as SRT.
func RenderWebVTT(cues []SubtitleCue) ([]byte, error) {
	if err := validateSubtitleCues(cues); err != nil {
		return nil, err
	}
	var out strings.Builder
	out.WriteString("WEBVTT\n\n")
	for _, cue := range cues {
		fmt.Fprintf(&out, "%s --> %s\n%s\n\n",
			formatTimestamp(cue.StartUS, '.'), formatTimestamp(cue.EndUS, '.'), html.EscapeString(normalizeCueText(cue.Text)))
	}
	return []byte(out.String()), nil
}

func validateSubtitleCues(cues []SubtitleCue) error {
	var previousEnd int64
	for _, cue := range cues {
		if cue.StartUS < previousEnd || cue.EndUS <= cue.StartUS || cue.StartUS < 0 {
			return errors.New("media: subtitle cues must be positive, ordered, and non-overlapping")
		}
		if cue.EndUS/1000 <= cue.StartUS/1000 {
			return errors.New("media: subtitle cue collapses at millisecond precision")
		}
		if strings.TrimSpace(cue.Text) == "" {
			return errors.New("media: subtitle cue text is empty")
		}
		previousEnd = cue.EndUS
	}
	return nil
}

func formatTimestamp(microseconds int64, millisecondSeparator byte) string {
	totalMS := microseconds / 1000
	hours := totalMS / 3_600_000
	minutes := totalMS / 60_000 % 60
	seconds := totalMS / 1000 % 60
	milliseconds := totalMS % 1000
	return fmt.Sprintf("%02d:%02d:%02d%c%03d", hours, minutes, seconds, millisecondSeparator, milliseconds)
}

func normalizeCueText(text string) string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	return strings.TrimSpace(text)
}

// ASSOptions 控制烧录字幕（ASS）的渲染参数。
type ASSOptions struct {
	// FontName 为字体族名；空 = sans-serif（libass 走默认）。
	FontName string
	// PlayResX/PlayResY 为脚本分辨率，应与目标视频一致（缺省 1920x1080）。
	PlayResX, PlayResY int
	// FontSize 为字号（按 PlayRes 缩放，缺省 52）。
	FontSize int
	// MarginV 为底部边距（缺省 60）。
	MarginV int
	// Watermark 是压在画面右上角的水印文案（ADR-029 ②）；空 = 不加。
	//
	// 走 ASS 而不是 drawtext：字幕烧录已经依赖 libass，再加一条 Dialogue 不需要
	// 新滤镜、不需要在 filtergraph 里转义字体文件路径（Windows 盘符与反斜杠是这里
	// 最容易出错的地方），也不会因为 drawtext 缺 fontconfig 而在某些镜像里静默不画。
	Watermark string
	// DurationUS 是水印的显示时长（微秒）。有水印时必填：ASS 的 Dialogue 需要
	// 明确的起止时间，而"整片多久"只有调用方知道（字幕 cue 的末尾不等于片尾）。
	DurationUS int64
}

// 与前端 playerClock.splitLines 保持一致的断行参数：烧录字幕与播放器逐行轮换同规则，
// 避免"视频里一行、网页里另一行"。窄到 SUBTITLE_LINE_MIN 才找句读断点，最长 SUBTITLE_LINE_MAX。
const (
	subtitleLineMin = 12
	subtitleLineMax = 34
)

var subtitleBreakRunes = map[rune]bool{
	'。': true, '！': true, '？': true, '；': true, '，': true, '、': true,
	'.': true, '!': true, '?': true, ';': true, ',': true,
}

// RenderASS 把时间轴字幕渲染成 ASS：每个 cue 按标点/长度切成单行轮换，行内用 \k 卡拉OK
// 标签让"已朗读"部分随进度变色（PrimaryColour=已读高亮，SecondaryColour=未读），
// 与网页播放器的逐行轮换 + 已读高亮一致。
func RenderASS(cues []SubtitleCue, opts ASSOptions) ([]byte, error) {
	if err := validateSubtitleCues(cues); err != nil {
		return nil, err
	}
	playX, playY := opts.PlayResX, opts.PlayResY
	if playX <= 0 {
		playX = 1920
	}
	if playY <= 0 {
		playY = 1080
	}
	fontSize := opts.FontSize
	if fontSize <= 0 {
		fontSize = 52
	}
	marginV := opts.MarginV
	if marginV <= 0 {
		marginV = 60
	}
	font := strings.TrimSpace(opts.FontName)
	if font == "" {
		font = "sans-serif"
	}
	// Primary=已朗读(#facc15)，Secondary=未朗读(白)，与 playerClock/CSS 的高亮色一致。
	const (
		spokenColor   = "&H0015CCFA"
		unspokenColor = "&H00FFFFFF"
		outlineColor  = "&H00000000"
		backColor     = "&H64000000"
	)

	var b strings.Builder
	b.WriteString("[Script Info]\n")
	b.WriteString("ScriptType: v4.00+\n")
	fmt.Fprintf(&b, "PlayResX: %d\n", playX)
	fmt.Fprintf(&b, "PlayResY: %d\n", playY)
	b.WriteString("WrapStyle: 2\n")
	b.WriteString("ScaledBorderAndShadow: yes\n\n")
	b.WriteString("[V4+ Styles]\n")
	b.WriteString("Format: Name, Fontname, Fontsize, PrimaryColour, SecondaryColour, OutlineColour, BackColour, Bold, Italic, Underline, StrikeOut, ScaleX, ScaleY, Spacing, Angle, BorderStyle, Outline, Shadow, Alignment, MarginL, MarginR, MarginV, Encoding\n")
	fmt.Fprintf(&b, "Style: Default,%s,%d,%s,%s,%s,%s,0,0,0,0,100,100,0,0,1,3,1,2,60,60,%d,1\n\n",
		font, fontSize, spokenColor, unspokenColor, outlineColor, backColor, marginV)
	// 水印样式（ADR-029 ②）：右上角、半透明白字黑边。
	//
	// 选右上角不是随手定的：字幕固定在底部居中，水印压在右下会与字幕互相遮挡，
	// 而左下常有页码等版式元素。右上角在纯色与图片背景上都能看清。
	watermark := strings.TrimSpace(opts.Watermark)
	watermarkSize := playY * 30 / 1080
	if watermarkSize < 18 {
		watermarkSize = 18
	}
	if watermark != "" {
		// 时长拿不到就不画：ASS 没有"到片尾"的写法，写死一个猜测值会让水印
		// 在长片上提前消失——比不加水印更糟，因为没人会发现。
		if opts.DurationUS <= 0 {
			return nil, errors.New("media: watermark requires a positive duration")
		}
		// 白色填充 + 深色描边（Outline 1.5）：浅色页与深色页都能读。
		//
		// 描边不透明度从 0x90 收紧到 0x20，是实测出来的：0x90（约 56% 不透明）在纯白页上
		// 只剩一圈淡灰，字几乎化在背景里；水印既然要"标明出处"，就得先看得见。
		fmt.Fprintf(&b, "Style: Watermark,%s,%d,&H40FFFFFF,&H40FFFFFF,&H20000000,&H00000000,0,0,0,0,100,100,0,0,1,1.5,0,9,40,40,40,1\n\n",
			font, watermarkSize)
	} else {
		b.WriteString("\n")
	}
	b.WriteString("[Events]\n")
	b.WriteString("Format: Layer, Start, End, Style, Name, MarginL, MarginR, MarginV, Effect, Text\n")

	for _, cue := range cues {
		runes := []rune(cue.Text)
		// 仅当字符级时间戳与展示文本逐字对齐时才用它做行/高亮定位；否则退化为按行均分。
		useChars := len(cue.Chars) == len(runes)
		lines := splitCueLines(cue.Text)
		for li, ln := range lines {
			startChar, endChar := ln[0], ln[1]
			lineStartUS, lineEndUS := distributedLine(cue, li, len(lines))
			if useChars && endChar > startChar {
				lineStartUS = cue.Chars[startChar].StartUS
				lineEndUS = cue.Chars[endChar-1].EndUS
			}
			if lineEndUS <= lineStartUS {
				lineEndUS = lineStartUS + 1000
			}
			total := endChar - startChar
			var text strings.Builder
			for ci := startChar; ci < endChar; ci++ {
				var durUS int64
				if useChars {
					durUS = cue.Chars[ci].EndUS - cue.Chars[ci].StartUS
				} else if total > 0 {
					durUS = (lineEndUS - lineStartUS) / int64(total)
				}
				if durUS < 0 {
					durUS = 0
				}
				// 百分之一秒（ASS \k 单位）；四舍五入。
				fmt.Fprintf(&text, "{\\k%d}", (durUS+5000)/10000)
				text.WriteString(escapeASSText(string(runes[ci])))
			}
			fmt.Fprintf(&b, "Dialogue: 0,%s,%s,Default,,0,0,0,,%s\n",
				formatASSTimestamp(lineStartUS), formatASSTimestamp(lineEndUS), text.String())
		}
	}
	// 水印排在字幕之后：libass 按出现顺序叠同层事件，写在后面才盖在上层。
	// 位置在右上角、字幕在底部，正常不重叠；但水印文案较长或画布很窄时可能碰上，
	// 那时"标明出处"应该压过字幕，而不是被字幕压掉。
	if watermark != "" {
		fmt.Fprintf(&b, "Dialogue: 0,0:00:00.00,%s,Watermark,,0,0,0,,%s\n",
			formatASSTimestamp(opts.DurationUS), escapeASSText(watermark))
	}
	return []byte(b.String()), nil
}

// splitCueLines 复刻前端 splitLines：优先尊重换行，无换行的整段按标点/长度拆分。
// 返回每个单行片段对应的 [startChar, endChar) 字符（rune）下标区间。
func splitCueLines(text string) [][2]int {
	runes := []rune(text)
	out := make([][2]int, 0)
	trim := func(start, end int) (int, int, bool) {
		for start < end && unicode.IsSpace(runes[start]) {
			start++
		}
		for end > start && unicode.IsSpace(runes[end-1]) {
			end--
		}
		return start, end, start < end
	}
	pushWrapped := func(start, end int) {
		cursor := start
		for cursor < end {
			hardLimit := end
			if cursor+subtitleLineMax < end {
				hardLimit = cursor + subtitleLineMax
			}
			next := hardLimit
			if hardLimit < end {
				breakAt := -1
				for i := cursor + subtitleLineMin; i <= hardLimit; i++ {
					if subtitleBreakRunes[runes[i-1]] {
						breakAt = i
					}
				}
				if breakAt < 0 {
					for i := hardLimit; i > cursor+subtitleLineMin; i-- {
						if unicode.IsSpace(runes[i-1]) {
							breakAt = i
							break
						}
					}
				}
				if breakAt > cursor {
					next = breakAt
				}
			}
			if s, e, ok := trim(cursor, next); ok {
				out = append(out, [2]int{s, e})
			}
			cursor = next
		}
	}
	start := 0
	for i := 0; i <= len(runes); i++ {
		if i == len(runes) || runes[i] == '\n' {
			pushWrapped(start, i)
			start = i + 1
		}
	}
	if len(out) == 0 {
		out = append(out, [2]int{0, 0})
	}
	return out
}

// distributedLine 在无字符级时间戳时，把 cue 时长按行数均分，保证仍有逐行轮换。
func distributedLine(cue SubtitleCue, index, total int) (int64, int64) {
	if total <= 0 {
		return cue.StartUS, cue.EndUS
	}
	dur := cue.EndUS - cue.StartUS
	start := cue.StartUS + dur*int64(index)/int64(total)
	end := cue.StartUS + dur*int64(index+1)/int64(total)
	return start, end
}

func escapeASSText(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "{", "\\{")
	s = strings.ReplaceAll(s, "}", "\\}")
	s = strings.ReplaceAll(s, "\n", "\\N")
	return s
}

// formatASSTimestamp 输出 H:MM:SS.cc（百分之一秒）。
func formatASSTimestamp(microseconds int64) string {
	if microseconds < 0 {
		microseconds = 0
	}
	cs := microseconds / 10000
	return fmt.Sprintf("%d:%02d:%02d.%02d", cs/360000, cs/6000%60, cs/100%60, cs%100)
}
